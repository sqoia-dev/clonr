package handlers

// nspawn_nonewprivs_test.go — BONUS-3: unit tests for the NoNewPrivileges
// guard in wrapNspawnInScope.
//
// These tests live in the internal `handlers` package so they can access the
// unexported procStatusPath sentinel and the wrapNspawnInScope function
// directly, without requiring real /proc manipulation or root privileges.

import (
	"os"
	"path/filepath"
	"testing"
)

// writeStatusFile writes a minimal /proc/self/status-alike file containing the
// given NoNewPrivs value into a temp directory, and returns the path.
func writeStatusFile(t *testing.T, noNewPrivsVal string) string {
	t.Helper()
	dir := t.TempDir()
	content := "Name:\tclustrd\nNoNewPrivs:\t" + noNewPrivsVal + "\nUid:\t0 0 0 0\n"
	path := filepath.Join(dir, "status")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeStatusFile: %v", err)
	}
	return path
}

// TestIsNoNewPrivilegesActive_Set verifies that a status file with
// NoNewPrivs: 1 is detected correctly.
func TestIsNoNewPrivilegesActive_Set(t *testing.T) {
	orig := procStatusPath
	defer func() { procStatusPath = orig }()
	procStatusPath = writeStatusFile(t, "1")

	if !isNoNewPrivilegesActive() {
		t.Error("expected isNoNewPrivilegesActive() == true when NoNewPrivs: 1")
	}
}

// TestIsNoNewPrivilegesActive_Clear verifies that a status file with
// NoNewPrivs: 0 returns false.
func TestIsNoNewPrivilegesActive_Clear(t *testing.T) {
	orig := procStatusPath
	defer func() { procStatusPath = orig }()
	procStatusPath = writeStatusFile(t, "0")

	if isNoNewPrivilegesActive() {
		t.Error("expected isNoNewPrivilegesActive() == false when NoNewPrivs: 0")
	}
}

// TestWrapNspawnInScope_RefusesFallbackUnderNoNewPrivs verifies that
// wrapNspawnInScope returns a non-nil error (and nil *Cmd) when systemd-run
// is not available AND NoNewPrivileges=true is active.
//
// This simulates the exact scenario described in BONUS-3: a service unit with
// NoNewPrivileges=true where systemd-run is absent (e.g. a minimal container
// or a stripped-down install). The fallback direct-nspawn invocation would
// silently fail at runtime, so we refuse early with a clear operator-facing
// error.
func TestWrapNspawnInScope_RefusesFallbackUnderNoNewPrivs(t *testing.T) {
	// Point procStatusPath at a file reporting NoNewPrivs: 1.
	origStatus := procStatusPath
	defer func() { procStatusPath = origStatus }()
	procStatusPath = writeStatusFile(t, "1")

	// Disable the systemd-run availability flag so the fallback path is taken.
	origAvail := systemdRunAvailable
	defer func() { systemdRunAvailable = origAvail }()
	systemdRunAvailable = false

	cmd, err := wrapNspawnInScope("test-session-id", []string{"--quiet", "-D", "/tmp/rootfs"})
	if err == nil {
		t.Fatal("expected error when fallback is taken under NoNewPrivileges=true, got nil")
	}
	if cmd != nil {
		t.Errorf("expected nil *exec.Cmd on error, got %v", cmd)
	}

	// The error message must mention both NoNewPrivileges and systemd-run so
	// the operator knows exactly what to fix.
	msg := err.Error()
	if len(msg) < 50 {
		t.Errorf("error message too short (%d chars): %q", len(msg), msg)
	}
}

// TestWrapNspawnInScope_AllowsFallbackWhenNoNewPrivsAbsent verifies that the
// fallback path succeeds (returns a non-nil Cmd and no error) when
// NoNewPrivileges is not set, even without systemd-run.
func TestWrapNspawnInScope_AllowsFallbackWhenNoNewPrivsAbsent(t *testing.T) {
	origStatus := procStatusPath
	defer func() { procStatusPath = origStatus }()
	procStatusPath = writeStatusFile(t, "0")

	origAvail := systemdRunAvailable
	defer func() { systemdRunAvailable = origAvail }()
	systemdRunAvailable = false

	cmd, err := wrapNspawnInScope("test-session-id", []string{"--quiet", "-D", "/tmp/rootfs"})
	if err != nil {
		t.Fatalf("unexpected error when NoNewPrivileges is clear: %v", err)
	}
	if cmd == nil {
		t.Fatal("expected non-nil *exec.Cmd when fallback is allowed")
	}
}
