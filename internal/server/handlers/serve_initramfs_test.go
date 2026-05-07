package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeInitramfs_PrefersLiveBuild documents the load-bearing behaviour
// added in v0.1.13: the HTTP boot endpoint must serve the rebuildable live
// initramfs (initramfs-clustr.img) when both files are present, not the
// legacy bootstrap seed (initramfs.img). The v0.1.13 root-cause investigation
// found that nodes were PXE-booting from a frozen May-3 initramfs.img while
// the build pipeline had been silently producing v0.1.12 initramfs-clustr.img
// rebuilds nobody read. Reversing this preference re-introduces that class
// of "fix isn't running on the node" puzzle.
func TestServeInitramfs_PrefersLiveBuild(t *testing.T) {
	dir := t.TempDir()
	liveContent := []byte("live-initramfs-clustr-content")
	legacyContent := []byte("legacy-initramfs-content")

	if err := os.WriteFile(filepath.Join(dir, "initramfs-clustr.img"), liveContent, 0o644); err != nil {
		t.Fatalf("write live initramfs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "initramfs.img"), legacyContent, 0o644); err != nil {
		t.Fatalf("write legacy initramfs: %v", err)
	}

	h := &BootHandler{BootDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/initramfs.img", nil)
	rec := httptest.NewRecorder()
	h.ServeInitramfs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeInitramfs status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != string(liveContent) {
		t.Errorf("ServeInitramfs body = %q, want %q (must prefer initramfs-clustr.img over initramfs.img)", string(body), string(liveContent))
	}
}

// TestServeInitramfs_FallsBackToLegacy verifies that the legacy bootstrap
// seed (initramfs.img) is still served when the live rebuildable file is
// absent. This preserves the brand-new install (pre-first-build) flow where
// only the seed file exists in BootDir.
func TestServeInitramfs_FallsBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	legacyContent := []byte("legacy-bootstrap-seed")
	if err := os.WriteFile(filepath.Join(dir, "initramfs.img"), legacyContent, 0o644); err != nil {
		t.Fatalf("write legacy initramfs: %v", err)
	}

	h := &BootHandler{BootDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/boot/initramfs.img", nil)
	rec := httptest.NewRecorder()
	h.ServeInitramfs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeInitramfs status = %d, want 200", rec.Code)
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(body) != string(legacyContent) {
		t.Errorf("ServeInitramfs body = %q, want legacy %q (fallback when live build is absent)", string(body), string(legacyContent))
	}
}
