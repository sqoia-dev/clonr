package main

// backup_test.go — Sprint 41 Day 4
//
// Tests for the backup-write and backup-restore privhelper verbs.
// Focuses on argv validation and tarball round-trip; does not require root.

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// ─── isSafeBackupPath ─────────────────────────────────────────────────────────

func TestIsSafeBackupPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Allowed prefixes.
		{"/etc/sssd/sssd.conf", true},
		{"/etc/hosts", true},
		{"/var/lib/sss/db/", true},
		{"/var/lib/sssd/", true},

		// Path traversal — rejected.
		{"/etc/../etc/passwd", false},
		{"/etc/sssd/../../etc/shadow", false},

		// Not absolute — rejected.
		{"etc/sssd/sssd.conf", false},
		{"relative/path", false},

		// Outside allowlist — rejected.
		{"/tmp/evil", false},
		{"/root/.ssh/authorized_keys", false},
		{"/var/lib/mysql/data.db", false},

		// Null bytes — rejected.
		{"/etc/\x00sssd", false},

		// Empty — rejected.
		{"", false},
	}

	for _, tt := range tests {
		got := isSafeBackupPath(tt.path)
		if got != tt.want {
			t.Errorf("isSafeBackupPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// ─── isSafePluginName ────────────────────────────────────────────────────────

func TestIsSafePluginName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"sssd", true},
		{"hostname", true},
		{"sssd-conf", true},
		{"sssd_conf", true},
		{"", false},
		{"with/slash", false},
		{"with space", false},
		{"with.dot", false},
	}
	for _, tt := range tests {
		got := isSafePluginName(tt.name)
		if got != tt.want {
			t.Errorf("isSafePluginName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// ─── verbBackupWrite validation (exit code checks) ───────────────────────────

func TestVerbBackupWriteValidation(t *testing.T) {
	// We call verbBackupWrite directly; writeAudit will fail gracefully
	// because the DB is not available in the test environment (that's expected
	// and is the established pattern across all privhelper tests).

	tmpDir := t.TempDir()

	// A valid output path must be under backupBaseDir. We override for testing
	// by using a temp subpath that matches the prefix pattern.
	// Since backupBaseDir is "/var/lib/clustr/backups/" and we can't write there
	// in tests, we verify that validation rejects bad inputs before reaching
	// the filesystem write.

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "missing required flags",
			args:     []string{"--plugin", "sssd"},
			wantCode: 1,
		},
		{
			name:     "unsafe plugin name with slash",
			args:     []string{"--plugin", "bad/name", "--node-id", "aaaa-bbbb", "--paths", "/etc/sssd/sssd.conf", "--out", tmpDir + "/snap.tar.gz"},
			wantCode: 1,
		},
		{
			name:     "invalid node-id",
			args:     []string{"--plugin", "sssd", "--node-id", "not;valid!", "--paths", "/etc/sssd/sssd.conf", "--out", tmpDir + "/snap.tar.gz"},
			wantCode: 1,
		},
		{
			name:     "path outside allowlist",
			args:     []string{"--plugin", "sssd", "--node-id", "aaaa-bbbb-cccc-dddd-eeee", "--paths", "/tmp/evil,/etc/sssd/sssd.conf", "--out", tmpDir + "/snap.tar.gz"},
			wantCode: 1,
		},
		{
			name:     "out path not under backupBaseDir",
			args:     []string{"--plugin", "sssd", "--node-id", "aaaa-bbbb-cccc-dddd-eeee", "--paths", "/etc/sssd/sssd.conf", "--out", tmpDir + "/snap.tar.gz"},
			wantCode: 1, // tmpDir is not under /var/lib/clustr/backups/
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verbBackupWrite(0, tt.args)
			if got != tt.wantCode {
				t.Errorf("verbBackupWrite(%v) = %d, want %d", tt.args, got, tt.wantCode)
			}
		})
	}
}

func TestVerbBackupRestoreValidation(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "missing required flags",
			args:     []string{"--tarball", "/var/lib/clustr/backups/test.tar.gz"},
			wantCode: 1,
		},
		{
			name:     "tarball outside backupBaseDir",
			args:     []string{"--tarball", "/tmp/evil.tar.gz", "--node-id", "aaaa-bbbb-cccc-dddd", "--plugin", "sssd"},
			wantCode: 1,
		},
		{
			name:     "tarball does not end in .tar.gz",
			args:     []string{"--tarball", "/var/lib/clustr/backups/test.tar", "--node-id", "aaaa-bbbb-cccc-dddd", "--plugin", "sssd"},
			wantCode: 1,
		},
		{
			name:     "unsafe plugin name",
			args:     []string{"--tarball", "/var/lib/clustr/backups/snap.tar.gz", "--node-id", "aaaa-bbbb-cccc-dddd", "--plugin", "bad name"},
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verbBackupRestore(0, tt.args)
			if got != tt.wantCode {
				t.Errorf("verbBackupRestore(%v) = %d, want %d", tt.args, got, tt.wantCode)
			}
		})
	}
}

// ─── tarball round-trip ───────────────────────────────────────────────────────

func TestTarballRoundTrip(t *testing.T) {
	// Create a temp source tree with a file to back up.
	srcDir := t.TempDir()
	testFile := filepath.Join(srcDir, "test.conf")
	testContent := []byte("# test config content\nkey=value\n")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Create the tarball.
	tarballDir := t.TempDir()
	tarball := filepath.Join(tarballDir, "snapshot.tar.gz")

	if err := createTarball(tarball, []string{testFile}); err != nil {
		t.Fatalf("createTarball: %v", err)
	}

	// Verify the tarball is non-empty.
	info, err := os.Stat(tarball)
	if err != nil {
		t.Fatalf("stat tarball: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("tarball is empty")
	}

	// Verify the tarball contains the expected entry.
	f, err := os.Open(tarball)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		// The header name should be the path without leading slash.
		expectedName := filepath.ToSlash(testFile)[1:] // strip leading "/"
		if hdr.Name == expectedName {
			content, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read entry content: %v", err)
			}
			if string(content) != string(testContent) {
				t.Errorf("entry content mismatch: got %q want %q", content, testContent)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("expected entry %s not found in tarball", testFile)
	}
}

func TestExtractTarball(t *testing.T) {
	// Build a tarball with a file simulating /etc/ structure.
	srcDir := t.TempDir()
	confDir := filepath.Join(srcDir, "etc", "sssd")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	confFile := filepath.Join(confDir, "sssd.conf")
	originalContent := []byte("[sssd]\ndomains = test\n")
	if err := os.WriteFile(confFile, originalContent, 0600); err != nil {
		t.Fatal(err)
	}

	// Create a tarball (manually, to avoid the allowlist restriction in createTarball
	// which checks isSafeBackupPath against the live allowlist).
	tarballDir := t.TempDir()
	tarball := filepath.Join(tarballDir, "snapshot.tar.gz")
	if err := createTestTarball(t, tarball, map[string][]byte{
		"etc/sssd/sssd.conf": originalContent,
	}); err != nil {
		t.Fatalf("create test tarball: %v", err)
	}

	// Extract to a different temp directory.
	destRoot := t.TempDir()
	if err := extractTarball(tarball, destRoot); err != nil {
		t.Fatalf("extractTarball: %v", err)
	}

	// Verify the file was restored.
	restored, err := os.ReadFile(filepath.Join(destRoot, "etc", "sssd", "sssd.conf"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != string(originalContent) {
		t.Errorf("restored content mismatch: got %q want %q", restored, originalContent)
	}
}

// ─── SSSD BackupSpec smoke test ───────────────────────────────────────────────

// TestCreateTarball_SSSDDefaultPaths verifies createTarball behaviour when some
// of the SSSD default BackupSpec paths don't exist (mirrors production: a fresh
// node may have /etc/sssd/sssd.conf but not /etc/sssd/conf.d/ or
// /var/lib/sss/db/).
//
// Assertions:
//   - A non-empty tarball is produced even when some paths are missing.
//   - The tarball contains the one file that does exist.
//   - Missing paths are silently skipped (no error returned).
func TestCreateTarball_SSSDDefaultPaths(t *testing.T) {
	// Build a temp tree that mimics a minimal SSSD installation:
	//   present:  <root>/etc/sssd/sssd.conf
	//   missing:  <root>/etc/sssd/conf.d/       (doesn't exist)
	//   missing:  <root>/var/lib/sss/db/         (doesn't exist)
	root := t.TempDir()
	confDir := filepath.Join(root, "etc", "sssd")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	confFile := filepath.Join(confDir, "sssd.conf")
	sssdConfContent := []byte("[sssd]\ndomains = test\nconfig_file_version = 2\n")
	if err := os.WriteFile(confFile, sssdConfContent, 0600); err != nil {
		t.Fatal(err)
	}

	missingDir1 := filepath.Join(confDir, "conf.d")   // does not exist
	missingDir2 := filepath.Join(root, "var", "lib", "sss", "db") // does not exist

	paths := []string{confFile, missingDir1, missingDir2}

	tarball := filepath.Join(t.TempDir(), "sssd-snapshot.tar.gz")
	if err := createTarball(tarball, paths); err != nil {
		t.Fatalf("createTarball with missing paths: %v", err)
	}

	// Tarball must be non-empty.
	info, err := os.Stat(tarball)
	if err != nil {
		t.Fatalf("stat tarball: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("tarball is empty — expected at least sssd.conf to be included")
	}

	// Tarball must contain sssd.conf (the one file that exists).
	f, err := os.Open(tarball)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		// Path stored without leading slash; strip the temp root prefix too.
		wantSuffix := "etc/sssd/sssd.conf"
		if len(hdr.Name) >= len(wantSuffix) && hdr.Name[len(hdr.Name)-len(wantSuffix):] == wantSuffix {
			found = true
		}
	}
	if !found {
		t.Error("sssd.conf was not found in the tarball even though the file exists")
	}
}

// TestCreateTarball_UnreadablePath verifies that createTarball returns a clear
// "path not readable: <path>: ..." error when a file exists but cannot be read.
func TestCreateTarball_UnreadablePath(t *testing.T) {
	// Create a file then chmod it unreadable.
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "unreadable.conf")
	if err := os.WriteFile(secretFile, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secretFile, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(secretFile, 0600) //nolint:errcheck // cleanup

	tarball := filepath.Join(t.TempDir(), "snap.tar.gz")
	err := createTarball(tarball, []string{secretFile})

	// If the test runs as root, the permission check won't fire; skip in that case.
	if os.Getuid() == 0 {
		t.Skip("skipping unreadable-path test: running as root (permissions not enforced)")
	}

	if err == nil {
		t.Fatal("expected error from createTarball with unreadable file, got nil")
	}
	const wantPrefix = "path not readable:"
	if len(err.Error()) < len(wantPrefix) || err.Error()[:len(wantPrefix)] != wantPrefix {
		t.Errorf("error message = %q; want prefix %q", err.Error(), wantPrefix)
	}
}

// createTestTarball creates a gzipped tarball from a map of name → content
// without the allowlist restriction (used in tests where we supply arbitrary paths).
func createTestTarball(t *testing.T, outPath string, files map[string][]byte) error {
	t.Helper()

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Size: int64(len(content)),
			Mode: 0600,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gw.Close()
}
