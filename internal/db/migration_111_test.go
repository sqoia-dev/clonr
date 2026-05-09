// migration_111_test.go — verify that migration 111 (Sprint 37 DISKLESS
// Bundle A) installs the operating_mode column on node_configs with the
// expected NOT NULL default and CHECK-constrained enum.
package db_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestMigration111_OperatingModeDefaultsToBlockInstall confirms that a node
// created via the production CreateNodeConfig path with an empty OperatingMode
// is persisted with the default 'block_install' value. This is the "every
// pre-existing row at upgrade time still boots correctly" guarantee.
func TestMigration111_OperatingModeDefaultsToBlockInstall(t *testing.T) {
	d := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-mig111-default",
		Hostname:   "node-mig111-default",
		PrimaryMAC: "aa:bb:cc:00:11:22",
		CreatedAt:  now,
		UpdatedAt:  now,
		// OperatingMode intentionally left empty.
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	got, err := d.GetNodeConfig(t.Context(), cfg.ID)
	if err != nil {
		t.Fatalf("GetNodeConfig: %v", err)
	}
	if got.OperatingMode != api.OperatingModeBlockInstall {
		t.Errorf("OperatingMode = %q, want %q (default)", got.OperatingMode, api.OperatingModeBlockInstall)
	}
}

// TestMigration111_OperatingModeRoundTrips writes each accepted enum value via
// the production update path and confirms it round-trips intact.
func TestMigration111_OperatingModeRoundTrips(t *testing.T) {
	d := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-mig111-roundtrip",
		Hostname:   "node-mig111-roundtrip",
		PrimaryMAC: "aa:bb:cc:00:11:33",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	for _, mode := range api.OperatingModeValues {
		cfg.OperatingMode = mode
		if err := d.UpdateNodeConfig(t.Context(), cfg); err != nil {
			t.Fatalf("UpdateNodeConfig(%q): %v", mode, err)
		}
		got, err := d.GetNodeConfig(t.Context(), cfg.ID)
		if err != nil {
			t.Fatalf("GetNodeConfig(%q): %v", mode, err)
		}
		if got.OperatingMode != mode {
			t.Errorf("operating_mode round-trip: got %q, want %q", got.OperatingMode, mode)
		}
	}
}

// TestMigration111_CheckConstraintRejectsBogusValue confirms the SQLite
// CHECK constraint rejects writes outside the enum. This is the "API-layer
// validator bypass cannot strand a node in an unrenderable state" guarantee.
//
// The test bypasses the API layer entirely by issuing raw SQL — exactly the
// failure mode the CHECK constraint exists to defend against.
func TestMigration111_CheckConstraintRejectsBogusValue(t *testing.T) {
	d := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-mig111-bogus",
		Hostname:   "node-mig111-bogus",
		PrimaryMAC: "aa:bb:cc:00:11:44",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	// Direct SQL bypasses the Go-side validation — the CHECK constraint is
	// the canonical guard and must reject this even with no API layer.
	_, err := d.SQL().Exec(
		`UPDATE node_configs SET operating_mode = ? WHERE id = ?`,
		"definitely-not-a-mode", cfg.ID,
	)
	if err == nil {
		t.Fatalf("UPDATE with bogus operating_mode succeeded; expected CHECK constraint to reject")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("expected CHECK constraint error, got: %v", err)
	}

	// Confirm the row was not mutated — value is still whatever the create
	// path wrote (block_install default).
	got, gerr := d.GetNodeConfig(t.Context(), cfg.ID)
	if gerr != nil {
		t.Fatalf("GetNodeConfig: %v", gerr)
	}
	if got.OperatingMode != api.OperatingModeBlockInstall {
		t.Errorf("OperatingMode after rejected UPDATE = %q, want %q (unchanged)", got.OperatingMode, api.OperatingModeBlockInstall)
	}
}
