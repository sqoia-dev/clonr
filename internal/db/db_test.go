package db_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func makeImage(id string) api.BaseImage {
	return api.BaseImage{
		ID:      id,
		Name:    "rocky9-base",
		Version: "1.0.0",
		OS:      "Rocky Linux 9.3",
		Arch:    "x86_64",
		Status:  api.ImageStatusBuilding,
		Format:  api.ImageFormatFilesystem,
		DiskLayout: api.DiskLayout{
			Partitions: []api.PartitionSpec{
				{Label: "boot", SizeBytes: 512 * 1024 * 1024, Filesystem: "vfat", MountPoint: "/boot/efi"},
				{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
			},
			Bootloader: api.Bootloader{Type: "grub2", Target: "x86_64-efi"},
		},
		Tags:      []string{"hpc", "rocky"},
		SourceURL: "https://example.com/rocky9.tar.gz",
		Notes:     "test image",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestBaseImage_CreateAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())

	if err := d.CreateBaseImage(ctx, img); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := d.GetBaseImage(ctx, img.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID != img.ID {
		t.Errorf("id: got %s want %s", got.ID, img.ID)
	}
	if got.Name != img.Name {
		t.Errorf("name: got %s want %s", got.Name, img.Name)
	}
	if got.Status != api.ImageStatusBuilding {
		t.Errorf("status: got %s want building", got.Status)
	}
	if len(got.DiskLayout.Partitions) != 2 {
		t.Errorf("disk layout partitions: got %d want 2", len(got.DiskLayout.Partitions))
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags: got %d want 2", len(got.Tags))
	}
}

func TestBaseImage_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetBaseImage(context.Background(), "does-not-exist")
	if err != api.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestBaseImage_List(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		img := makeImage(uuid.New().String())
		if err := d.CreateBaseImage(ctx, img); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	images, err := d.ListBaseImages(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(images) != 3 {
		t.Errorf("count: got %d want 3", len(images))
	}

	filtered, err := d.ListBaseImages(ctx, string(api.ImageStatusBuilding))
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered) != 3 {
		t.Errorf("filtered count: got %d want 3", len(filtered))
	}
}

func TestBaseImage_UpdateStatus(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	if err := d.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusError, "download failed"); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, _ := d.GetBaseImage(ctx, img.ID)
	if got.Status != api.ImageStatusError {
		t.Errorf("status: got %s want error", got.Status)
	}
	if got.ErrorMessage != "download failed" {
		t.Errorf("error_message: got %q", got.ErrorMessage)
	}
}

func TestBaseImage_Finalize(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	if err := d.FinalizeBaseImage(ctx, img.ID, 1024*1024*500, "abc123def456"); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	got, _ := d.GetBaseImage(ctx, img.ID)
	if got.Status != api.ImageStatusReady {
		t.Errorf("status: got %s want ready", got.Status)
	}
	if got.SizeBytes != 1024*1024*500 {
		t.Errorf("size_bytes: got %d", got.SizeBytes)
	}
	if got.Checksum != "abc123def456" {
		t.Errorf("checksum: got %s", got.Checksum)
	}
	if got.FinalizedAt == nil {
		t.Error("finalized_at should be set")
	}
}

func TestBaseImage_Archive(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)
	_ = d.FinalizeBaseImage(ctx, img.ID, 100, "cksum")

	if err := d.ArchiveBaseImage(ctx, img.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, _ := d.GetBaseImage(ctx, img.ID)
	if got.Status != api.ImageStatusArchived {
		t.Errorf("status: got %s want archived", got.Status)
	}
}

func TestBaseImage_BlobPath(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	// Initially empty.
	path, err := d.GetBlobPath(ctx, img.ID)
	if err != nil {
		t.Fatalf("get blob path: %v", err)
	}
	if path != "" {
		t.Errorf("blob path should be empty initially, got %q", path)
	}

	if err := d.SetBlobPath(ctx, img.ID, "/var/lib/clonr/images/"+img.ID+".blob"); err != nil {
		t.Fatalf("set blob path: %v", err)
	}

	path, _ = d.GetBlobPath(ctx, img.ID)
	if path != "/var/lib/clonr/images/"+img.ID+".blob" {
		t.Errorf("blob path: got %q", path)
	}
}

// ─── NodeConfig tests ────────────────────────────────────────────────────────

func makeNode(id, baseImageID string) api.NodeConfig {
	now := time.Now().UTC().Truncate(time.Second)
	return api.NodeConfig{
		ID:          id,
		Hostname:    "compute-01",
		FQDN:        "compute-01.hpc.example.com",
		PrimaryMAC:  "aa:bb:cc:dd:ee:01",
		Interfaces:  []api.InterfaceConfig{{MACAddress: "aa:bb:cc:dd:ee:01", Name: "ens3", IPAddress: "10.0.0.1/24"}},
		SSHKeys:     []string{"ssh-ed25519 AAAA..."},
		KernelArgs:  "console=ttyS0",
		Groups:      []string{"compute", "gpu"},
		CustomVars:  map[string]string{"slurm_partition": "gpu"},
		BaseImageID: baseImageID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestNodeConfig_CreateAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	node := makeNode(uuid.New().String(), img.ID)
	if err := d.CreateNodeConfig(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}

	got, err := d.GetNodeConfig(ctx, node.ID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Hostname != "compute-01" {
		t.Errorf("hostname: got %s", got.Hostname)
	}
	if len(got.Interfaces) != 1 {
		t.Errorf("interfaces: got %d want 1", len(got.Interfaces))
	}
	if len(got.Groups) != 2 {
		t.Errorf("groups: got %d want 2", len(got.Groups))
	}
	if got.CustomVars["slurm_partition"] != "gpu" {
		t.Errorf("custom_vars: got %v", got.CustomVars)
	}
}

func TestNodeConfig_GetByMAC(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	got, err := d.GetNodeConfigByMAC(ctx, "aa:bb:cc:dd:ee:01")
	if err != nil {
		t.Fatalf("get by mac: %v", err)
	}
	if got.ID != node.ID {
		t.Errorf("id: got %s want %s", got.ID, node.ID)
	}
}

func TestNodeConfig_GetByMAC_NotFound(t *testing.T) {
	d := openTestDB(t)
	_, err := d.GetNodeConfigByMAC(context.Background(), "ff:ff:ff:ff:ff:ff")
	if err != api.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestNodeConfig_Update(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	node.Hostname = "compute-01-updated"
	node.KernelArgs = "console=ttyS0 nomodeset"
	if err := d.UpdateNodeConfig(ctx, node); err != nil {
		t.Fatalf("update node: %v", err)
	}

	got, _ := d.GetNodeConfig(ctx, node.ID)
	if got.Hostname != "compute-01-updated" {
		t.Errorf("hostname: got %s", got.Hostname)
	}
}

func TestNodeConfig_Delete(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	node := makeNode(uuid.New().String(), img.ID)
	_ = d.CreateNodeConfig(ctx, node)

	if err := d.DeleteNodeConfig(ctx, node.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := d.GetNodeConfig(ctx, node.ID)
	if err != api.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestNodeConfig_List(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	img := makeImage(uuid.New().String())
	_ = d.CreateBaseImage(ctx, img)

	for i, mac := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"} {
		node := makeNode(uuid.New().String(), img.ID)
		node.PrimaryMAC = mac
		node.Hostname = fmt.Sprintf("compute-%02d", i+1)
		_ = d.CreateNodeConfig(ctx, node)
	}

	all, err := d.ListNodeConfigs(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("count: got %d want 3", len(all))
	}

	byImage, err := d.ListNodeConfigs(ctx, img.ID)
	if err != nil {
		t.Fatalf("list by image: %v", err)
	}
	if len(byImage) != 3 {
		t.Errorf("by image count: got %d want 3", len(byImage))
	}
}

func TestMigrations_Idempotent(t *testing.T) {
	// Opening twice should not error — migrations are idempotent.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idem.db")

	d1, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	d1.Close()

	d2, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	d2.Close()
}

// Suppress unused import warnings.
var _ = os.DevNull
var _ = fmt.Sprintf
