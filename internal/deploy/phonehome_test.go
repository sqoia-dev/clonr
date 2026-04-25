package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInjectPhoneHome_NoOp verifies that injectPhoneHome is a no-op when either
// the token or URL is empty, leaving the rootfs unchanged.
func TestInjectPhoneHome_NoOp(t *testing.T) {
	t.Parallel()

	rootfs := t.TempDir()

	// Both empty — no-op.
	if err := injectPhoneHome(rootfs, "", ""); err != nil {
		t.Fatalf("expected no-op, got error: %v", err)
	}

	// Only token set — still no-op.
	if err := injectPhoneHome(rootfs, "tok123", ""); err != nil {
		t.Fatalf("expected no-op with empty URL, got error: %v", err)
	}

	// Only URL set — still no-op.
	if err := injectPhoneHome(rootfs, "", "http://server/verify"); err != nil {
		t.Fatalf("expected no-op with empty token, got error: %v", err)
	}

	// Confirm nothing was written.
	clustrDir := filepath.Join(rootfs, "etc", "clustr")
	if _, err := os.Stat(clustrDir); !os.IsNotExist(err) {
		t.Fatalf("expected /etc/clustr to not exist, stat returned: %v", err)
	}
}

// TestInjectPhoneHome_Writes verifies that injectPhoneHome writes all expected
// files with correct permissions and content when given valid inputs.
// It uses a fake rootfs tree. The WantedBy symlink is now created directly via
// os.Symlink — no systemctl dependency.
func TestInjectPhoneHome_Writes(t *testing.T) {
	rootfs := t.TempDir()

	token := "clustr-node-tok-abc123"
	verifyURL := "http://clustr-server:8080/api/v1/nodes/node-id-xyz/verify-boot"

	if err := injectPhoneHome(rootfs, token, verifyURL); err != nil {
		t.Fatalf("injectPhoneHome: %v", err)
	}

	// ── Idempotency: second call must succeed without EEXIST ─────────────────
	// Simulates a retry or re-deploy attempt where the symlink already exists
	// with the correct target. The new Lstat-based logic must treat this as a
	// no-op (skip symlink creation) rather than failing with EEXIST.
	if err := injectPhoneHome(rootfs, token, verifyURL); err != nil {
		t.Fatalf("injectPhoneHome (second call, idempotency): %v", err)
	}

	// ── Stale symlink: wrong target replaced ─────────────────────────────────
	// Simulates a stale symlink from a broken previous deploy pointing to a
	// different target. The new code must remove it and create the correct one.
	wantsDirForStale := filepath.Join(rootfs, "etc", "systemd", "system", "multi-user.target.wants")
	staleLinkPath := filepath.Join(wantsDirForStale, "clustr-verify-boot.service")
	if err := os.Remove(staleLinkPath); err != nil {
		t.Fatalf("setup stale symlink: remove existing: %v", err)
	}
	if err := os.Symlink("../wrong-target.service", staleLinkPath); err != nil {
		t.Fatalf("setup stale symlink: create: %v", err)
	}
	if err := injectPhoneHome(rootfs, token, verifyURL); err != nil {
		t.Fatalf("injectPhoneHome (stale symlink replacement): %v", err)
	}
	// Confirm the symlink was corrected.
	correctedTarget, err := os.Readlink(staleLinkPath)
	if err != nil {
		t.Fatalf("readlink after stale replacement: %v", err)
	}
	if correctedTarget != "../clustr-verify-boot.service" {
		t.Errorf("after stale replacement: symlink target = %q, want %q", correctedTarget, "../clustr-verify-boot.service")
	}

	multiUserWantsDir := filepath.Join(rootfs, "etc", "systemd", "system", "multi-user.target.wants")

	// ── Assert /etc/clustr/node-token ─────────────────────────────────────────
	tokenPath := filepath.Join(rootfs, "etc", "clustr", "node-token")
	fi, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("node-token not found: %v", err)
	}
	if fi.Mode() != 0o600 {
		t.Errorf("node-token mode = %o, want 0600", fi.Mode())
	}
	got, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read node-token: %v", err)
	}
	if string(got) != token {
		t.Errorf("node-token content = %q, want %q", string(got), token)
	}

	// ── Assert /etc/clustr/verify-boot-url ────────────────────────────────────
	urlPath := filepath.Join(rootfs, "etc", "clustr", "verify-boot-url")
	if _, err := os.Stat(urlPath); err != nil {
		t.Fatalf("verify-boot-url not found: %v", err)
	}
	gotURL, err := os.ReadFile(urlPath)
	if err != nil {
		t.Fatalf("read verify-boot-url: %v", err)
	}
	if string(gotURL) != verifyURL {
		t.Errorf("verify-boot-url content = %q, want %q", string(gotURL), verifyURL)
	}

	// ── Assert /etc/systemd/system/clustr-verify-boot.service ─────────────────
	unitPath := filepath.Join(rootfs, "etc", "systemd", "system", "clustr-verify-boot.service")
	unitInfo, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("unit file not found: %v", err)
	}
	if unitInfo.Size() == 0 {
		t.Error("unit file is empty")
	}
	unitContent, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit file: %v", err)
	}
	for _, want := range []string{
		"clustr-verify-boot",
		"network-online.target",
		"ConditionPathExists=/etc/clustr/node-token",
		"ConditionPathExists=/etc/clustr/verify-boot-url",
		"WantedBy=multi-user.target",
	} {
		if !containsString(string(unitContent), want) {
			t.Errorf("unit file missing expected content %q", want)
		}
	}

	// ── Assert /usr/local/bin/clustr-verify-boot ──────────────────────────────
	scriptPath := filepath.Join(rootfs, "usr", "local", "bin", "clustr-verify-boot")
	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script not found: %v", err)
	}
	if scriptInfo.Mode()&0o111 == 0 {
		t.Errorf("script is not executable: mode %o", scriptInfo.Mode())
	}
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	for _, want := range []string{
		"#!/bin/sh",
		"/etc/clustr/node-token",
		"/etc/clustr/verify-boot-url",
		"curl",
		"Authorization: Bearer",
	} {
		if !containsString(string(scriptContent), want) {
			t.Errorf("script missing expected content %q", want)
		}
	}

	// ── Assert multi-user.target.wants symlink ───────────────────────────────
	// injectPhoneHome creates the symlink directly via os.Symlink — no systemctl.
	symlinkPath := filepath.Join(multiUserWantsDir, "clustr-verify-boot.service")
	symlinkInfo, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("WantedBy symlink not created: %v", err)
	}
	if symlinkInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink at %s, got mode %v", symlinkPath, symlinkInfo.Mode())
	}
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", symlinkPath, err)
	}
	if target != "../clustr-verify-boot.service" {
		t.Errorf("symlink target = %q, want %q", target, "../clustr-verify-boot.service")
	}
}

// containsString is a simple substring check used to avoid importing strings in tests.
func containsString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
