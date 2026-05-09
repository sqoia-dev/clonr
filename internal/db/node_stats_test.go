// internal/db/node_stats_test.go — regression test for Codex post-ship
// review issue #5: QueryNodeStats was applying the expires_at>now TTL
// filter even when the caller supplied an explicit historical Since/
// Until window, silently dropping legitimate past samples whose TTL has
// since elapsed.
package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// openStatsDB returns a fresh DB with the migrations applied.  Mirrors
// the helper in node_variants_test.go but local to this file so the
// test file remains self-contained.
func openStatsDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestQueryNodeStats_HistoricalWindowReturnsExpiredRows pins down the
// fix.  Setup:
//   - one row with TS = 2 hours ago, ExpiresAt = 1 hour ago (already
//     expired at query time)
//   - one row with TS = 2 hours ago, ExpiresAt = nil (never expires)
//
// Query Since/Until covers the 2-hours-ago window.  Both rows must
// appear regardless of IncludeExpired, because the caller explicitly
// asked for that historical window.
func TestQueryNodeStats_HistoricalWindowReturnsExpiredRows(t *testing.T) {
	d := openStatsDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	twoHoursAgo := now.Add(-2 * time.Hour)
	oneHourAgo := now.Add(-time.Hour)

	rows := []db.NodeStatRow{
		{
			NodeID:    "node-1",
			Plugin:    "bmc",
			Sensor:    "cpu_temp",
			Value:     42.0,
			Unit:      "C",
			TS:        twoHoursAgo,
			ExpiresAt: &oneHourAgo, // expired well before "now"
		},
		{
			NodeID: "node-1",
			Plugin: "stream",
			Sensor: "cpu_temp",
			Value:  43.0,
			Unit:   "C",
			TS:     twoHoursAgo,
			// no expiry
		},
	}
	if err := d.InsertStatsBatch(ctx, rows); err != nil {
		t.Fatalf("InsertStatsBatch: %v", err)
	}

	// Default IncludeExpired=false: with the fix, the time-range
	// query returns BOTH rows since the caller is asking for a
	// historical window.
	got, _, err := d.QueryNodeStats(ctx, db.QueryNodeStatsParams{
		NodeID: "node-1",
		Since:  twoHoursAgo.Add(-5 * time.Minute),
		Until:  twoHoursAgo.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("QueryNodeStats: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (both should be visible in a "+
			"historical window even with IncludeExpired=false): %+v", len(got), got)
	}
	// Sanity: the expired bmc row is one of the two.
	foundBMC := false
	for _, r := range got {
		if r.Plugin == "bmc" {
			foundBMC = true
			if r.ExpiresAt == nil {
				t.Errorf("bmc row missing expires_at on read")
			}
		}
	}
	if !foundBMC {
		t.Errorf("expired bmc row dropped from historical window: %+v", got)
	}
}
