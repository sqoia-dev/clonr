package handlers

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// makeInitramfsGzCpio creates a minimal gzipped newc cpio archive at dest
// containing a lib/modules/<kernelVer>/ directory entry.
// This is a self-contained helper that does not require cpio at generation
// time — only zcat+cpio are needed at test-runtime for the code under test.
func makeInitramfsGzCpio(t *testing.T, dest, kernelVer string) {
	t.Helper()

	f, err := os.Create(dest)
	if err != nil {
		t.Fatalf("makeInitramfsGzCpio: create: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)

	writeDirEntry := func(name string) {
		namePlusNull := name + "\x00"
		nameLen := len(namePlusNull)
		header := []byte("070701" +
			"00000001" + "000041ED" + "00000000" + "00000000" +
			"00000002" + "00000000" + "00000000" + "00000008" +
			"00000001" + "00000000" + "00000000" +
			fmt.Sprintf("%08X", nameLen) + "00000000")
		_, _ = gw.Write(header)
		_, _ = gw.Write([]byte(namePlusNull))
		total := len(header) + nameLen
		if pad := (4 - total%4) % 4; pad > 0 {
			_, _ = gw.Write(make([]byte, pad))
		}
	}

	writeTrailer := func() {
		const trailerName = "TRAILER!!!\x00"
		nameLen := len(trailerName)
		header := []byte("070701" +
			"00000000" + "00000000" + "00000000" + "00000000" +
			"00000001" + "00000000" + "00000000" + "00000000" +
			"00000000" + "00000000" + "00000000" +
			fmt.Sprintf("%08X", nameLen) + "00000000")
		_, _ = gw.Write(header)
		_, _ = gw.Write([]byte(trailerName))
		total := len(header) + nameLen
		if pad := (4 - total%4) % 4; pad > 0 {
			_, _ = gw.Write(make([]byte, pad))
		}
	}

	writeDirEntry("lib/modules/" + kernelVer)
	writeTrailer()

	if err := gw.Close(); err != nil {
		t.Fatalf("makeInitramfsGzCpio: gzip close: %v", err)
	}
}

// newInitramfsHandler returns a handler wired to a fresh test DB with the
// initramfs file at path.
func newInitramfsHandler(d *db.DB, imgPath string) *InitramfsHandler {
	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: imgPath,
	}
	h.InitLiveSHA256()
	return h
}

// TestGetInitramfs_KernelVersionAlreadyInDB verifies that when the DB record
// already has a kernel_version, the handler returns it without re-extracting
// from disk (the short-circuit path).
func TestGetInitramfs_KernelVersionAlreadyInDB(t *testing.T) {
	const wantVer = "5.14.0-611.5.1.el9_7.x86_64"

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")
	makeInitramfsGzCpio(t, imgPath, wantVer)

	d := openTestDB(t)
	h := newInitramfsHandler(d, imgPath)

	// Seed a successful build record with the live sha256 and a known kernel version.
	ctx := context.Background()
	record := db.InitramfsBuildRecord{
		ID:        "build-001",
		StartedAt: time.Now().UTC(),
		SHA256:    h.liveSHA256,
		Outcome:   "success",
	}
	if err := d.CreateInitramfsBuild(ctx, record); err != nil {
		t.Fatalf("CreateInitramfsBuild: %v", err)
	}
	if err := d.FinishInitramfsBuild(ctx, record.ID, h.liveSHA256, 0, wantVer, "success"); err != nil {
		t.Fatalf("FinishInitramfsBuild: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs: status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != wantVer {
		t.Errorf("GetInitramfs: KernelVersion = %q, want %q", info.KernelVersion, wantVer)
	}
}

// TestGetInitramfs_LazyExtract verifies that when the DB record exists but
// kernel_version is empty (autodeploy timer path), the handler extracts the
// version from disk and back-fills the DB.
func TestGetInitramfs_LazyExtract(t *testing.T) {
	const wantVer = "5.14.0-611.5.1.el9_7.x86_64"

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "initramfs.img")
	makeInitramfsGzCpio(t, imgPath, wantVer)

	d := openTestDB(t)
	h := newInitramfsHandler(d, imgPath)

	// Seed a successful build record with empty kernel_version (simulates autodeploy).
	ctx := context.Background()
	record := db.InitramfsBuildRecord{
		ID:        "build-002",
		StartedAt: time.Now().UTC(),
		SHA256:    h.liveSHA256,
		Outcome:   "success",
	}
	if err := d.CreateInitramfsBuild(ctx, record); err != nil {
		t.Fatalf("CreateInitramfsBuild: %v", err)
	}
	if err := d.FinishInitramfsBuild(ctx, record.ID, h.liveSHA256, 0, "", "success"); err != nil {
		t.Fatalf("FinishInitramfsBuild: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs: status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != wantVer {
		t.Errorf("GetInitramfs: KernelVersion = %q, want %q (lazy extract failed)", info.KernelVersion, wantVer)
	}

	// Verify the DB was back-filled.
	_, backFilled, err := d.GetLatestSuccessfulBuildBySHA256(ctx, h.liveSHA256)
	if err != nil {
		t.Fatalf("GetLatestSuccessfulBuildBySHA256 after back-fill: %v", err)
	}
	if backFilled != wantVer {
		t.Errorf("DB back-fill: kernel_version = %q, want %q", backFilled, wantVer)
	}
}

// TestGetInitramfs_NoDB_NoFile verifies that the handler returns 200 with an
// empty KernelVersion when neither the DB record nor the file yields a version
// (e.g. corrupt/missing initramfs). It must not crash or return 5xx.
func TestGetInitramfs_NoDB_NoFile(t *testing.T) {
	d := openTestDB(t)
	h := &InitramfsHandler{
		DB:            d,
		InitramfsPath: "/nonexistent/initramfs.img",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/initramfs", nil)
	w := httptest.NewRecorder()
	h.GetInitramfs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GetInitramfs (no file): status %d, want 200", w.Code)
	}

	var info InitramfsBuildInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("GetInitramfs: unmarshal: %v", err)
	}
	if info.KernelVersion != "" {
		t.Errorf("GetInitramfs: KernelVersion = %q, want empty", info.KernelVersion)
	}
}
