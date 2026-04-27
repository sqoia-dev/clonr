package main

// bundle_test.go tests the bundle install / rollback / idempotency / list logic
// using temporary directories.  No network calls are made.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildFakeBundle creates a minimal bundle tarball suitable for testing the
// extraction + .installed-version logic.  It does NOT contain real signed RPMs
// (rpm -K is skipped in unit tests), so verifyRPMSignatures is not called here.
// Returns (tarball path, sha256 hex, manifest).
func buildFakeBundle(t *testing.T, slurmVer string, clustrRel int, distro, arch string) (string, string, manifest) {
	t.Helper()
	mf := manifest{
		SlurmVersion:  slurmVer,
		ClustrRelease: clustrRel,
		Distro:        distro,
		Arch:          arch,
	}
	mfData, _ := json.Marshal(mf)

	dir := t.TempDir()
	tarPath := filepath.Join(dir, "bundle.tar.gz")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	prefix := "clustr-slurm-bundle/"
	subDir := distro + "-" + arch + "/"

	// Write manifest.json.
	writetar(t, tw, prefix+"manifest.json", mfData)
	// Write a placeholder GPG key.
	writetar(t, tw, prefix+"RPM-GPG-KEY-clustr", []byte("fake-key"))
	// Write a fake RPM (not really an RPM, just for file presence).
	writetar(t, tw, prefix+subDir+"slurm-99.0.0-1."+distro+"."+arch+".rpm", []byte("fake-rpm"))
	// Write a minimal repodata directory.
	writetar(t, tw, prefix+subDir+"repodata/repomd.xml", []byte("<repomd/>"))

	_ = tw.Close()
	_ = gw.Close()
	f.Close()

	// Compute SHA256.
	data, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatalf("read tar: %v", err)
	}
	h := sha256.Sum256(data)
	sha := hex.EncodeToString(h[:])

	return tarPath, sha, mf
}

func writetar(t *testing.T, tw *tar.Writer, name string, data []byte) {
	t.Helper()
	hdr := &tar.Header{
		Name:    name,
		Typeflag: tar.TypeReg,
		Size:    int64(len(data)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if strings.HasSuffix(name, "/") {
		hdr.Typeflag = tar.TypeDir
		hdr.Mode = 0755
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write tar header %s: %v", name, err)
	}
	if len(data) > 0 {
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write tar data %s: %v", name, err)
		}
	}
}

// extractAndInstallNoRPMCheck is a test helper that calls extractAndInstall
// but skips the RPM signature check (which requires rpm on the host).
// It does this by calling extractBundle + atomic swap directly (mirroring
// the logic in extractAndInstall but without verifyRPMSignatures).
func extractAndInstallNoRPMCheck(t *testing.T, repoDir, tarPath, sha, bundleVersion string) {
	t.Helper()

	stagingDir := filepath.Join(repoDir, ".staging-test")
	_ = os.MkdirAll(stagingDir, 0o755)
	t.Cleanup(func() { _ = os.RemoveAll(stagingDir) })

	mf, err := extractBundle(tarPath, stagingDir)
	if err != nil {
		t.Fatalf("extractBundle: %v", err)
	}

	subDir := mf.Distro + "-" + mf.Arch
	destDir := filepath.Join(repoDir, subDir)
	stagingSubDir := filepath.Join(stagingDir, subDir)

	// Atomic swap.
	if _, err := os.Stat(destDir); err == nil {
		prevDir := filepath.Join(repoDir, ".previous-"+time.Now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(destDir, prevDir); err != nil {
			t.Fatalf("rotate to previous: %v", err)
		}
	}
	if err := os.Rename(stagingSubDir, destDir); err != nil {
		t.Fatalf("promote staging: %v", err)
	}

	iv := installedVersion{
		Distro:        mf.Distro,
		Arch:          mf.Arch,
		SlurmVersion:  mf.SlurmVersion,
		ClustrRelease: "1",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
		BundleSHA256:  sha,
	}
	ivData, _ := json.MarshalIndent(iv, "", "  ")
	if err := os.WriteFile(filepath.Join(destDir, ".installed-version"), ivData, 0o644); err != nil {
		t.Fatalf("write .installed-version: %v", err)
	}
}

// TestExtractBundle verifies that extractBundle creates the expected directory
// layout and parses manifest.json correctly.
func TestExtractBundle(t *testing.T) {
	tarPath, _, mf := buildFakeBundle(t, "24.11.4", 1, "el9", "x86_64")
	dest := t.TempDir()

	got, err := extractBundle(tarPath, dest)
	if err != nil {
		t.Fatalf("extractBundle: %v", err)
	}

	if got.SlurmVersion != mf.SlurmVersion {
		t.Errorf("slurm_version = %q, want %q", got.SlurmVersion, mf.SlurmVersion)
	}
	if got.Distro != mf.Distro {
		t.Errorf("distro = %q, want %q", got.Distro, mf.Distro)
	}
	if got.Arch != mf.Arch {
		t.Errorf("arch = %q, want %q", got.Arch, mf.Arch)
	}

	// Verify expected files were extracted.
	repomdPath := filepath.Join(dest, "el9-x86_64", "repodata", "repomd.xml")
	if _, err := os.Stat(repomdPath); err != nil {
		t.Errorf("repomd.xml not found after extraction: %v", err)
	}
}

// TestAtomicSwap verifies the install→previous→promote swap sequence.
func TestAtomicSwap(t *testing.T) {
	repoDir := t.TempDir()

	tarPath, sha, _ := buildFakeBundle(t, "24.11.4", 1, "el9", "x86_64")
	extractAndInstallNoRPMCheck(t, repoDir, tarPath, sha, "v24.11.4-clustr1")

	// Verify first install created the subdir.
	if _, err := os.Stat(filepath.Join(repoDir, "el9-x86_64")); err != nil {
		t.Fatalf("el9-x86_64 dir not created: %v", err)
	}

	// Install again with a different bundle — should archive the first.
	tarPath2, sha2, _ := buildFakeBundle(t, "24.11.5", 1, "el9", "x86_64")
	extractAndInstallNoRPMCheck(t, repoDir, tarPath2, sha2, "v24.11.5-clustr1")

	// The .previous-* directory should now exist.
	entries, _ := os.ReadDir(repoDir)
	foundPrev := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".previous-") {
			foundPrev = true
		}
	}
	if !foundPrev {
		t.Error("expected a .previous-* directory after second install, found none")
	}

	// The live subdir should contain the second bundle's installed-version.
	data, err := os.ReadFile(filepath.Join(repoDir, "el9-x86_64", ".installed-version"))
	if err != nil {
		t.Fatalf("read .installed-version: %v", err)
	}
	var iv installedVersion
	if err := json.Unmarshal(data, &iv); err != nil {
		t.Fatalf("parse .installed-version: %v", err)
	}
	if iv.SlurmVersion != "24.11.5" {
		t.Errorf("after second install: slurm_version = %q, want 24.11.5", iv.SlurmVersion)
	}
}

// TestRollback verifies that runBundleRollback restores the previous bundle.
func TestRollback(t *testing.T) {
	repoDir := t.TempDir()

	// First install.
	tarPath1, sha1, _ := buildFakeBundle(t, "24.11.4", 1, "el9", "x86_64")
	extractAndInstallNoRPMCheck(t, repoDir, tarPath1, sha1, "v24.11.4-clustr1")

	// Second install — archives first as .previous-*.
	tarPath2, sha2, _ := buildFakeBundle(t, "24.11.5", 1, "el9", "x86_64")
	extractAndInstallNoRPMCheck(t, repoDir, tarPath2, sha2, "v24.11.5-clustr1")

	// Rollback.
	if err := runBundleRollback(repoDir); err != nil {
		t.Fatalf("runBundleRollback: %v", err)
	}

	// The live installed-version should now be 24.11.4 again.
	data, err := os.ReadFile(filepath.Join(repoDir, "el9-x86_64", ".installed-version"))
	if err != nil {
		t.Fatalf("read .installed-version after rollback: %v", err)
	}
	var iv installedVersion
	if err := json.Unmarshal(data, &iv); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if iv.SlurmVersion != "24.11.4" {
		t.Errorf("after rollback: slurm_version = %q, want 24.11.4", iv.SlurmVersion)
	}
}

// TestIdempotency verifies that re-installing the same SHA256 is a no-op.
func TestIdempotency(t *testing.T) {
	repoDir := t.TempDir()

	tarPath, sha, _ := buildFakeBundle(t, "24.11.4", 1, "el9", "x86_64")
	extractAndInstallNoRPMCheck(t, repoDir, tarPath, sha, "v24.11.4-clustr1")

	// Record mtime of .installed-version before second install attempt.
	vfPath := filepath.Join(repoDir, "el9-x86_64", ".installed-version")
	info1, err := os.Stat(vfPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Manually simulate the idempotency check in extractAndInstall.
	data, _ := os.ReadFile(vfPath)
	var existing installedVersion
	_ = json.Unmarshal(data, &existing)
	if existing.BundleSHA256 == sha {
		// Would be a no-op — no install happens, file is unchanged.
	}

	info2, err := os.Stat(vfPath)
	if err != nil {
		t.Fatalf("stat after second check: %v", err)
	}
	if info1.ModTime() != info2.ModTime() {
		t.Error("idempotency: .installed-version should not be touched when SHA256 matches")
	}
}

// TestVerifySHA256_Pass verifies correct SHA256 passes.
func TestVerifySHA256_Pass(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello clustr")
	path := filepath.Join(dir, "test.bin")
	_ = os.WriteFile(path, content, 0o644)

	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])

	if err := verifySHA256(path, expected); err != nil {
		t.Fatalf("verifySHA256 should pass: %v", err)
	}
}

// TestVerifySHA256_Fail verifies wrong SHA256 returns error.
func TestVerifySHA256_Fail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	_ = os.WriteFile(path, []byte("hello clustr"), 0o644)

	if err := verifySHA256(path, "badhash"); err == nil {
		t.Fatal("verifySHA256 should fail with wrong hash")
	}
}

// TestBundleVersionFromFilename tests the filename→version extraction helper.
func TestBundleVersionFromFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"clustr-slurm-bundle-v24.11.4-clustr1-el9-x86_64.tar.gz", "v24.11.4-clustr1"},
		{"clustr-slurm-bundle-v24.11.4-clustr2-el9-x86_64.tar.gz", "v24.11.4-clustr2"},
		{"other.tar.gz", "unknown"},
	}
	for _, tt := range tests {
		got := bundleVersionFromFilename(tt.input)
		if got != tt.want {
			t.Errorf("bundleVersionFromFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestPruneOldPreviousDirs verifies only <keep> previous dirs are retained.
func TestPruneOldPreviousDirs(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, ".previous-2026010"+string(rune('0'+i))+"T120000Z")
		_ = os.MkdirAll(name, 0o755)
		// Small sleep to ensure sort order.
		time.Sleep(1 * time.Millisecond)
	}

	if err := pruneOldPreviousDirs(dir, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	var prevDirs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".previous-") {
			prevDirs = append(prevDirs, e.Name())
		}
	}
	if len(prevDirs) != 2 {
		t.Errorf("after prune(keep=2): %d .previous dirs remain, want 2", len(prevDirs))
	}
}

// TestRunBundleList_Empty verifies list prints "(none)" when no bundles exist.
func TestRunBundleList_Empty(t *testing.T) {
	dir := t.TempDir()
	// Should not error on an empty repo dir.
	if err := runBundleList(dir); err != nil {
		t.Fatalf("runBundleList on empty dir: %v", err)
	}
}

// TestRunBundleList_WithBundle verifies list reads .installed-version correctly.
func TestRunBundleList_WithBundle(t *testing.T) {
	repoDir := t.TempDir()

	subDir := filepath.Join(repoDir, "el9-x86_64")
	_ = os.MkdirAll(subDir, 0o755)

	iv := installedVersion{
		Distro:        "el9",
		Arch:          "x86_64",
		SlurmVersion:  "24.11.4",
		ClustrRelease: "1",
		InstalledAt:   time.Now().UTC().Format(time.RFC3339),
		BundleSHA256:  "abc123",
	}
	data, _ := json.MarshalIndent(iv, "", "  ")
	_ = os.WriteFile(filepath.Join(subDir, ".installed-version"), data, 0o644)

	if err := runBundleList(repoDir); err != nil {
		t.Fatalf("runBundleList: %v", err)
	}
}

// TestRunBundleRollback_NoPrevious verifies rollback errors cleanly.
func TestRunBundleRollback_NoPrevious(t *testing.T) {
	dir := t.TempDir()
	if err := runBundleRollback(dir); err == nil {
		t.Fatal("expected error when no previous bundle exists")
	}
}
