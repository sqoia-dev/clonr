package plugins

import (
	"reflect"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestHostnamePlugin_RendersOneInstructionPerNode verifies that Render returns
// exactly one InstallInstruction for a node with a non-empty hostname, and that
// the instruction writes to /etc/hostname with the expected content.
func TestHostnamePlugin_RendersOneInstructionPerNode(t *testing.T) {
	p := HostnamePlugin{}

	nodes := []struct {
		id       string
		hostname string
	}{
		{"node-a", "compute01"},
		{"node-b", "compute02"},
		{"node-c", "head-node"},
	}

	for _, n := range nodes {
		state := config.ClusterState{
			NodeID: n.id,
			NodeConfig: api.NodeConfig{
				ID:       n.id,
				Hostname: n.hostname,
			},
		}

		instrs, err := p.Render(state)
		if err != nil {
			t.Errorf("node %s: Render returned unexpected error: %v", n.id, err)
			continue
		}
		if len(instrs) != 1 {
			t.Errorf("node %s: len(instrs) = %d, want 1", n.id, len(instrs))
			continue
		}

		instr := instrs[0]
		if instr.Opcode != "overwrite" {
			t.Errorf("node %s: Opcode = %q, want \"overwrite\"", n.id, instr.Opcode)
		}
		if instr.Target != "/etc/hostname" {
			t.Errorf("node %s: Target = %q, want \"/etc/hostname\"", n.id, instr.Target)
		}
		wantPayload := n.hostname + "\n"
		if instr.Payload != wantPayload {
			t.Errorf("node %s: Payload = %q, want %q", n.id, instr.Payload, wantPayload)
		}
		if instr.Anchors != nil {
			t.Errorf("node %s: Anchors should be nil (hostname is full-file overwrite), got %+v", n.id, instr.Anchors)
		}
	}
}

// TestHostnamePlugin_EmptyHostnameReturnsNil verifies that a node with an empty
// hostname produces a nil slice and no error — the observer treats nil as
// "nothing to push" and skips the WS send.
func TestHostnamePlugin_EmptyHostnameReturnsNil(t *testing.T) {
	p := HostnamePlugin{}
	state := config.ClusterState{
		NodeID: "node-x",
		NodeConfig: api.NodeConfig{
			ID:       "node-x",
			Hostname: "",
		},
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Errorf("Render returned unexpected error for empty hostname: %v", err)
	}
	if instrs != nil {
		t.Errorf("expected nil instructions for empty hostname, got %+v", instrs)
	}
}

// TestHostnamePlugin_IdempotentSameStateSameOutput verifies that calling Render
// twice with identical ClusterState produces byte-identical output. This is
// required by the Plugin interface contract (side-effect-free, idempotent).
func TestHostnamePlugin_IdempotentSameStateSameOutput(t *testing.T) {
	p := HostnamePlugin{}
	state := config.ClusterState{
		NodeID: "node-idem",
		NodeConfig: api.NodeConfig{
			ID:       "node-idem",
			Hostname: "compute-99",
		},
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

// TestHostnamePlugin_HashStableAcrossCalls verifies that HashInstructions
// produces the same digest across repeated calls with identical state.
// A non-stable hash would cause spurious re-pushes on every observer tick.
func TestHostnamePlugin_HashStableAcrossCalls(t *testing.T) {
	p := HostnamePlugin{}
	state := config.ClusterState{
		NodeID: "node-hash",
		NodeConfig: api.NodeConfig{
			ID:       "node-hash",
			Hostname: "head-01",
		},
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

	// Sanity: a different hostname must produce a different hash.
	altState := state
	altState.NodeConfig.Hostname = "head-02"
	altInstrs, err := p.Render(altState)
	if err != nil {
		t.Fatalf("Render (alt): %v", err)
	}
	altHash, err := config.HashInstructions(altInstrs)
	if err != nil {
		t.Fatalf("HashInstructions (alt): %v", err)
	}
	if hash1 == altHash {
		t.Error("different hostnames produced identical hashes — hash function is broken")
	}
}

// TestHostnamePlugin_Name verifies the stable plugin name used in DB rows.
func TestHostnamePlugin_Name(t *testing.T) {
	p := HostnamePlugin{}
	if got := p.Name(); got != "hostname" {
		t.Errorf("Name() = %q, want \"hostname\"", got)
	}
}

// TestHostnamePlugin_WatchedKeys verifies that the plugin subscribes to the
// correct literal watch-key and that WatchKey() returns the same value.
func TestHostnamePlugin_WatchedKeys(t *testing.T) {
	p := HostnamePlugin{}
	keys := p.WatchedKeys()
	if len(keys) != 1 {
		t.Fatalf("WatchedKeys() returned %d keys, want 1: %v", len(keys), keys)
	}
	if keys[0] != hostnameWatchKey {
		t.Errorf("WatchedKeys()[0] = %q, want %q", keys[0], hostnameWatchKey)
	}
	if WatchKey() != hostnameWatchKey {
		t.Errorf("WatchKey() = %q, want %q", WatchKey(), hostnameWatchKey)
	}
}
