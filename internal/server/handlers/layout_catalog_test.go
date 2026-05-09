package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fake resolver ────────────────────────────────────────────────────────────

// fakeCatalogResolver implements diskLayoutCatalogResolver for unit tests.
type fakeCatalogResolver struct {
	nodeDiskLayoutID  string // "" = no FK set on node
	groupDiskLayoutID string // "" = no FK set on group
	layouts           map[string]api.StoredDiskLayout
}

func (f *fakeCatalogResolver) GetNodeDiskLayoutID(_ context.Context, _ string) (string, error) {
	return f.nodeDiskLayoutID, nil
}

func (f *fakeCatalogResolver) GetGroupDiskLayoutID(_ context.Context, _ string) (string, error) {
	return f.groupDiskLayoutID, nil
}

func (f *fakeCatalogResolver) GetDiskLayout(_ context.Context, id string) (api.StoredDiskLayout, error) {
	dl, ok := f.layouts[id]
	if !ok {
		return api.StoredDiskLayout{}, api.ErrNotFound
	}
	return dl, nil
}

func (f *fakeCatalogResolver) ListDiskLayouts(_ context.Context) ([]api.StoredDiskLayout, error) {
	out := make([]api.StoredDiskLayout, 0, len(f.layouts))
	for _, dl := range f.layouts {
		out = append(out, dl)
	}
	return out, nil
}

func newTestLayout(fs string) api.StoredDiskLayout {
	now := time.Now().UTC()
	id := uuid.New().String()
	return api.StoredDiskLayout{
		ID:         id,
		Name:       "test-" + fs,
		CapturedAt: now,
		Layout: api.DiskLayout{
			Partitions: []api.PartitionSpec{
				{Label: "root", SizeBytes: 0, Filesystem: fs, MountPoint: "/"},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ─── resolver unit tests ──────────────────────────────────────────────────────

// TestResolve_NeitherSetFallsThrough verifies that when no disk_layout_id is
// set on either the node or the group, resolveDiskLayoutFromCatalog returns
// ok=false so the caller falls back to the existing logic.
func TestResolve_NeitherSetFallsThrough(t *testing.T) {
	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  "",
		groupDiskLayoutID: "",
		layouts:           make(map[string]api.StoredDiskLayout),
	}

	_, source, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "group-1", "")
	if ok {
		t.Errorf("ok = true, want false (should fall through to existing logic)")
	}
	if source != "" {
		t.Errorf("source = %q, want empty on miss", source)
	}
}

// TestResolve_NodeOverrideUsed verifies that a node disk_layout_id takes
// precedence and returns source "layout_catalog:node".
func TestResolve_NodeOverrideUsed(t *testing.T) {
	nodeDL := newTestLayout("xfs")
	groupDL := newTestLayout("ext4")

	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  nodeDL.ID,
		groupDiskLayoutID: groupDL.ID,
		layouts: map[string]api.StoredDiskLayout{
			nodeDL.ID:  nodeDL,
			groupDL.ID: groupDL,
		},
	}

	layout, source, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "group-1", "")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if source != "layout_catalog:node" {
		t.Errorf("source = %q, want layout_catalog:node", source)
	}
	if len(layout.Partitions) == 0 || layout.Partitions[0].Filesystem != "xfs" {
		t.Errorf("got filesystem %q via node override, want xfs", func() string {
			if len(layout.Partitions) > 0 {
				return layout.Partitions[0].Filesystem
			}
			return ""
		}())
	}
}

// TestResolve_GroupDefaultUsedWhenNoNodeOverride verifies that when the node
// has no disk_layout_id but the group does, the group's layout is used with
// source "layout_catalog:group".
func TestResolve_GroupDefaultUsedWhenNoNodeOverride(t *testing.T) {
	groupDL := newTestLayout("ext4")

	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  "",
		groupDiskLayoutID: groupDL.ID,
		layouts: map[string]api.StoredDiskLayout{
			groupDL.ID: groupDL,
		},
	}

	layout, source, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "group-1", "")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if source != "layout_catalog:group" {
		t.Errorf("source = %q, want layout_catalog:group", source)
	}
	if len(layout.Partitions) == 0 || layout.Partitions[0].Filesystem != "ext4" {
		t.Error("expected ext4 filesystem from group layout")
	}
}

// TestResolve_NodeOverrideIgnoresGroup verifies that a node disk_layout_id is
// used regardless of what the group has set.
func TestResolve_NodeOverrideIgnoresGroup(t *testing.T) {
	nodeDL := newTestLayout("xfs")
	groupDL := newTestLayout("zfs")

	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  nodeDL.ID,
		groupDiskLayoutID: groupDL.ID,
		layouts: map[string]api.StoredDiskLayout{
			nodeDL.ID:  nodeDL,
			groupDL.ID: groupDL,
		},
	}

	layout, source, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "group-1", "")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if source != "layout_catalog:node" {
		t.Errorf("source = %q, want layout_catalog:node (node must beat group)", source)
	}
	if len(layout.Partitions) > 0 && layout.Partitions[0].Filesystem == "zfs" {
		t.Error("group layout was used instead of node override")
	}
}

// TestResolve_NodeFKPointsToMissingRecord verifies that a dangling node FK
// causes a graceful fallback (ok=false) rather than an error.
func TestResolve_NodeFKPointsToMissingRecord(t *testing.T) {
	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  "non-existent-id",
		groupDiskLayoutID: "",
		layouts:           make(map[string]api.StoredDiskLayout),
	}

	_, _, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "", "")
	if ok {
		t.Error("ok = true, want false for dangling FK (should fall back)")
	}
}

// TestResolve_NoGroupID verifies that the group check is skipped entirely
// when groupID is empty.
func TestResolve_NoGroupID(t *testing.T) {
	groupDL := newTestLayout("ext4")
	r := &fakeCatalogResolver{
		nodeDiskLayoutID:  "",
		groupDiskLayoutID: groupDL.ID, // set, but group check skipped because groupID=""
		layouts: map[string]api.StoredDiskLayout{
			groupDL.ID: groupDL,
		},
	}

	_, _, ok := resolveDiskLayoutFromCatalog(context.Background(), r, "node-1", "", "")
	if ok {
		t.Error("ok = true, want false when groupID is empty and no node FK set")
	}
}
