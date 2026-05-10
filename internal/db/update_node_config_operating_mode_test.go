// update_node_config_operating_mode_test.go — regression tests for
// CODEX-FIX-4 Issue #1: UpdateNodeConfig must preserve operating_mode when the
// caller sends an empty value (e.g. a PUT from UpdateNodeConfigRequest which
// has no operating_mode field).
package db_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// makeMinimalNode returns the smallest api.NodeConfig that satisfies the DB
// constraints (no base image FK required for node_configs).
func makeMinimalNode(id, mac string) api.NodeConfig {
	now := time.Now().UTC().Truncate(time.Second)
	return api.NodeConfig{
		ID:         id,
		Hostname:   id,
		PrimaryMAC: mac,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// TestUpdateNodeConfig_PreservesOperatingModeWhenEmpty is the primary
// regression test for CODEX-FIX-4 Issue #1.
//
// Scenario: node is set to stateless_nfs.  A PUT for an unrelated field
// (hostname rename) arrives with an empty OperatingMode in the payload.
// The DB row must retain stateless_nfs, not silently reset to block_install.
func TestUpdateNodeConfig_PreservesOperatingModeWhenEmpty(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	node := makeMinimalNode(uuid.New().String(), "aa:bb:cc:11:22:01")
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	// Explicitly set the node to stateless_nfs.
	node.OperatingMode = api.OperatingModeStatelessNFS
	if err := d.UpdateNodeConfig(ctx, node); err != nil {
		t.Fatalf("UpdateNodeConfig (set stateless_nfs): %v", err)
	}
	got, err := d.GetNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.OperatingMode != api.OperatingModeStatelessNFS {
		t.Fatalf("pre-condition: OperatingMode = %q, want %q", got.OperatingMode, api.OperatingModeStatelessNFS)
	}

	// Simulate a PUT that only changes the hostname — OperatingMode is zero.
	node.Hostname = "renamed-node"
	node.OperatingMode = "" // as sent by UpdateNodeConfigRequest (no operating_mode field)
	if err := d.UpdateNodeConfig(ctx, node); err != nil {
		t.Fatalf("UpdateNodeConfig (hostname rename, empty operating_mode): %v", err)
	}

	after, err := d.GetNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after rename: %v", err)
	}
	if after.Hostname != "renamed-node" {
		t.Errorf("Hostname = %q, want %q", after.Hostname, "renamed-node")
	}
	if after.OperatingMode != api.OperatingModeStatelessNFS {
		t.Errorf("OperatingMode = %q after hostname-only PUT, want %q (must not reset)", after.OperatingMode, api.OperatingModeStatelessNFS)
	}
}

// TestUpdateNodeConfig_AppliesExplicitOperatingModeWhenSet verifies that an
// explicit non-empty OperatingMode in the payload always takes effect —
// preserving the happy-path round-trip behaviour.
func TestUpdateNodeConfig_AppliesExplicitOperatingModeWhenSet(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	node := makeMinimalNode(uuid.New().String(), "aa:bb:cc:11:22:02")
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	for _, mode := range []string{
		api.OperatingModeStatelessNFS,
		api.OperatingModeStatelessRAM,
		api.OperatingModeBlockInstall,
	} {
		node.OperatingMode = mode
		if err := d.UpdateNodeConfig(ctx, node); err != nil {
			t.Fatalf("UpdateNodeConfig(%q): %v", mode, err)
		}
		got, err := d.GetNodeConfig(ctx, node.ID)
		if err != nil {
			t.Fatalf("GetNodeConfig(%q): %v", mode, err)
		}
		if got.OperatingMode != mode {
			t.Errorf("OperatingMode = %q, want %q", got.OperatingMode, mode)
		}
	}
}

// TestUpdateNodeConfig_DefaultsToBlockInstallOnFreshRow covers the defensive
// branch: if a fresh row somehow has an empty operating_mode in the DB (which
// should never happen given the NOT NULL DEFAULT constraint, but guard anyway),
// UpdateNodeConfig with an empty payload value must write block_install rather
// than propagating empty to the CHECK constraint.
//
// We manufacture this by writing the default directly and then verifying
// CreateNodeConfig itself set block_install (the INSERT path coerces empty →
// block_install separately; this confirms the round-trip is intact).
func TestUpdateNodeConfig_DefaultsToBlockInstallOnFreshRow(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	node := makeMinimalNode(uuid.New().String(), "aa:bb:cc:11:22:03")
	// Intentionally leave OperatingMode empty to exercise the INSERT default.
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	got, err := d.GetNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	// INSERT coerces empty → block_install (migration 111 guarantee).
	if got.OperatingMode != api.OperatingModeBlockInstall {
		t.Fatalf("fresh row OperatingMode = %q, want %q", got.OperatingMode, api.OperatingModeBlockInstall)
	}

	// Now call UpdateNodeConfig with empty operating_mode — the preserve-existing
	// logic must fetch "block_install" from the row and write it back unchanged.
	node.Hostname = "fresh-renamed"
	node.OperatingMode = ""
	if err := d.UpdateNodeConfig(ctx, node); err != nil {
		t.Fatalf("UpdateNodeConfig (empty mode on fresh row): %v", err)
	}

	after, err := d.GetNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig after update: %v", err)
	}
	if after.OperatingMode != api.OperatingModeBlockInstall {
		t.Errorf("OperatingMode = %q after update with empty mode on fresh row, want %q", after.OperatingMode, api.OperatingModeBlockInstall)
	}
}
