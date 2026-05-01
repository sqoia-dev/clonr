package image

import (
	"archive/tar"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tarSupportsXattrs returns true when the system tar binary accepts
// --xattrs, --xattrs-include, --selinux, and --acls flags.
// Some versions of tar (e.g. macOS bsdtar) do not support these flags.
// Tests that require GNU tar xattr/ACL support skip when this returns false.
func tarSupportsXattrs(t *testing.T) bool {
	t.Helper()
	// --xattrs is a GNU tar flag. A non-zero exit code or "unrecognized option"
	// in stderr means the installed tar does not support it.
	dir := t.TempDir()
	dummy := filepath.Join(dir, "dummy.txt")
	if err := os.WriteFile(dummy, []byte("x"), 0o644); err != nil {
		t.Fatalf("tarSupportsXattrs: write dummy: %v", err)
	}
	out := filepath.Join(dir, "test.tar")
	cmd := exec.Command("tar",
		"--xattrs",
		"--xattrs-include=*",
		"--acls",
		"-C", dir,
		"-cf", out,
		"dummy.txt",
	)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// TestTarRoundTrip_XattrACLFlags verifies that the tar create flags used in
// bakeDeterministicTar (--xattrs, --xattrs-include=*, --acls) are accepted by
// the system tar binary, and that a round-trip (create → extract → re-create)
// produces a valid archive without error.
//
// We cannot assert specific xattr or ACL values in CI because GitHub Actions
// Ubuntu runners do not expose SELinux or user-namespace-scoped ACLs in a way
// that survives tar without root. We instead verify:
//  1. tar accepts the flags without error (proof flags work)
//  2. The extracted archive is non-empty
//  3. A second create pass with the same flags succeeds, establishing that the
//     capture → deploy → re-capture chain does not break on the flags themselves
//
// If GNU tar is not available (bsdtar on macOS), the test is skipped.
func TestTarRoundTrip_XattrACLFlags(t *testing.T) {
	if !tarSupportsXattrs(t) {
		t.Skip("system tar does not support --xattrs/--acls (not GNU tar) — skipping")
	}

	// Build a minimal fake rootfs with a few files.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "regular.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write regular.txt: %v", err)
	}
	subdir := filepath.Join(src, "etc")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "passwd"), []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatalf("write etc/passwd: %v", err)
	}

	out := t.TempDir()
	tarPath := filepath.Join(out, "rootfs.tar")

	// ── Step 1: create ──────────────────────────────────────────────────────
	// Reproduce the bakeDeterministicTar flag set (minus --sort, --mtime, etc.
	// which require GNU tar ≥1.28 and don't affect xattr behavior).
	createCmd := exec.Command("tar",
		"--xattrs",
		"--xattrs-include=*",
		"--acls",
		"--numeric-owner",
		"-C", src,
		"-cf", tarPath,
		".",
	)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar create with xattr/acl flags failed: %v\noutput: %s", err, out)
	}

	// Verify the archive is non-empty.
	stat, err := os.Stat(tarPath)
	if err != nil || stat.Size() == 0 {
		t.Fatalf("tar create produced empty or missing archive: %v", err)
	}

	// ── Step 2: extract ─────────────────────────────────────────────────────
	// Use the same flags as streamExtract in internal/deploy/rsync.go.
	extractDir := t.TempDir()
	extractCmd := exec.Command("tar",
		"--numeric-owner",
		"--xattrs",
		"--xattrs-include=*",
		"--acls",
		"-xf", tarPath,
		"-C", extractDir,
	)
	if out, err := extractCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar extract with xattr/acl flags failed: %v\noutput: %s", err, out)
	}

	// Verify expected files are present after extraction.
	for _, rel := range []string{"regular.txt", filepath.Join("etc", "passwd")} {
		p := filepath.Join(extractDir, rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q after extraction but not found: %v", rel, err)
		}
	}

	// ── Step 3: re-create (simulates re-capture) ────────────────────────────
	tarPath2 := filepath.Join(out, "rootfs2.tar")
	recaptureCmd := exec.Command("tar",
		"--xattrs",
		"--xattrs-include=*",
		"--acls",
		"--numeric-owner",
		"-C", extractDir,
		"-cf", tarPath2,
		".",
	)
	if out, err := recaptureCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar re-create (re-capture) failed: %v\noutput: %s", err, out)
	}

	// Both archives must contain the same file names (order may differ without
	// --sort, but the set must match).
	list1 := tarFileList(t, tarPath)
	list2 := tarFileList(t, tarPath2)
	for _, name := range list1 {
		if !stringSliceContains(list2, name) {
			t.Errorf("file %q in first tar not present in re-capture tar", name)
		}
	}
}

// tarFileList returns the list of member names in a tar archive.
func tarFileList(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("tarFileList: open %s: %v", path, err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tarFileList: read %s: %v", path, err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func stringSliceContains(ss []string, s string) bool {
	for _, x := range ss {
		if strings.TrimPrefix(x, "./") == strings.TrimPrefix(s, "./") {
			return true
		}
	}
	return false
}
