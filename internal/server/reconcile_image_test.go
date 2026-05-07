package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/pkg/reconcile"
)

// TestDefaultBlobPath_FormatAware verifies the F6 default-layout fallback
// returns a format-appropriate filename. Regression test for BLOB-RESOLVE:
// initramfs / block-format images write to <imageDir>/<id>/image.img but the
// resolver historically hardcoded rootfs.tar, falsely flipping every block
// row to blob_missing on each reconcile tick.
func TestDefaultBlobPath_FormatAware(t *testing.T) {
	tests := []struct {
		name     string
		format   api.ImageFormat
		wantBase string
	}{
		{"filesystem returns rootfs.tar", api.ImageFormatFilesystem, "rootfs.tar"},
		{"block returns image.img", api.ImageFormatBlock, "image.img"},
		{"empty format falls back to rootfs.tar", api.ImageFormat(""), "rootfs.tar"},
		{"unknown format falls back to rootfs.tar", api.ImageFormat("future-format-xyz"), "rootfs.tar"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultBlobPath("/var/lib/clustr/images", "img-001", tc.format)
			want := filepath.Join("/var/lib/clustr/images", "img-001", tc.wantBase)
			if got != want {
				t.Fatalf("defaultBlobPath(%q) = %q, want %q", tc.format, got, want)
			}
		})
	}
}

// TestResolveBlobPath_BlockFormatEmptyDBPath verifies the end-to-end resolver
// against a real Server + DB: a block-format image with an empty blob_path
// column AND an on-disk image.img must resolve via the default layout (F6),
// not be reported as not-found. This is the core BLOB-RESOLVE regression.
func TestResolveBlobPath_BlockFormatEmptyDBPath(t *testing.T) {
	srv, database, dir := newReconcileTestServer(t)

	imgID := "img-block-resolve-empty-dbpath"
	img := api.BaseImage{
		ID:        imgID,
		Name:      "block-resolve-test",
		Status:    api.ImageStatusReady,
		Format:    api.ImageFormatBlock,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(context.Background(), img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	// Mimic the initramfs writer: drop image.img into <imageDir>/<id>/ but
	// never call SetBlobPath. blob_path stays empty in the DB.
	imgDir := filepath.Join(dir, "images", imgID)
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	imgFile := filepath.Join(imgDir, "image.img")
	if err := os.WriteFile(imgFile, []byte("fake-block-image-bytes"), 0o644); err != nil {
		t.Fatalf("write image.img: %v", err)
	}

	gotPath, gotResolution := srv.resolveBlobPath(img)
	if gotResolution != reconcile.BlobPathFoundAtDefaultLayout {
		t.Fatalf("resolution = %q, want %q (the BLOB-RESOLVE fix should locate image.img via the format-aware default)", gotResolution, reconcile.BlobPathFoundAtDefaultLayout)
	}
	if gotPath != imgFile {
		t.Fatalf("path = %q, want %q", gotPath, imgFile)
	}
}

// TestResolveBlobPath_FilesystemFormatEmptyDBPath is the symmetric case for
// filesystem images: empty blob_path, rootfs.tar present, must resolve.
// Pre-existing behaviour we must not regress.
func TestResolveBlobPath_FilesystemFormatEmptyDBPath(t *testing.T) {
	srv, database, dir := newReconcileTestServer(t)

	imgID := "img-fs-resolve-empty-dbpath"
	img := api.BaseImage{
		ID:        imgID,
		Name:      "fs-resolve-test",
		Status:    api.ImageStatusReady,
		Format:    api.ImageFormatFilesystem,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(context.Background(), img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	imgDir := filepath.Join(dir, "images", imgID)
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	tarFile := filepath.Join(imgDir, "rootfs.tar")
	if err := os.WriteFile(tarFile, []byte("fake-tar-bytes"), 0o644); err != nil {
		t.Fatalf("write rootfs.tar: %v", err)
	}

	gotPath, gotResolution := srv.resolveBlobPath(img)
	if gotResolution != reconcile.BlobPathFoundAtDefaultLayout {
		t.Fatalf("resolution = %q, want %q", gotResolution, reconcile.BlobPathFoundAtDefaultLayout)
	}
	if gotPath != tarFile {
		t.Fatalf("path = %q, want %q", gotPath, tarFile)
	}
}

// TestResolveBlobPath_BlockFormatTrulyMissing verifies the resolver still
// reports BlobPathNotFound for a block-format image whose blob is genuinely
// gone — i.e. the format-aware default doesn't falsely heal dead rows.
func TestResolveBlobPath_BlockFormatTrulyMissing(t *testing.T) {
	srv, database, _ := newReconcileTestServer(t)

	imgID := "img-block-truly-missing"
	img := api.BaseImage{
		ID:        imgID,
		Name:      "block-missing-test",
		Status:    api.ImageStatusReady,
		Format:    api.ImageFormatBlock,
		Tags:      []string{},
		CreatedAt: time.Now().UTC(),
	}
	if err := database.CreateBaseImage(context.Background(), img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	// No on-disk file written.

	_, gotResolution := srv.resolveBlobPath(img)
	if gotResolution != reconcile.BlobPathNotFound {
		t.Fatalf("resolution = %q, want %q (truly missing block blob must not be resolved)", gotResolution, reconcile.BlobPathNotFound)
	}
}

// newReconcileTestServer is a minimal in-package test harness for resolver
// tests. Returns the *Server, the underlying *db.DB, and the temp dir root.
func newReconcileTestServer(t *testing.T) (*Server, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := config.ServerConfig{
		ListenAddr:  ":0",
		ImageDir:    filepath.Join(dir, "images"),
		DBPath:      filepath.Join(dir, "test.db"),
		AuthDevMode: true,
		LogLevel:    "error",
		PXE: config.PXEConfig{
			BootDir: filepath.Join(dir, "boot"),
		},
	}
	if err := os.MkdirAll(cfg.ImageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	if err := os.MkdirAll(cfg.PXE.BootDir, 0o755); err != nil {
		t.Fatalf("mkdir boot dir: %v", err)
	}
	srv := New(cfg, database, BuildInfo{})
	return srv, database, dir
}
