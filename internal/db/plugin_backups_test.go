package db_test

// plugin_backups_test.go — Sprint 41 Day 4
//
// Tests for plugin_backups CRUD and pruning helpers.
// Follows the openTestDB(t) pattern from db_test.go.

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// seedPendingPush inserts a minimal pending_dangerous_pushes row so that
// plugin_backups rows with a pending_dangerous_push_id can be linked.
func seedPendingPush(t *testing.T, d *db.DB, id, nodeID, pluginName string) {
	t.Helper()
	ctx := context.Background()
	push := db.PendingDangerousPush{
		ID:           id,
		NodeID:       nodeID,
		PluginName:   pluginName,
		RenderedHash: "abc123",
		PayloadJSON:  `{"test":true}`,
		Reason:       "test reason",
		Challenge:    "TYPE:CONFIRM",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
		CreatedBy:    "test-actor",
		CreatedAt:    time.Now(),
	}
	if err := d.InsertPendingDangerousPush(ctx, push); err != nil {
		t.Fatalf("seedPendingPush: %v", err)
	}
}

func makeBackup(id, nodeID, pluginName string, takenAt time.Time) db.PluginBackup {
	return db.PluginBackup{
		ID:         id,
		NodeID:     nodeID,
		PluginName: pluginName,
		BlobPath:   "/var/lib/clustr/backups/" + id + ".tar.gz",
		TakenAt:    takenAt.UTC().Truncate(time.Second),
	}
}

// ─── InsertPluginBackup / GetPluginBackup ────────────────────────────────────

func TestPluginBackup_InsertAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	b := makeBackup("pb-001", "node-aaa", "sssd", time.Now())
	if err := d.InsertPluginBackup(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetPluginBackup(ctx, b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID != b.ID {
		t.Errorf("id: got %s want %s", got.ID, b.ID)
	}
	if got.NodeID != b.NodeID {
		t.Errorf("node_id: got %s want %s", got.NodeID, b.NodeID)
	}
	if got.PluginName != b.PluginName {
		t.Errorf("plugin_name: got %s want %s", got.PluginName, b.PluginName)
	}
	if got.BlobPath != b.BlobPath {
		t.Errorf("blob_path: got %s want %s", got.BlobPath, b.BlobPath)
	}
	if !got.TakenAt.Equal(b.TakenAt) {
		t.Errorf("taken_at: got %v want %v", got.TakenAt, b.TakenAt)
	}
	if got.PendingDangerousPushID != "" {
		t.Errorf("pending_dangerous_push_id: got %q want empty", got.PendingDangerousPushID)
	}
}

func TestPluginBackup_GetNotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetPluginBackup(context.Background(), "does-not-exist")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestPluginBackup_WithPendingDangerousPushID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	pendingID := "dpush-111"
	nodeID := "node-bbb"
	seedPendingPush(t, d, pendingID, nodeID, "sssd")

	b := makeBackup("pb-002", nodeID, "sssd", time.Now())
	b.PendingDangerousPushID = pendingID

	if err := d.InsertPluginBackup(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetPluginBackup(ctx, b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PendingDangerousPushID != pendingID {
		t.Errorf("pending_dangerous_push_id: got %q want %q", got.PendingDangerousPushID, pendingID)
	}
}

// ─── GetPluginBackupByPendingPush ────────────────────────────────────────────

func TestPluginBackup_GetByPendingPush(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	pendingID := "dpush-222"
	nodeID := "node-ccc"
	seedPendingPush(t, d, pendingID, nodeID, "sssd")

	b := makeBackup("pb-003", nodeID, "sssd", time.Now())
	b.PendingDangerousPushID = pendingID
	if err := d.InsertPluginBackup(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := d.GetPluginBackupByPendingPush(ctx, pendingID)
	if err != nil {
		t.Fatalf("get by pending push: %v", err)
	}
	if got.ID != b.ID {
		t.Errorf("id: got %s want %s", got.ID, b.ID)
	}
	if got.PendingDangerousPushID != pendingID {
		t.Errorf("pending_dangerous_push_id mismatch")
	}
}

func TestPluginBackup_GetByPendingPush_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetPluginBackupByPendingPush(context.Background(), "no-such-push")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

// ─── ListPluginBackups ───────────────────────────────────────────────────────

func TestPluginBackup_ListNewestFirst(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-ddd"
	base := time.Now().Truncate(time.Second)

	// Insert three backups at different times (oldest → newest).
	for i, id := range []string{"pb-old", "pb-mid", "pb-new"} {
		b := makeBackup(id, nodeID, "sssd", base.Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	results, err := d.ListPluginBackups(ctx, nodeID, "sssd")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("len: got %d want 3", len(results))
	}

	// Newest first.
	if results[0].ID != "pb-new" {
		t.Errorf("[0].ID: got %s want pb-new", results[0].ID)
	}
	if results[2].ID != "pb-old" {
		t.Errorf("[2].ID: got %s want pb-old", results[2].ID)
	}
}

func TestPluginBackup_ListFilterByNodeID(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Insert two backups for different nodes.
	if err := d.InsertPluginBackup(ctx, makeBackup("pb-n1", "node-111", "sssd", time.Now())); err != nil {
		t.Fatalf("insert n1: %v", err)
	}
	if err := d.InsertPluginBackup(ctx, makeBackup("pb-n2", "node-222", "sssd", time.Now())); err != nil {
		t.Fatalf("insert n2: %v", err)
	}

	results, err := d.ListPluginBackups(ctx, "node-111", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 1 || results[0].ID != "pb-n1" {
		t.Errorf("expected only pb-n1, got %+v", results)
	}
}

func TestPluginBackup_ListFilterByPlugin(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-eee"
	if err := d.InsertPluginBackup(ctx, makeBackup("pb-sssd-1", nodeID, "sssd", time.Now())); err != nil {
		t.Fatalf("insert sssd: %v", err)
	}
	if err := d.InsertPluginBackup(ctx, makeBackup("pb-hosts-1", nodeID, "hostname", time.Now())); err != nil {
		t.Fatalf("insert hostname: %v", err)
	}

	results, err := d.ListPluginBackups(ctx, nodeID, "sssd")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 1 || results[0].PluginName != "sssd" {
		t.Errorf("expected only sssd backup, got %+v", results)
	}
}

func TestPluginBackup_ListNoFilter(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.InsertPluginBackup(ctx, makeBackup("pb-x1", "node-x1", "sssd", time.Now())); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := d.InsertPluginBackup(ctx, makeBackup("pb-x2", "node-x2", "hostname", time.Now())); err != nil {
		t.Fatalf("insert: %v", err)
	}

	results, err := d.ListPluginBackups(ctx, "", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestPluginBackup_ListEmpty(t *testing.T) {
	d := openTestDB(t)
	results, err := d.ListPluginBackups(context.Background(), "node-zzz", "sssd")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty slice, got %d items", len(results))
	}
}

// ─── PrunePluginBackups ───────────────────────────────────────────────────────

func TestPluginBackup_Prune_BelowMax(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-prune-1"
	// Insert 2 backups; maxBackups=5 → nothing pruned.
	for i, id := range []string{"pb-p1", "pb-p2"} {
		b := makeBackup(id, nodeID, "sssd", time.Now().Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	pruned, err := d.PrunePluginBackups(ctx, nodeID, "sssd", 5)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned, got %d: %+v", len(pruned), pruned)
	}

	// All rows should still exist.
	results, err := d.ListPluginBackups(ctx, nodeID, "sssd")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 remaining, got %d", len(results))
	}
}

func TestPluginBackup_Prune_AtExact(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-prune-exact"
	// Insert exactly maxBackups=3 → nothing pruned.
	for i, id := range []string{"pb-e1", "pb-e2", "pb-e3"} {
		b := makeBackup(id, nodeID, "sssd", time.Now().Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	pruned, err := d.PrunePluginBackups(ctx, nodeID, "sssd", 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Errorf("expected 0 pruned at exact limit, got %d", len(pruned))
	}
}

func TestPluginBackup_Prune_ExceedsMax(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-prune-2"
	base := time.Now().Truncate(time.Second)
	ids := []string{"pb-oldest", "pb-middle", "pb-newer", "pb-newer2", "pb-newest"}
	for i, id := range ids {
		b := makeBackup(id, nodeID, "sssd", base.Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// maxBackups=3 → the 2 oldest should be pruned.
	pruned, err := d.PrunePluginBackups(ctx, nodeID, "sssd", 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 2 {
		t.Errorf("expected 2 pruned, got %d: %+v", len(pruned), pruned)
	}

	// Pruned rows should be the oldest ones.
	prunedIDs := map[string]bool{}
	for _, p := range pruned {
		prunedIDs[p.ID] = true
	}
	if !prunedIDs["pb-oldest"] {
		t.Errorf("pb-oldest should be pruned, pruned set: %v", prunedIDs)
	}
	if !prunedIDs["pb-middle"] {
		t.Errorf("pb-middle should be pruned, pruned set: %v", prunedIDs)
	}

	// Confirm 3 remain in DB.
	remaining, err := d.ListPluginBackups(ctx, nodeID, "sssd")
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(remaining) != 3 {
		t.Errorf("expected 3 remaining, got %d", len(remaining))
	}

	// Blob paths returned so the caller can delete tarballs.
	for _, p := range pruned {
		if p.BlobPath == "" {
			t.Errorf("pruned entry %s has empty blob_path", p.ID)
		}
	}
}

func TestPluginBackup_Prune_DoesNotCrossPlugin(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-prune-3"
	// Insert 3 sssd + 3 hostname backups.
	for i, id := range []string{"pb-s1", "pb-s2", "pb-s3"} {
		b := makeBackup(id, nodeID, "sssd", time.Now().Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert sssd %s: %v", id, err)
		}
	}
	for i, id := range []string{"pb-h1", "pb-h2", "pb-h3"} {
		b := makeBackup(id, nodeID, "hostname", time.Now().Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert hostname %s: %v", id, err)
		}
	}

	// Prune sssd to maxBackups=1.
	pruned, err := d.PrunePluginBackups(ctx, nodeID, "sssd", 1)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 2 {
		t.Errorf("expected 2 sssd pruned, got %d", len(pruned))
	}

	// hostname backups are untouched.
	hostnameBackups, err := d.ListPluginBackups(ctx, nodeID, "hostname")
	if err != nil {
		t.Fatalf("list hostname: %v", err)
	}
	if len(hostnameBackups) != 3 {
		t.Errorf("expected 3 hostname backups, got %d", len(hostnameBackups))
	}
}

func TestPluginBackup_Prune_DefaultMax(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	nodeID := "node-prune-default"
	// Insert 5 backups; passing maxBackups=0 should default to 3.
	for i := range 5 {
		id := "pb-def-" + string(rune('a'+i))
		b := makeBackup(id, nodeID, "sssd", time.Now().Add(time.Duration(i)*time.Minute))
		if err := d.InsertPluginBackup(ctx, b); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	pruned, err := d.PrunePluginBackups(ctx, nodeID, "sssd", 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// maxBackups defaults to 3 → 5-3 = 2 pruned.
	if len(pruned) != 2 {
		t.Errorf("expected 2 pruned with default max, got %d", len(pruned))
	}
}

// ─── DeletePluginBackup ───────────────────────────────────────────────────────

func TestPluginBackup_Delete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	b := makeBackup("pb-del-1", "node-fff", "sssd", time.Now())
	if err := d.InsertPluginBackup(ctx, b); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := d.DeletePluginBackup(ctx, b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := d.GetPluginBackup(ctx, b.ID)
	if err != sql.ErrNoRows {
		t.Errorf("after delete, expected sql.ErrNoRows, got %v", err)
	}
}

func TestPluginBackup_Delete_NotFound(t *testing.T) {
	d := openTestDB(t)
	err := d.DeletePluginBackup(context.Background(), "does-not-exist")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}
