package db_test

import (
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// seedNode inserts a minimal node_configs row so the FK constraint on
// config_render_state is satisfied.
func seedNode(t *testing.T, d *db.DB, id string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         id,
		Hostname:   id,
		PrimaryMAC: "aa:bb:cc:dd:ee:ff",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("seedNode %q: %v", id, err)
	}
}

// TestConfigRenderState_GetRenderHashMissing asserts that GetRenderHash
// returns ("", nil) when no row exists for the (node, plugin) pair.
func TestConfigRenderState_GetRenderHashMissing(t *testing.T) {
	d := openTestDB(t)
	seedNode(t, d, "node-rhs-01")

	hash, err := d.GetRenderHash(t.Context(), "node-rhs-01", "hostname")
	if err != nil {
		t.Fatalf("GetRenderHash: %v", err)
	}
	if hash != "" {
		t.Errorf("GetRenderHash on missing row = %q, want empty string", hash)
	}
}

// TestConfigRenderState_UpsertIsIdempotent asserts that calling UpsertRenderHash
// twice with the same arguments produces exactly one row and returns the last hash.
func TestConfigRenderState_UpsertIsIdempotent(t *testing.T) {
	d := openTestDB(t)
	seedNode(t, d, "node-rhs-02")

	now := time.Now().UTC().Truncate(time.Second)
	hash1 := "abc123"
	hash2 := "def456"

	if err := d.UpsertRenderHash(t.Context(), "node-rhs-02", "hostname", hash1, now, time.Time{}); err != nil {
		t.Fatalf("UpsertRenderHash (first): %v", err)
	}
	// Upsert again with a different hash — should update, not insert a second row.
	if err := d.UpsertRenderHash(t.Context(), "node-rhs-02", "hostname", hash2, now, time.Time{}); err != nil {
		t.Fatalf("UpsertRenderHash (second): %v", err)
	}

	got, err := d.GetRenderHash(t.Context(), "node-rhs-02", "hostname")
	if err != nil {
		t.Fatalf("GetRenderHash: %v", err)
	}
	if got != hash2 {
		t.Errorf("GetRenderHash = %q, want %q (second upsert should win)", got, hash2)
	}
}

// TestConfigRenderState_PushedAtRoundTrip asserts that a non-zero pushedAt is
// stored and retrieved correctly.
func TestConfigRenderState_PushedAtRoundTrip(t *testing.T) {
	d := openTestDB(t)
	seedNode(t, d, "node-rhs-03")

	rendered := time.Now().UTC().Truncate(time.Second)
	pushed := rendered.Add(100 * time.Millisecond).Truncate(time.Second)

	if err := d.UpsertRenderHash(t.Context(), "node-rhs-03", "hostname", "aaa", rendered, pushed); err != nil {
		t.Fatalf("UpsertRenderHash: %v", err)
	}

	row, err := d.GetRenderState(t.Context(), "node-rhs-03", "hostname")
	if err != nil {
		t.Fatalf("GetRenderState: %v", err)
	}
	if row == nil {
		t.Fatal("GetRenderState returned nil, want a row")
	}
	if !row.PushedAt.Equal(pushed) {
		t.Errorf("PushedAt = %v, want %v", row.PushedAt, pushed)
	}
}

// TestConfigRenderState_DeleteForNodeCascades asserts that DeleteForNode removes
// all rows for the given node.
func TestConfigRenderState_DeleteForNodeCascades(t *testing.T) {
	d := openTestDB(t)
	seedNode(t, d, "node-rhs-04")

	now := time.Now().UTC().Truncate(time.Second)
	for _, plugin := range []string{"hostname", "hosts", "sssd"} {
		if err := d.UpsertRenderHash(t.Context(), "node-rhs-04", plugin, "hash-"+plugin, now, time.Time{}); err != nil {
			t.Fatalf("UpsertRenderHash(%q): %v", plugin, err)
		}
	}

	if err := d.DeleteForNode(t.Context(), "node-rhs-04"); err != nil {
		t.Fatalf("DeleteForNode: %v", err)
	}

	for _, plugin := range []string{"hostname", "hosts", "sssd"} {
		hash, err := d.GetRenderHash(t.Context(), "node-rhs-04", plugin)
		if err != nil {
			t.Fatalf("GetRenderHash(%q) after delete: %v", plugin, err)
		}
		if hash != "" {
			t.Errorf("GetRenderHash(%q) after DeleteForNode = %q, want empty string", plugin, hash)
		}
	}
}

// TestConfigRenderState_PushAttemptsIncrement asserts that each UpsertRenderHash
// increments push_attempts.
func TestConfigRenderState_PushAttemptsIncrement(t *testing.T) {
	d := openTestDB(t)
	seedNode(t, d, "node-rhs-05")

	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		if err := d.UpsertRenderHash(t.Context(), "node-rhs-05", "hostname", "h", now, time.Time{}); err != nil {
			t.Fatalf("UpsertRenderHash iter %d: %v", i, err)
		}
	}

	row, err := d.GetRenderState(t.Context(), "node-rhs-05", "hostname")
	if err != nil {
		t.Fatalf("GetRenderState: %v", err)
	}
	if row == nil {
		t.Fatal("GetRenderState returned nil")
	}
	if row.PushAttempts != 3 {
		t.Errorf("PushAttempts = %d, want 3", row.PushAttempts)
	}
}
