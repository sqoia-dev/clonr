package initramfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixtureTree creates a fake /lib/modules/<kver>/kernel/ layout under dir
// and populates it with zero-byte .ko and .ko.xz files named after the provided
// entries. Returns the path to the kernel/ subtree root.
//
// entries is a slice of paths relative to kernel/, e.g.:
//
//	"drivers/net/mlx5/mlx5_core.ko"
//	"net/core/failover.ko.xz"
func buildFixtureTree(t *testing.T, dir string, entries []string) string {
	t.Helper()
	root := filepath.Join(dir, "kernel")
	for _, e := range entries {
		full := filepath.Join(root, e)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("fixture: mkdir %s: %v", filepath.Dir(full), err)
		}
		// Write a small but non-empty fake ELF header so sha256 is deterministic.
		content := fmt.Sprintf("FAKE_KO:%s", e)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("fixture: write %s: %v", full, err)
		}
	}
	return root
}

// TestEnumerateModules_BasicMatch verifies that the enumerator finds modules
// that are present in the allowlist and ignores modules that are not.
func TestEnumerateModules_BasicMatch(t *testing.T) {
	dir := t.TempDir()
	fixtureEntries := []string{
		"drivers/net/mlx5/mlx5_core.ko",        // should be found
		"drivers/net/virtio_net.ko.xz",         // should be found (compressed)
		"net/core/failover.ko",                 // should be found
		"drivers/net/some_unknown_driver.ko",   // NOT in allowlist
		"drivers/scsi/megaraid/megaraid_sas.ko", // should be found
		"drivers/nvme/host/nvme.ko",            // should be found
		"fs/xfs/xfs.ko",                        // should be found
		"drivers/md/dm_mod.ko",                 // should be found
	}

	root := buildFixtureTree(t, dir, fixtureEntries)

	allowlist := []string{
		"mlx5_core", "virtio_net", "failover", "megaraid_sas",
		"nvme", "xfs", "dm_mod",
	}
	entries, err := EnumerateModules(root, allowlist)
	if err != nil {
		t.Fatalf("EnumerateModules: unexpected error: %v", err)
	}

	// Build a set of found module names.
	foundNames := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		foundNames[e.Name] = struct{}{}
	}

	for _, want := range allowlist {
		if _, ok := foundNames[want]; !ok {
			t.Errorf("expected module %q to be found but it was not", want)
		}
	}

	// Verify unknown driver is NOT included.
	for _, e := range entries {
		if strings.Contains(e.RelPath, "some_unknown_driver") {
			t.Errorf("expected 'some_unknown_driver' to be excluded but found it in entries")
		}
	}
}

// TestEnumerateModules_HyphenNormalisation verifies that "crc32c-intel.ko"
// matches an allowlist entry "crc32c_intel" (and vice-versa).
func TestEnumerateModules_HyphenNormalisation(t *testing.T) {
	dir := t.TempDir()
	// File uses hyphens, allowlist uses underscores.
	buildFixtureTree(t, dir, []string{
		"arch/x86/crypto/crc32c-intel.ko",
	})
	root := filepath.Join(dir, "kernel")

	entries, err := EnumerateModules(root, []string{"crc32c_intel"})
	if err != nil {
		t.Fatalf("EnumerateModules: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "crc32c_intel" {
		t.Errorf("expected canonical name 'crc32c_intel', got %q", entries[0].Name)
	}
}

// TestEnumerateModules_ManifestContent verifies that each entry has a
// non-empty SHA256 and a relative path that does not include the fixture root.
func TestEnumerateModules_ManifestContent(t *testing.T) {
	dir := t.TempDir()
	buildFixtureTree(t, dir, []string{
		"drivers/scsi/sd_mod.ko",
	})
	root := filepath.Join(dir, "kernel")

	entries, err := EnumerateModules(root, []string{"sd_mod"})
	if err != nil {
		t.Fatalf("EnumerateModules: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.SHA256 == "" || e.SHA256 == "unavailable" {
		t.Errorf("expected a real SHA256 hash, got %q", e.SHA256)
	}
	// RelPath must not contain the tmpdir.
	if strings.Contains(e.RelPath, dir) {
		t.Errorf("RelPath %q must be relative to module root, not contain tmpdir", e.RelPath)
	}
	// ManifestLine format: "name relpath sha256"
	line := e.ManifestLine()
	parts := strings.Fields(line)
	if len(parts) != 3 {
		t.Errorf("ManifestLine %q: expected 3 fields, got %d", line, len(parts))
	}
}

// TestEnumerateModules_KOXZIncluded verifies that .ko.xz files are found and
// their RelPath retains the .ko.xz extension (the build script decompresses
// them; the Go enumerator just records what's in the tree).
func TestEnumerateModules_KOXZIncluded(t *testing.T) {
	dir := t.TempDir()
	buildFixtureTree(t, dir, []string{
		"drivers/net/virtio_net.ko.xz",
	})
	root := filepath.Join(dir, "kernel")

	entries, err := EnumerateModules(root, []string{"virtio_net"})
	if err != nil {
		t.Fatalf("EnumerateModules: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].RelPath, ".ko.xz") {
		t.Errorf("expected RelPath to retain .ko.xz suffix, got %q", entries[0].RelPath)
	}
}

// TestEnumerateModules_EmptyTree verifies that an empty module tree returns
// zero entries without error.
func TestEnumerateModules_EmptyTree(t *testing.T) {
	dir := t.TempDir()
	root := buildFixtureTree(t, dir, nil) // no files
	entries, err := EnumerateModules(root, ModuleAllowlist)
	if err != nil {
		t.Fatalf("EnumerateModules on empty tree: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries on empty tree, got %d", len(entries))
	}
}

// TestEnumerateModules_AllowlistCoverage checks that EnumerateModules finds
// all expected modules from a fixture tree that contains every allowlisted
// module (simulating a kernel with all modules available).
func TestEnumerateModules_AllowlistCoverage(t *testing.T) {
	// Representative subset of modules that must be in the allowlist.
	required := []string{
		"mlx5_core", "mlx4_core", "i40e", "ice", "ixgbe", "igb", "e1000e",
		"bnxt_en", "bnx2x", "tg3",
		"nvme", "nvme_core",
		"megaraid_sas", "mpt3sas", "aacraid",
		"dm_mod", "dm_mirror", "dm_snapshot", "dm_thin_pool",
		"xfs", "btrfs", "ext4",
		"virtio_net", "failover", "net_failover",
		"virtio_scsi", "virtio_blk",
	}

	// Verify all are present in the canonical allowlist.
	allowedSet := make(map[string]struct{}, len(ModuleAllowlist))
	for _, name := range ModuleAllowlist {
		allowedSet[normModuleName(name)] = struct{}{}
	}
	for _, req := range required {
		if _, ok := allowedSet[normModuleName(req)]; !ok {
			t.Errorf("required module %q is missing from ModuleAllowlist", req)
		}
	}

	// Build a fixture tree with all required modules and verify enumeration.
	dir := t.TempDir()
	var fixtureFiles []string
	for _, name := range required {
		fixtureFiles = append(fixtureFiles, fmt.Sprintf("drivers/%s.ko", name))
	}
	root := buildFixtureTree(t, dir, fixtureFiles)

	entries, err := EnumerateModules(root, required)
	if err != nil {
		t.Fatalf("EnumerateModules: %v", err)
	}

	foundNames := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		foundNames[normModuleName(e.Name)] = struct{}{}
	}
	for _, req := range required {
		if _, ok := foundNames[normModuleName(req)]; !ok {
			t.Errorf("module %q expected in enumeration result but not found", req)
		}
	}
}

// TestWriteManifest verifies that WriteManifest writes one line per entry in
// the expected format and the output is correctly formed.
func TestWriteManifest(t *testing.T) {
	entries := []ModuleEntry{
		{Name: "mlx5_core", RelPath: "drivers/net/mlx5/mlx5_core.ko", SHA256: "abc123"},
		{Name: "xfs", RelPath: "fs/xfs/xfs.ko", SHA256: "def456"},
	}

	var sb strings.Builder
	if err := WriteManifest(&sb, entries); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), sb.String())
	}
	for i, e := range entries {
		want := e.ManifestLine()
		if lines[i] != want {
			t.Errorf("line %d: got %q, want %q", i, lines[i], want)
		}
	}
}
