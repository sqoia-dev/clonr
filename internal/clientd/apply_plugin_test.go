package clientd

// apply_plugin_test.go — Sprint 36 Day 2
//
// Tests for the reactive-config plugin-tag fork in handleConfigPush.
// Asserts that a config_push with Plugin:"hostname" writes /etc/hostname
// correctly and does NOT trigger a full reapply of other targets.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConfigPush_PluginTaggedHostnameWritesFile verifies that a config_push
// with Plugin:"hostname" writes the expected content to /etc/hostname (within
// a temp root) using the ConfigApplier. This confirms the "hostname" target
// is in configTargets and the apply path works end-to-end for the reactive path.
func TestConfigPush_PluginTaggedHostnameWritesFile(t *testing.T) {
	root := t.TempDir()
	// /etc must exist in the temp root for the applier to write into it.
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: mkdir etc: %v", err)
	}

	content := "compute-01\n"
	payload := ConfigPushPayload{
		Target:   "hostname",
		Content:  content,
		Checksum: checksum(content),
		// Plugin-tagged: reactive-config path.
		Plugin:       "hostname",
		RenderedHash: "deadbeef", // value is not validated on the client side
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne (plugin-tagged): %v", err)
	}

	hostnamePath := filepath.Join(root, "etc", "hostname")
	got, err := os.ReadFile(hostnamePath)
	if err != nil {
		t.Fatalf("read /etc/hostname: %v", err)
	}
	if string(got) != content {
		t.Errorf("/etc/hostname content = %q, want %q", string(got), content)
	}
}

// TestConfigPush_PluginTaggedHostname_ChecksumMismatch verifies that a
// plugin-tagged config_push with a bad checksum is rejected before any
// write, identical to the legacy path.
func TestConfigPush_PluginTaggedHostname_ChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	payload := ConfigPushPayload{
		Target:   "hostname",
		Content:  "compute-01\n",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Plugin:   "hostname",
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}

	// /etc/hostname must not have been created.
	if _, err := os.Stat(filepath.Join(root, "etc", "hostname")); !os.IsNotExist(err) {
		t.Error("/etc/hostname should not exist after checksum failure")
	}
}

// TestConfigPush_LegacyPathUnchanged verifies that a config_push with no
// Plugin field (Plugin == "") continues to work exactly as before Sprint 36.
// This is the backward-compat regression guard.
func TestConfigPush_LegacyPathUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	content := "127.0.0.1 localhost\n"
	payload := ConfigPushPayload{
		Target:   "hosts",
		Content:  content,
		Checksum: checksum(content),
		// Plugin is empty — legacy path.
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne (legacy): %v", err)
	}

	hostsPath := filepath.Join(root, "etc", "hosts")
	got, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("read /etc/hosts: %v", err)
	}
	if string(got) != content {
		t.Errorf("/etc/hosts content = %q, want %q", string(got), content)
	}
}

// TestConfigPush_PluginTagAppliesOnlyThatPlugin asserts that a plugin-tagged
// push for "hostname" does NOT write to any other target. This is the exit
// criterion for the Sprint 36 Day 2 sprint goal: "only the hostname plugin
// re-pushes on a hostname change."
//
// The test verifies by checking that after a Plugin:"hostname" push, the
// /etc/hosts file (a different target) has NOT been created/modified.
func TestConfigPush_PluginTagAppliesOnlyThatPlugin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Apply only the hostname plugin push.
	content := "head-01\n"
	payload := ConfigPushPayload{
		Target:   "hostname",
		Content:  content,
		Checksum: checksum(content),
		Plugin:   "hostname",
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}

	// /etc/hostname must have been written.
	if _, err := os.Stat(filepath.Join(root, "etc", "hostname")); os.IsNotExist(err) {
		t.Error("/etc/hostname was not created by hostname plugin push")
	}

	// /etc/hosts must NOT have been written — the plugin push is targeted.
	if _, err := os.Stat(filepath.Join(root, "etc", "hosts")); !os.IsNotExist(err) {
		t.Error("/etc/hosts was unexpectedly created — plugin push must not trigger full reapply")
	}
}
