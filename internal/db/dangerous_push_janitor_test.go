package db_test

// dangerous_push_janitor_test.go — Sprint 41 hygiene
//
// Unit test for JanitorSweepDangerousPushes: seeds expired, consumed, and
// active rows and asserts only the active row survives the sweep.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// insertTestPendingPush seeds one pending_dangerous_pushes row for testing.
// All non-critical fields are set to reasonable defaults.
func insertTestPendingPush(t *testing.T, d *db.DB, id string, expiresAt time.Time, consumed bool) {
	t.Helper()
	ctx := context.Background()
	p := db.PendingDangerousPush{
		ID:           id,
		NodeID:       "node-test-" + id,
		PluginName:   "sssd",
		RenderedHash: "hash-" + id,
		PayloadJSON:  `{"target":"sssd","content":"","checksum":"sha256:abc"}`,
		Reason:       "test danger reason",
		Challenge:    "testcluster",
		ExpiresAt:    expiresAt,
		CreatedBy:    "test-actor",
		CreatedAt:    time.Now().UTC().Add(-15 * time.Minute),
		Consumed:     consumed,
	}
	if err := d.InsertPendingDangerousPush(ctx, p); err != nil {
		t.Fatalf("insertTestPendingPush(%s): %v", id, err)
	}
	// If consumed, flip the flag via the DB method (mirrors production path).
	if consumed {
		if err := d.ConsumePendingDangerousPush(ctx, id); err != nil {
			t.Fatalf("consumeTestPendingPush(%s): %v", id, err)
		}
	}
}

// TestJanitorSweepDangerousPushes verifies that:
//   - expired + unconfirmed rows are returned in expiredIDs and deleted
//   - consumed rows are deleted but NOT returned in expiredIDs
//   - active (not expired, not consumed) rows survive
func TestJanitorSweepDangerousPushes(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "janitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Row A: expired and unconfirmed — should be deleted, appear in expiredIDs.
	insertTestPendingPush(t, d, "row-expired-unconfirmed", now.Add(-2*time.Minute), false)

	// Row B: consumed (success or lockout) — should be deleted, NOT in expiredIDs.
	insertTestPendingPush(t, d, "row-consumed", now.Add(10*time.Minute), true)

	// Row C: active (not expired, not consumed) — must survive.
	insertTestPendingPush(t, d, "row-active", now.Add(9*time.Minute), false)

	expiredIDs, totalDeleted, err := d.JanitorSweepDangerousPushes(ctx, now)
	if err != nil {
		t.Fatalf("JanitorSweepDangerousPushes: %v", err)
	}

	// Two rows deleted: expired-unconfirmed + consumed.
	if totalDeleted != 2 {
		t.Errorf("totalDeleted = %d, want 2", totalDeleted)
	}

	// Only the expired-unconfirmed row appears in expiredIDs.
	if len(expiredIDs) != 1 {
		t.Errorf("len(expiredIDs) = %d, want 1", len(expiredIDs))
	} else if expiredIDs[0] != "row-expired-unconfirmed" {
		t.Errorf("expiredIDs[0] = %q, want row-expired-unconfirmed", expiredIDs[0])
	}

	// Active row must still be retrievable.
	active, err := d.GetPendingDangerousPush(ctx, "row-active")
	if err != nil {
		t.Fatalf("GetPendingDangerousPush(row-active) after sweep: %v", err)
	}
	if active.Consumed {
		t.Error("active row was incorrectly marked consumed")
	}

	// Deleted rows must be gone.
	_, errExpired := d.GetPendingDangerousPush(ctx, "row-expired-unconfirmed")
	if errExpired == nil {
		t.Error("expired-unconfirmed row was not deleted")
	}
	_, errConsumed := d.GetPendingDangerousPush(ctx, "row-consumed")
	if errConsumed == nil {
		t.Error("consumed row was not deleted")
	}
}

// TestJanitorSweepDangerousPushes_AllActive verifies a no-op sweep returns zeros.
func TestJanitorSweepDangerousPushes_AllActive(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "janitor-noop.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Only active rows — nothing should be swept.
	insertTestPendingPush(t, d, "active-1", now.Add(5*time.Minute), false)
	insertTestPendingPush(t, d, "active-2", now.Add(8*time.Minute), false)

	expiredIDs, totalDeleted, err := d.JanitorSweepDangerousPushes(ctx, now)
	if err != nil {
		t.Fatalf("JanitorSweepDangerousPushes: %v", err)
	}
	if totalDeleted != 0 {
		t.Errorf("totalDeleted = %d, want 0 (all rows are active)", totalDeleted)
	}
	if len(expiredIDs) != 0 {
		t.Errorf("expiredIDs = %v, want empty", expiredIDs)
	}
}

// TestJanitorSweepDangerousPushes_Idempotent verifies that a second sweep on
// an already-clean table returns (nil, 0, nil).
func TestJanitorSweepDangerousPushes_Idempotent(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "janitor-idempotent.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	insertTestPendingPush(t, d, "will-expire", now.Add(-1*time.Minute), false)

	// First sweep.
	_, n1, err := d.JanitorSweepDangerousPushes(ctx, now)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("first sweep: want 1 deleted, got %d", n1)
	}

	// Second sweep on empty table.
	expiredIDs, n2, err := d.JanitorSweepDangerousPushes(ctx, now)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second sweep: want 0 deleted, got %d", n2)
	}
	if len(expiredIDs) != 0 {
		t.Errorf("second sweep: want empty expiredIDs, got %v", expiredIDs)
	}
}
