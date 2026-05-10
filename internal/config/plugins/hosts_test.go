package plugins

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// testNodes returns a minimal set of NodeConfig values suitable for hosts
// plugin tests. Each node has a unique hostname and interface IP.
func testNodes() []api.NodeConfig {
	return []api.NodeConfig{
		{
			ID:       "node-a",
			Hostname: "compute01",
			FQDN:     "compute01.cluster.local",
			Interfaces: []api.InterfaceConfig{
				{IPAddress: "10.0.0.1/24"},
			},
		},
		{
			ID:       "node-b",
			Hostname: "compute02",
			FQDN:     "compute02.cluster.local",
			Interfaces: []api.InterfaceConfig{
				{IPAddress: "10.0.0.2/24"},
			},
		},
		{
			ID:       "node-c",
			Hostname: "head-node",
			FQDN:     "head-node.cluster.local",
			Interfaces: []api.InterfaceConfig{
				{IPAddress: "10.0.0.254/24"},
			},
		},
	}
}

// TestHostsPlugin_RendersOneInstructionPerNode verifies that Render returns
// exactly one InstallInstruction with the expected anchor pair and content.
func TestHostsPlugin_RendersOneInstructionPerNode(t *testing.T) {
	p := HostsPlugin{}
	nodes := testNodes()

	state := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: nodes[0],
		AllNodes:   nodes,
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render returned unexpected error: %v", err)
	}
	if len(instrs) != 1 {
		t.Fatalf("len(instrs) = %d, want 1", len(instrs))
	}

	instr := instrs[0]
	if instr.Opcode != "overwrite" {
		t.Errorf("Opcode = %q, want \"overwrite\"", instr.Opcode)
	}
	if instr.Target != "/etc/hosts" {
		t.Errorf("Target = %q, want \"/etc/hosts\"", instr.Target)
	}
	if instr.Anchors == nil {
		t.Fatal("Anchors must not be nil — /etc/hosts uses ANCHORS to preserve OS entries")
	}
	if instr.Anchors.Begin != hostsAnchorBegin {
		t.Errorf("Anchors.Begin = %q, want %q", instr.Anchors.Begin, hostsAnchorBegin)
	}
	if instr.Anchors.End != hostsAnchorEnd {
		t.Errorf("Anchors.End = %q, want %q", instr.Anchors.End, hostsAnchorEnd)
	}

	// Content must contain all three nodes.
	for _, n := range nodes {
		if n.Interfaces[0].IPAddress == "" {
			continue
		}
		ip := extractIPFromCIDR(n.Interfaces[0].IPAddress)
		if !strings.Contains(instr.Payload, ip) {
			t.Errorf("Payload missing IP %q for node %s\ncontent:\n%s", ip, n.Hostname, instr.Payload)
		}
		if !strings.Contains(instr.Payload, n.Hostname) {
			t.Errorf("Payload missing hostname %q\ncontent:\n%s", n.Hostname, instr.Payload)
		}
	}
}

// TestHostsPlugin_EmptyAllNodesReturnsNil verifies that a state with no nodes
// produces a nil slice and no error.
func TestHostsPlugin_EmptyAllNodesReturnsNil(t *testing.T) {
	p := HostsPlugin{}
	state := config.ClusterState{
		NodeID:   "node-x",
		AllNodes: nil,
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Errorf("Render returned unexpected error for empty AllNodes: %v", err)
	}
	if instrs != nil {
		t.Errorf("expected nil instructions for empty AllNodes, got %+v", instrs)
	}
}

// TestHostsPlugin_IdempotentSameStateSameOutput verifies that calling Render
// twice with identical ClusterState produces byte-identical output.
func TestHostsPlugin_IdempotentSameStateSameOutput(t *testing.T) {
	p := HostsPlugin{}
	nodes := testNodes()
	state := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: nodes[0],
		AllNodes:   nodes,
	}

	first, err := p.Render(state)
	if err != nil {
		t.Fatalf("first Render: %v", err)
	}
	second, err := p.Render(state)
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("Render is not idempotent:\n  first  = %+v\n  second = %+v", first, second)
	}
}

// TestHostsPlugin_HashStableAcrossCalls verifies that HashInstructions
// produces the same digest across repeated calls with identical state.
func TestHostsPlugin_HashStableAcrossCalls(t *testing.T) {
	p := HostsPlugin{}
	nodes := testNodes()
	state := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: nodes[0],
		AllNodes:   nodes,
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	hash1, err := config.HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (first): %v", err)
	}
	hash2, err := config.HashInstructions(instrs)
	if err != nil {
		t.Fatalf("HashInstructions (second): %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("hash is not stable: first=%q second=%q", hash1, hash2)
	}

	// A different node IP must produce a different hash.
	altNodes := testNodes()
	altNodes[0].Interfaces[0].IPAddress = "10.0.0.99/24"
	altState := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: altNodes[0],
		AllNodes:   altNodes,
	}
	altInstrs, err := p.Render(altState)
	if err != nil {
		t.Fatalf("Render (alt): %v", err)
	}
	altHash, err := config.HashInstructions(altInstrs)
	if err != nil {
		t.Fatalf("HashInstructions (alt): %v", err)
	}
	if hash1 == altHash {
		t.Error("different node IP produced identical hashes — hash function is broken")
	}
}

// TestHostsPlugin_Name verifies the stable plugin name used in DB rows.
func TestHostsPlugin_Name(t *testing.T) {
	p := HostsPlugin{}
	if got := p.Name(); got != "hosts" {
		t.Errorf("Name() = %q, want \"hosts\"", got)
	}
}

// TestHostsPlugin_WatchedKeys verifies that the plugin subscribes to the
// correct literal watch-key and that HostsWatchKey() returns the same value.
func TestHostsPlugin_WatchedKeys(t *testing.T) {
	p := HostsPlugin{}
	keys := p.WatchedKeys()
	if len(keys) != 1 {
		t.Fatalf("WatchedKeys() returned %d keys, want 1: %v", len(keys), keys)
	}
	if keys[0] != hostsWatchKey {
		t.Errorf("WatchedKeys()[0] = %q, want %q", keys[0], hostsWatchKey)
	}
	if HostsWatchKey() != hostsWatchKey {
		t.Errorf("HostsWatchKey() = %q, want %q", HostsWatchKey(), hostsWatchKey)
	}
}

// TestHostsPlugin_FQDNRenderedWhenDistinct verifies that a node with a FQDN
// distinct from its hostname produces a "IP FQDN hostname" entry.
func TestHostsPlugin_FQDNRenderedWhenDistinct(t *testing.T) {
	p := HostsPlugin{}
	nodes := testNodes()
	state := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: nodes[0],
		AllNodes:   nodes,
	}

	instrs, _ := p.Render(state)
	if len(instrs) == 0 {
		t.Fatal("expected at least one instruction")
	}
	payload := instrs[0].Payload

	// node-a has FQDN "compute01.cluster.local" ≠ hostname "compute01".
	if !strings.Contains(payload, "compute01.cluster.local compute01") {
		t.Errorf("expected FQDN entry 'compute01.cluster.local compute01' not found in payload:\n%s", payload)
	}
}

// TestHostsPlugin_NodeWithNoInterfaceExcluded verifies that nodes without a
// configured interface IP are silently omitted from the hosts block.
func TestHostsPlugin_NodeWithNoInterfaceExcluded(t *testing.T) {
	p := HostsPlugin{}
	nodes := []api.NodeConfig{
		{
			ID:       "node-a",
			Hostname: "compute01",
			Interfaces: []api.InterfaceConfig{
				{IPAddress: "10.0.0.1/24"},
			},
		},
		{
			ID:         "node-b",
			Hostname:   "compute-noip",
			Interfaces: nil, // no interfaces — must be excluded
		},
	}

	state := config.ClusterState{
		NodeID:     "node-a",
		NodeConfig: nodes[0],
		AllNodes:   nodes,
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(instrs) == 0 {
		t.Fatal("expected 1 instruction")
	}

	if strings.Contains(instrs[0].Payload, "compute-noip") {
		t.Error("node with no interface IP should be excluded from hosts block, but was present")
	}
}

// TestHostsPlugin_AnchorsExport verifies that the exported accessor functions
// return the canonical marker strings used by the plugin.
func TestHostsPlugin_AnchorsExport(t *testing.T) {
	if HostsAnchorBegin() != hostsAnchorBegin {
		t.Errorf("HostsAnchorBegin() = %q, want %q", HostsAnchorBegin(), hostsAnchorBegin)
	}
	if HostsAnchorEnd() != hostsAnchorEnd {
		t.Errorf("HostsAnchorEnd() = %q, want %q", HostsAnchorEnd(), hostsAnchorEnd)
	}
}
