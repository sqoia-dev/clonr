package plugins

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestLimitsPlugin_RendersOneInstructionPerNode verifies that Render always
// returns exactly one InstallInstruction with the correct anchor pair.
func TestLimitsPlugin_RendersOneInstructionPerNode(t *testing.T) {
	p := LimitsPlugin{}

	cases := []struct {
		id       string
		hostname string
		tags     []string
	}{
		{"node-a", "compute01", []string{"compute"}},
		{"node-b", "gpu-01", []string{"gpu"}},
		{"node-c", "login01", []string{"login"}},
		{"node-d", "head-node", nil},
	}

	for _, tc := range cases {
		state := config.ClusterState{
			NodeID: tc.id,
			NodeConfig: api.NodeConfig{
				ID:       tc.id,
				Hostname: tc.hostname,
				Tags:     tc.tags,
			},
		}

		instrs, err := p.Render(state)
		if err != nil {
			t.Errorf("node %s: Render returned unexpected error: %v", tc.id, err)
			continue
		}
		if len(instrs) != 1 {
			t.Errorf("node %s: len(instrs) = %d, want 1", tc.id, len(instrs))
			continue
		}

		instr := instrs[0]
		if instr.Opcode != "overwrite" {
			t.Errorf("node %s: Opcode = %q, want \"overwrite\"", tc.id, instr.Opcode)
		}
		if instr.Target != "/etc/security/limits.conf" {
			t.Errorf("node %s: Target = %q, want \"/etc/security/limits.conf\"", tc.id, instr.Target)
		}
		if instr.Anchors == nil {
			t.Errorf("node %s: Anchors must not be nil — limits.conf uses ANCHORS", tc.id)
			continue
		}
		if instr.Anchors.Begin != limitsAnchorBegin {
			t.Errorf("node %s: Anchors.Begin = %q, want %q", tc.id, instr.Anchors.Begin, limitsAnchorBegin)
		}
		if instr.Anchors.End != limitsAnchorEnd {
			t.Errorf("node %s: Anchors.End = %q, want %q", tc.id, instr.Anchors.End, limitsAnchorEnd)
		}
	}
}

// TestLimitsPlugin_IdempotentSameStateSameOutput verifies that calling Render
// twice with identical ClusterState produces byte-identical output.
func TestLimitsPlugin_IdempotentSameStateSameOutput(t *testing.T) {
	p := LimitsPlugin{}
	state := config.ClusterState{
		NodeID: "node-idem",
		NodeConfig: api.NodeConfig{
			ID:       "node-idem",
			Hostname: "compute-99",
			Tags:     []string{"compute"},
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

// TestLimitsPlugin_HashStableAcrossCalls verifies that HashInstructions
// produces the same digest across repeated calls with identical state.
func TestLimitsPlugin_HashStableAcrossCalls(t *testing.T) {
	p := LimitsPlugin{}
	state := config.ClusterState{
		NodeID: "node-hash",
		NodeConfig: api.NodeConfig{
			ID:       "node-hash",
			Hostname: "compute-42",
			Tags:     []string{"compute"},
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

	// A different role (different tag) must produce a different hash.
	altState := state
	altState.NodeConfig.Tags = []string{"gpu"}
	altInstrs, err := p.Render(altState)
	if err != nil {
		t.Fatalf("Render (alt): %v", err)
	}
	altHash, err := config.HashInstructions(altInstrs)
	if err != nil {
		t.Fatalf("HashInstructions (alt): %v", err)
	}
	if hash1 == altHash {
		t.Error("different role tags produced identical hashes — hash function is broken")
	}
}

// TestLimitsPlugin_Name verifies the stable plugin name used in DB rows.
func TestLimitsPlugin_Name(t *testing.T) {
	p := LimitsPlugin{}
	if got := p.Name(); got != "limits" {
		t.Errorf("Name() = %q, want \"limits\"", got)
	}
}

// TestLimitsPlugin_WatchedKeys verifies that the plugin subscribes to the
// correct literal watch-key and that LimitsWatchKey() returns the same value.
func TestLimitsPlugin_WatchedKeys(t *testing.T) {
	p := LimitsPlugin{}
	keys := p.WatchedKeys()
	if len(keys) != 1 {
		t.Fatalf("WatchedKeys() returned %d keys, want 1: %v", len(keys), keys)
	}
	if keys[0] != limitsWatchKey {
		t.Errorf("WatchedKeys()[0] = %q, want %q", keys[0], limitsWatchKey)
	}
	if LimitsWatchKey() != limitsWatchKey {
		t.Errorf("LimitsWatchKey() = %q, want %q", LimitsWatchKey(), limitsWatchKey)
	}
}

// TestLimitsPlugin_RoleProfiles verifies that each role tag produces a
// distinct limit profile with the expected content.
func TestLimitsPlugin_RoleProfiles(t *testing.T) {
	p := LimitsPlugin{}

	cases := []struct {
		tags    []string
		wantKey string // a substring that distinguishes this profile
	}{
		{[]string{"gpu"}, "unlimited\n"},   // gpu: memlock unlimited
		{[]string{"compute"}, "memlock"},    // compute: has memlock
		{[]string{"login"}, "nproc      4096"}, // login: tighter nproc
		{nil, "nofile"},                     // default: has nofile
	}

	for _, tc := range cases {
		state := config.ClusterState{
			NodeID: "node-role-test",
			NodeConfig: api.NodeConfig{
				ID:       "node-role-test",
				Hostname: "test-node",
				Tags:     tc.tags,
			},
		}

		instrs, err := p.Render(state)
		if err != nil {
			t.Errorf("tags=%v: Render error: %v", tc.tags, err)
			continue
		}
		if len(instrs) == 0 {
			t.Errorf("tags=%v: expected 1 instruction, got 0", tc.tags)
			continue
		}

		if !strings.Contains(instrs[0].Payload, tc.wantKey) {
			t.Errorf("tags=%v: payload missing %q\npayload:\n%s", tc.tags, tc.wantKey, instrs[0].Payload)
		}
	}
}

// TestLimitsPlugin_AnchorsExport verifies that the exported accessor functions
// return the canonical marker strings used by the plugin.
func TestLimitsPlugin_AnchorsExport(t *testing.T) {
	if LimitsAnchorBegin() != limitsAnchorBegin {
		t.Errorf("LimitsAnchorBegin() = %q, want %q", LimitsAnchorBegin(), limitsAnchorBegin)
	}
	if LimitsAnchorEnd() != limitsAnchorEnd {
		t.Errorf("LimitsAnchorEnd() = %q, want %q", LimitsAnchorEnd(), limitsAnchorEnd)
	}
}

// TestLimitsPlugin_GPUPriorityOverCompute verifies that a node tagged both
// "gpu" and "compute" resolves to the gpu profile (highest priority).
func TestLimitsPlugin_GPUPriorityOverCompute(t *testing.T) {
	p := LimitsPlugin{}
	state := config.ClusterState{
		NodeID: "node-gpu-compute",
		NodeConfig: api.NodeConfig{
			ID:       "node-gpu-compute",
			Hostname: "gpu-compute-01",
			Tags:     []string{"compute", "gpu"},
		},
	}

	instrs, err := p.Render(state)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(instrs) == 0 {
		t.Fatal("expected 1 instruction")
	}

	payload := instrs[0].Payload
	// gpu profile has "nproc      unlimited"; compute has "nproc      65536".
	if !strings.Contains(payload, "nproc      unlimited") {
		t.Errorf("expected gpu profile (nproc unlimited) for ['compute','gpu'] node, got:\n%s", payload)
	}
}
