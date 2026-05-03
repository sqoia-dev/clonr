package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Migration smoke test ─────────────────────────────────────────────────────

// TestMigration_DiskLayouts opens a fresh DB (which runs all migrations including
// 087_disk_layouts) and asserts that the disk_layouts table exists with the
// correct schema and that the FK columns were added to node_groups and node_configs.
func TestMigration_DiskLayouts(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Verify disk_layouts table exists by inserting and selecting a row.
	dl := makeDiskLayout("test-layout", "")
	if err := d.CreateDiskLayout(ctx, dl); err != nil {
		t.Fatalf("create disk layout: %v", err)
	}
	got, err := d.GetDiskLayout(ctx, dl.ID)
	if err != nil {
		t.Fatalf("get disk layout: %v", err)
	}
	if got.ID != dl.ID {
		t.Errorf("id = %q, want %q", got.ID, dl.ID)
	}
	if got.Name != dl.Name {
		t.Errorf("name = %q, want %q", got.Name, dl.Name)
	}
	if len(got.Layout.Partitions) != 1 {
		t.Errorf("partitions len = %d, want 1", len(got.Layout.Partitions))
	}

	// Verify FK column exists on node_groups by writing and reading back.
	g := makeGroup("layout-group", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create node group: %v", err)
	}
	if err := d.SetGroupDiskLayoutID(ctx, g.ID, dl.ID); err != nil {
		t.Fatalf("set group disk_layout_id: %v", err)
	}
	gLayoutID, err := d.GetGroupDiskLayoutID(ctx, g.ID)
	if err != nil {
		t.Fatalf("get group disk_layout_id: %v", err)
	}
	if gLayoutID != dl.ID {
		t.Errorf("group disk_layout_id = %q, want %q", gLayoutID, dl.ID)
	}

	// Verify FK column exists on node_configs.
	node := makeTestNode("layout-node", "aa:bb:cc:dd:ee:ff")
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node config: %v", err)
	}
	if err := d.SetNodeDiskLayoutID(ctx, node.ID, dl.ID); err != nil {
		t.Fatalf("set node disk_layout_id: %v", err)
	}
	nLayoutID, err := d.GetNodeDiskLayoutID(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node disk_layout_id: %v", err)
	}
	if nLayoutID != dl.ID {
		t.Errorf("node disk_layout_id = %q, want %q", nLayoutID, dl.ID)
	}
}

// ─── CRUD tests ───────────────────────────────────────────────────────────────

func TestDiskLayout_CreateGetList(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	dl1 := makeDiskLayout("uefi-layout", "")
	dl2 := makeDiskLayout("bios-layout", "node-source-1")

	for _, dl := range []api.StoredDiskLayout{dl1, dl2} {
		if err := d.CreateDiskLayout(ctx, dl); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	got, err := d.GetDiskLayout(ctx, dl1.ID)
	if err != nil {
		t.Fatalf("get dl1: %v", err)
	}
	if got.Name != "uefi-layout" {
		t.Errorf("name = %q, want uefi-layout", got.Name)
	}
	if got.SourceNodeID != "" {
		t.Errorf("source_node_id = %q, want empty", got.SourceNodeID)
	}

	got2, err := d.GetDiskLayout(ctx, dl2.ID)
	if err != nil {
		t.Fatalf("get dl2: %v", err)
	}
	if got2.SourceNodeID != "node-source-1" {
		t.Errorf("source_node_id = %q, want node-source-1", got2.SourceNodeID)
	}

	list, err := d.ListDiskLayouts(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
}

func TestDiskLayout_Update(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	dl := makeDiskLayout("original-name", "")
	if err := d.CreateDiskLayout(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	newLayout := api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{Label: "boot", SizeBytes: 1 << 30, Filesystem: "vfat", MountPoint: "/boot/efi"},
			{Label: "swap", SizeBytes: 8 << 30, Filesystem: "swap", MountPoint: "swap"},
			{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
		},
	}
	if err := d.UpdateDiskLayoutFields(ctx, dl.ID, "updated-name", newLayout); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := d.GetDiskLayout(ctx, dl.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Name != "updated-name" {
		t.Errorf("name = %q, want updated-name", got.Name)
	}
	if len(got.Layout.Partitions) != 3 {
		t.Errorf("partitions = %d, want 3", len(got.Layout.Partitions))
	}
}

func TestDiskLayout_Delete_RefCountGuard(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	dl := makeDiskLayout("compute-layout", "")
	if err := d.CreateDiskLayout(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}

	g := makeGroup("compute-group", "compute")
	if err := d.CreateNodeGroupFull(ctx, g); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := d.SetGroupDiskLayoutID(ctx, g.ID, dl.ID); err != nil {
		t.Fatalf("set group disk_layout_id: %v", err)
	}

	refs, err := d.DiskLayoutRefCount(ctx, dl.ID)
	if err != nil {
		t.Fatalf("ref count: %v", err)
	}
	if refs != 1 {
		t.Errorf("ref count = %d, want 1", refs)
	}

	// Caller should check ref count before calling Delete; simulate the handler guard.
	// After clearing the FK, delete should succeed.
	if err := d.SetGroupDiskLayoutID(ctx, g.ID, ""); err != nil {
		t.Fatalf("clear group disk_layout_id: %v", err)
	}
	if err := d.DeleteDiskLayout(ctx, dl.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err = d.GetDiskLayout(ctx, dl.ID)
	if err == nil {
		t.Error("expected ErrNotFound after delete, got nil")
	}
}

// TestDiskLayout_NodeFKClear verifies that SetNodeDiskLayoutID(ctx, nodeID, "")
// clears the column (sets it to NULL).
func TestDiskLayout_NodeFKClear(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	dl := makeDiskLayout("to-clear", "")
	if err := d.CreateDiskLayout(ctx, dl); err != nil {
		t.Fatalf("create: %v", err)
	}
	node := makeTestNode("fk-node", "11:22:33:44:55:66")
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := d.SetNodeDiskLayoutID(ctx, node.ID, dl.ID); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Now clear it.
	if err := d.SetNodeDiskLayoutID(ctx, node.ID, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	id, err := d.GetNodeDiskLayoutID(ctx, node.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if id != "" {
		t.Errorf("disk_layout_id after clear = %q, want empty", id)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeDiskLayout(name, sourceNodeID string) api.StoredDiskLayout {
	now := time.Now().UTC().Truncate(time.Second)
	return api.StoredDiskLayout{
		ID:           uuid.New().String(),
		Name:         name,
		SourceNodeID: sourceNodeID,
		CapturedAt:   now,
		Layout: api.DiskLayout{
			Partitions: []api.PartitionSpec{
				{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
			},
			Bootloader: api.Bootloader{Type: "grub2", Target: "x86_64-efi"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}
