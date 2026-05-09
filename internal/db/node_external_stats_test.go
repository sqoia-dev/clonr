package db_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// openExternalDB is a small replica of openTestDB scoped to this file
// so the new tests don't depend on db_test.go internals.
func openExternalDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "external.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestUpsertExternalStat_RoundTrip(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(60 * time.Minute)

	row := db.NodeExternalStatRow{
		NodeID:     "n1",
		Source:     db.ExternalSourceProbe,
		Payload:    json.RawMessage(`{"ping":true,"ssh":false,"ipmi_mc":true}`),
		LastSeenAt: now,
		ExpiresAt:  expires,
	}
	if err := d.UpsertExternalStat(ctx, row); err != nil {
		t.Fatalf("UpsertExternalStat: %v", err)
	}

	got, err := d.ListExternalStatsForNode(ctx, "n1", now)
	if err != nil {
		t.Fatalf("ListExternalStatsForNode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List: got %d rows, want 1", len(got))
	}
	if got[0].Source != db.ExternalSourceProbe {
		t.Fatalf("Source: %q, want probe", got[0].Source)
	}
	if string(got[0].Payload) != string(row.Payload) {
		t.Fatalf("Payload: got %s, want %s", got[0].Payload, row.Payload)
	}
	if !got[0].ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt: got %v, want %v", got[0].ExpiresAt, expires)
	}

	// Second upsert with a different payload should overwrite.
	updated := row
	updated.Payload = json.RawMessage(`{"ping":false}`)
	updated.LastSeenAt = now.Add(time.Second)
	if err := d.UpsertExternalStat(ctx, updated); err != nil {
		t.Fatalf("UpsertExternalStat (overwrite): %v", err)
	}
	got, err = d.ListExternalStatsForNode(ctx, "n1", now)
	if err != nil {
		t.Fatalf("List after overwrite: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after overwrite: %d rows, want 1", len(got))
	}
	if string(got[0].Payload) != `{"ping":false}` {
		t.Fatalf("after overwrite payload: %s", got[0].Payload)
	}
}

func TestUpsertExternalStat_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	err := d.UpsertExternalStat(context.Background(), db.NodeExternalStatRow{
		NodeID:     "n1",
		Source:     db.ExternalSourceBMC,
		Payload:    json.RawMessage(`{this is not json`),
		LastSeenAt: time.Now(),
		ExpiresAt:  time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected error on invalid JSON payload")
	}
}

func TestListExternalStatsForNode_FiltersExpired(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	// One fresh, one already expired.
	if err := d.UpsertExternalStat(ctx, db.NodeExternalStatRow{
		NodeID: "n1", Source: db.ExternalSourceProbe,
		Payload:    json.RawMessage(`{"fresh":true}`),
		LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("upsert fresh: %v", err)
	}
	if err := d.UpsertExternalStat(ctx, db.NodeExternalStatRow{
		NodeID: "n1", Source: db.ExternalSourceBMC,
		Payload:    json.RawMessage(`{"old":true}`),
		LastSeenAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("upsert expired: %v", err)
	}

	got, err := d.ListExternalStatsForNode(ctx, "n1", now)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List: got %d rows, want 1 (expired filtered)", len(got))
	}
	if got[0].Source != db.ExternalSourceProbe {
		t.Fatalf("Source: %q, want probe", got[0].Source)
	}
}

func TestSweepExpiredExternalStats(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	for _, row := range []db.NodeExternalStatRow{
		{
			NodeID: "a", Source: db.ExternalSourceProbe,
			Payload:    json.RawMessage(`{}`),
			LastSeenAt: now.Add(-time.Hour),
			ExpiresAt:  now.Add(-time.Second), // expired
		},
		{
			NodeID: "b", Source: db.ExternalSourceProbe,
			Payload:    json.RawMessage(`{}`),
			LastSeenAt: now,
			ExpiresAt:  now.Add(time.Hour), // alive
		},
	} {
		if err := d.UpsertExternalStat(ctx, row); err != nil {
			t.Fatalf("upsert %s: %v", row.NodeID, err)
		}
	}

	n, err := d.SweepExpiredExternalStats(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("Sweep deleted %d rows, want 1", n)
	}

	// Re-running should be a no-op.
	n, err = d.SweepExpiredExternalStats(ctx, now)
	if err != nil {
		t.Fatalf("Sweep idempotent: %v", err)
	}
	if n != 0 {
		t.Fatalf("Sweep idempotent deleted %d rows, want 0", n)
	}

	// "b" must still be there.
	got, err := d.ListExternalStatsForNode(ctx, "b", now)
	if err != nil || len(got) != 1 {
		t.Fatalf("alive row gone: rows=%d err=%v", len(got), err)
	}
}

func TestSweepExpiredNodeStats_LeavesNullExpiresAtAlone(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	// Two rows: one with no expiry (legacy clientd push), one TTL-bounded
	// and already expired.
	stable := time.Time{}
	expired := now.Add(-time.Minute)

	if err := d.InsertStatsBatch(ctx, []db.NodeStatRow{
		{
			NodeID: "n1", Plugin: "cpu", Sensor: "load1",
			Value: 1.0, TS: now.Add(-2 * time.Minute),
			ExpiresAt: nil, // no TTL
		},
		{
			NodeID: "n1", Plugin: "probe", Sensor: "ping",
			Value: 1.0, TS: now.Add(-3 * time.Minute),
			ExpiresAt: &expired,
		},
	}); err != nil {
		t.Fatalf("InsertStatsBatch: %v", err)
	}
	_ = stable // mark as used

	// Sweep should delete only the TTL-bounded row.
	n, err := d.SweepExpiredNodeStats(ctx, now)
	if err != nil {
		t.Fatalf("SweepExpiredNodeStats: %v", err)
	}
	if n != 1 {
		t.Fatalf("Sweep deleted %d rows, want 1", n)
	}

	// The legacy row must still be readable via the historical query
	// (use a since/until window that includes its ts).
	rows, _, err := d.QueryNodeStats(ctx, db.QueryNodeStatsParams{
		NodeID:         "n1",
		Since:          now.Add(-10 * time.Minute),
		Until:          now,
		Limit:          100,
		IncludeExpired: false,
	})
	if err != nil {
		t.Fatalf("QueryNodeStats: %v", err)
	}
	// The legacy row (cpu/load1, no expiry) must still be present;
	// the probe row (TTL-expired then swept) must not.
	if len(rows) != 1 {
		t.Fatalf("QueryNodeStats: got %d rows, want 1", len(rows))
	}
	if rows[0].Plugin != "cpu" || rows[0].Sensor != "load1" {
		t.Fatalf("Wrong row survived: plugin=%q sensor=%q", rows[0].Plugin, rows[0].Sensor)
	}
}

// TestQueryNodeStats_HistoricalWindowReturnsExpiredRowsTooBundle is
// the time-range counterpart kept here next to its inserter for
// Sprint 38 STAT-EXPIRES regression coverage.
//
// Codex post-ship review issue #5 reversed the semantics for
// time-range queries: the TTL filter is a "current values" concept,
// so callers who pass an explicit Since/Until window now see expired
// rows in that window regardless of IncludeExpired.  The Prometheus
// scrape path (QueryLatestNodeStats) keeps the filter, since "latest
// per (plugin,sensor)" is the view the TTL was designed for.
//
// This test asserts BOTH branches of the new behaviour:
//   - Time-range query (Since/Until set, IncludeExpired=false) returns
//     both rows: the expired one is in the window, so it surfaces.
//   - IncludeExpired=true is the same shape (both rows) — the flag is
//     effectively a no-op once the time-range guard is engaged.
//
// QueryLatestNodeStats's filter is exercised by the dedicated
// "TestQueryLatestNodeStats_*" tests elsewhere in this package.
func TestQueryNodeStats_FiltersExpiresAtByDefault(t *testing.T) {
	t.Parallel()
	d := openExternalDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	expired := now.Add(-time.Second)

	if err := d.InsertStatsBatch(ctx, []db.NodeStatRow{
		{
			NodeID: "n1", Plugin: "probe", Sensor: "ping",
			Value: 1, TS: now.Add(-time.Minute),
			ExpiresAt: &expired, // expired one second ago
		},
		{
			NodeID: "n1", Plugin: "cpu", Sensor: "load1",
			Value: 0.5, TS: now.Add(-time.Minute),
			ExpiresAt: nil, // no expiry → always current
		},
	}); err != nil {
		t.Fatalf("InsertStatsBatch: %v", err)
	}

	// Issue #5 semantics: time-range query returns BOTH rows, even
	// with IncludeExpired=false, because the caller asked for a
	// historical window.
	rows, _, err := d.QueryNodeStats(ctx, db.QueryNodeStatsParams{
		NodeID: "n1", Since: now.Add(-time.Hour), Until: now, Limit: 100,
	})
	if err != nil {
		t.Fatalf("QueryNodeStats: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("time-range query: got %d rows, want 2 (both rows must surface in a historical window — issue #5)", len(rows))
	}

	rows, _, err = d.QueryNodeStats(ctx, db.QueryNodeStatsParams{
		NodeID: "n1", Since: now.Add(-time.Hour), Until: now, Limit: 100,
		IncludeExpired: true,
	})
	if err != nil {
		t.Fatalf("QueryNodeStats include-expired: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("include-expired: got %d rows, want 2", len(rows))
	}
}
