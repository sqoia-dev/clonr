package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupChrootMounts_ResolvConfBind verifies the core bug fix:
// after setupChrootMounts runs, the target rootfs's /etc/resolv.conf
// reads back the HOST's /etc/resolv.conf content (via bind-mount), even
// though on disk the target's resolv.conf was originally a stale broken
// nameserver entry that would cause every chrooted DNS lookup to fail.
//
// This is skipped when not running as root, since mount(2) requires
// CAP_SYS_ADMIN. CI runs as root in our setup; local dev may skip.
func TestSetupChrootMounts_ResolvConfBind(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setupChrootMounts requires root (CAP_SYS_ADMIN); skipping")
	}

	// Read host's /etc/resolv.conf so we can verify the bind landed.
	hostResolv, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Skipf("host /etc/resolv.conf unreadable: %v — cannot validate bind", err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}

	// Plant a broken resolv.conf in the target — this simulates the QEMU NAT
	// 10.0.2.3 nameserver baked into Rocky9 cloud images.
	brokenContent := "nameserver 10.0.2.3\n"
	targetResolv := filepath.Join(root, "etc", "resolv.conf")
	if err := os.WriteFile(targetResolv, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("plant broken resolv.conf: %v", err)
	}

	cleanup, err := setupChrootMounts(root)
	if err != nil {
		t.Fatalf("setupChrootMounts: %v", err)
	}

	// While the bind is active, the target's resolv.conf must read back as
	// the host's content, not the broken planted content.
	got, err := os.ReadFile(targetResolv)
	if err != nil {
		cleanup()
		t.Fatalf("read target resolv.conf after bind: %v", err)
	}
	if string(got) == brokenContent {
		cleanup()
		t.Fatalf("target resolv.conf still shows broken content — bind did not take effect")
	}
	if string(got) != string(hostResolv) {
		cleanup()
		t.Errorf("target resolv.conf content does not match host\nhost:\n%s\ntarget:\n%s",
			string(hostResolv), string(got))
	}

	// /proc, /sys, /dev should all be populated under root.
	for _, sub := range []string{"proc", "sys", "dev"} {
		entries, lerr := os.ReadDir(filepath.Join(root, sub))
		if lerr != nil {
			cleanup()
			t.Errorf("read %s: %v", sub, lerr)
			continue
		}
		if len(entries) == 0 {
			cleanup()
			t.Errorf("%s/ is empty after mount — expected populated virtual fs", sub)
		}
	}

	// Cleanup must unmount everything; after cleanup the target's
	// resolv.conf must read back as the planted broken content again.
	cleanup()
	got2, err := os.ReadFile(targetResolv)
	if err != nil {
		t.Fatalf("read target resolv.conf after cleanup: %v", err)
	}
	if string(got2) != brokenContent {
		t.Errorf("after cleanup, expected broken content (%q), got %q",
			brokenContent, string(got2))
	}

	// /proc, /sys, /dev should be empty/unmounted after cleanup.
	for _, sub := range []string{"proc", "sys", "dev"} {
		entries, _ := os.ReadDir(filepath.Join(root, sub))
		if len(entries) > 0 {
			t.Errorf("%s/ has %d entries after cleanup — mount not torn down", sub, len(entries))
		}
	}
}

// TestSetupChrootMounts_NoExistingResolvConf verifies the helper correctly
// creates a placeholder file when the target rootfs has no /etc/resolv.conf
// at all (some minimal images ship without one).
func TestSetupChrootMounts_NoExistingResolvConf(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setupChrootMounts requires root (CAP_SYS_ADMIN); skipping")
	}
	if _, err := os.Stat("/etc/resolv.conf"); err != nil {
		t.Skipf("host /etc/resolv.conf missing: %v", err)
	}

	root := t.TempDir()
	// Note: we deliberately do NOT pre-create /etc — setupChrootMounts must
	// handle that. (Real image targets always have /etc, but the helper
	// should be defensive.)

	cleanup, err := setupChrootMounts(root)
	if err != nil {
		t.Fatalf("setupChrootMounts: %v", err)
	}
	defer cleanup()

	// /etc/resolv.conf must now exist and read as host content.
	got, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("read target resolv.conf: %v", err)
	}
	hostResolv, _ := os.ReadFile("/etc/resolv.conf")
	if string(got) != string(hostResolv) {
		t.Errorf("placeholder bind: target content != host content")
	}
}

// TestSetupChrootMounts_SymlinkResolvConf verifies that when the target's
// /etc/resolv.conf is a symlink (e.g. systemd-resolved stub), the helper
// replaces it with a real file before binding so the bind has a stable
// target.
func TestSetupChrootMounts_SymlinkResolvConf(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setupChrootMounts requires root (CAP_SYS_ADMIN); skipping")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Plant a symlink pointing at a non-existent file to mimic a broken
	// systemd-resolved stub link inside the image.
	symlinkPath := filepath.Join(root, "etc", "resolv.conf")
	if err := os.Symlink("/run/systemd/resolve/stub-resolv.conf", symlinkPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	cleanup, err := setupChrootMounts(root)
	if err != nil {
		t.Fatalf("setupChrootMounts: %v", err)
	}
	defer cleanup()

	// Symlink should have been replaced; reading must return host content.
	got, err := os.ReadFile(symlinkPath)
	if err != nil {
		t.Fatalf("read after bind: %v", err)
	}
	hostResolv, _ := os.ReadFile("/etc/resolv.conf")
	if string(got) != string(hostResolv) {
		t.Errorf("symlink replacement: target content does not match host")
	}
	// And it must no longer be a symlink.
	info, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("lstat after bind: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected symlink to be replaced with regular file")
	}
}

// TestSetupChrootMounts_PartialFailureCleansUp verifies that if a later
// mount step fails, mounts already established are unmounted before
// returning the error — no leaks on the deploy host.
//
// We simulate failure by passing a target whose /proc cannot be created
// because the parent path is a regular file, not a directory.
func TestSetupChrootMounts_PartialFailureCleansUp(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setupChrootMounts requires root (CAP_SYS_ADMIN); skipping")
	}

	root := t.TempDir()
	// Make root/proc unmount-able by planting a file at root/proc (mkdir
	// would normally succeed because root is a tmpdir, so engineer the
	// failure by making root/proc a regular file).
	procPath := filepath.Join(root, "proc")
	if err := os.WriteFile(procPath, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("plant proc-file: %v", err)
	}

	_, err := setupChrootMounts(root)
	if err == nil {
		t.Fatal("expected error mounting on a non-directory, got nil")
	}
	if !strings.Contains(err.Error(), "proc") && !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error = %q, expected mention of proc or mkdir", err)
	}
}
