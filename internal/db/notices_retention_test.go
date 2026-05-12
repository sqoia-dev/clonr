package db_test

// notices_retention_test.go — Sprint 43-prime Day 1
//
// Unit tests for SweepDismissedNotices.
// Three notice variants are seeded:
//   - dismissed > 30d ago   → must be deleted
//   - dismissed < 30d ago   → must survive
//   - un-dismissed (nil)    → must survive
//
// The cutoff passed to SweepDismissedNotices is constructed to sit
// between "dismissed long ago" and "dismissed recently" so the test
// is deterministic and does not depend on wall-clock timing beyond
// integer-second resolution.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// seedNotice inserts a notice and optionally dismisses it at dismissedAt.
// dismissedAt == nil means the notice is not dismissed.
func seedNotice(t *testing.T, d *db.DB, body string, dismissedAt *time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	n, err := d.InsertNotice(ctx, db.CreateNoticeParams{
		Body:      body,
		Severity:  "info",
		CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("InsertNotice(%q): %v", body, err)
	}
	if dismissedAt != nil {
		if err := d.DismissNoticeAt(ctx, n.ID, *dismissedAt); err != nil {
			t.Fatalf("DismissNoticeAt(%d): %v", n.ID, err)
		}
	}
	return n.ID
}

// TestSweepDismissedNotices verifies the three-way partition:
//   - dismissed >30d ago → deleted
//   - dismissed <30d ago → survives
//   - un-dismissed       → survives
func TestSweepDismissedNotices(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "notices.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	// Old dismissed: 45 days ago.
	oldDismissed := now.Add(-45 * 24 * time.Hour)
	idOld := seedNotice(t, d, "old dismissed", &oldDismissed)

	// Recent dismissed: 10 days ago.
	recentDismissed := now.Add(-10 * 24 * time.Hour)
	idRecent := seedNotice(t, d, "recent dismissed", &recentDismissed)

	// Un-dismissed: sticky banner.
	idUndismissed := seedNotice(t, d, "sticky undismissed", nil)

	n, err := d.SweepDismissedNotices(ctx, cutoff)
	if err != nil {
		t.Fatalf("SweepDismissedNotices: %v", err)
	}
	if n != 1 {
		t.Errorf("SweepDismissedNotices: want 1 deleted, got %d", n)
	}

	// The old-dismissed row must be gone.
	exists, err := noticeExists(t, d, idOld)
	if err != nil {
		t.Fatalf("checking old row: %v", err)
	}
	if exists {
		t.Errorf("notice %d (old dismissed) should have been deleted", idOld)
	}

	// The recently-dismissed row must survive.
	exists, err = noticeExists(t, d, idRecent)
	if err != nil {
		t.Fatalf("checking recent row: %v", err)
	}
	if !exists {
		t.Errorf("notice %d (recent dismissed) should NOT have been deleted", idRecent)
	}

	// The un-dismissed row must survive.
	exists, err = noticeExists(t, d, idUndismissed)
	if err != nil {
		t.Fatalf("checking undismissed row: %v", err)
	}
	if !exists {
		t.Errorf("notice %d (undismissed) should NOT have been deleted", idUndismissed)
	}
}

// TestSweepDismissedNotices_Noop verifies no rows deleted when all are undismissed.
func TestSweepDismissedNotices_Noop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "notices-noop.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	_ = seedNotice(t, d, "sticky-1", nil)
	_ = seedNotice(t, d, "sticky-2", nil)

	n, err := d.SweepDismissedNotices(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("SweepDismissedNotices: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 deleted, got %d", n)
	}
}

// TestSweepDismissedNotices_Idempotent verifies a second sweep on an already-clean
// table returns 0.
func TestSweepDismissedNotices_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "notices-idem.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// One notice dismissed 60 days ago.
	old := now.Add(-60 * 24 * time.Hour)
	_ = seedNotice(t, d, "very old", &old)

	cutoff := now.Add(-30 * 24 * time.Hour)

	n1, err := d.SweepDismissedNotices(ctx, cutoff)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("first sweep: want 1, got %d", n1)
	}

	n2, err := d.SweepDismissedNotices(ctx, cutoff)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second sweep (idempotent): want 0, got %d", n2)
	}
}

// noticeExists checks whether a row with the given id is still in the notices table.
func noticeExists(t *testing.T, d *db.DB, id int64) (bool, error) {
	t.Helper()
	return d.NoticeExists(context.Background(), id)
}
