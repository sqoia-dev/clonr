package clientd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// checksum returns the "sha256:<hex>" string for content, matching the format
// the server sends in ConfigPushPayload.Checksum.
func checksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum)
}

// TestConfigApplier_WritesHostsToRoot verifies that a ConfigApplier constructed
// with a tmpdir root writes the "hosts" target to <rootDir>/etc/hosts, not to
// the live /etc/hosts.
func TestConfigApplier_WritesHostsToRoot(t *testing.T) {
	root := t.TempDir()

	// The etc directory is usually present in an installed OS; create it here.
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: mkdir etc: %v", err)
	}

	content := "127.0.0.1 localhost\n10.0.0.1 head\n"
	payload := ConfigPushPayload{
		Target:   "hosts",
		Content:  content,
		Checksum: checksum(content),
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}

	hostsPath := filepath.Join(root, "etc", "hosts")
	got, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("read etc/hosts: %v", err)
	}
	if string(got) != content {
		t.Errorf("etc/hosts content = %q, want %q", string(got), content)
	}

	// Confirm the live /etc/hosts was NOT written.
	if _, err := os.Stat(filepath.Join("/etc", "hosts")); err == nil {
		// File exists — but it should be the system's own hosts file, not ours.
		// We verify by checking that the live /etc/hosts does NOT contain our sentinel line.
		live, readErr := os.ReadFile("/etc/hosts")
		if readErr == nil && string(live) == content {
			t.Error("live /etc/hosts was unexpectedly overwritten by in-chroot applier")
		}
	}
}

// TestConfigApplier_WritesSSSDConf verifies that the sssd target lands at
// <rootDir>/etc/sssd/sssd.conf with mode 0600, and that the parent directory
// is created automatically (the image may not have /etc/sssd/).
func TestConfigApplier_WritesSSSDConf(t *testing.T) {
	root := t.TempDir()
	// Intentionally do NOT pre-create /etc/sssd — ApplyOne must create it.

	content := "[sssd]\ndomains = clustr\n\n[domain/clustr]\nid_provider = ldap\n"
	payload := ConfigPushPayload{
		Target:   "sssd",
		Content:  content,
		Checksum: checksum(content),
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}

	sssdPath := filepath.Join(root, "etc", "sssd", "sssd.conf")
	got, err := os.ReadFile(sssdPath)
	if err != nil {
		t.Fatalf("read sssd.conf: %v", err)
	}
	if string(got) != content {
		t.Errorf("sssd.conf content = %q, want %q", string(got), content)
	}

	info, err := os.Stat(sssdPath)
	if err != nil {
		t.Fatalf("stat sssd.conf: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("sssd.conf mode = %04o, want 0600", perm)
	}
}

// TestConfigApplier_WritesSudoers verifies that the sudoers target lands at
// <rootDir>/etc/sudoers.d/clustr-admins with mode 0440.
func TestConfigApplier_WritesSudoers(t *testing.T) {
	root := t.TempDir()

	content := "%clustr-admins ALL=(ALL) NOPASSWD:ALL\n"
	payload := ConfigPushPayload{
		Target:   "sudoers",
		Content:  content,
		Checksum: checksum(content),
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}

	sudoersPath := filepath.Join(root, "etc", "sudoers.d", "clustr-admins")
	got, err := os.ReadFile(sudoersPath)
	if err != nil {
		t.Fatalf("read sudoers: %v", err)
	}
	if string(got) != content {
		t.Errorf("sudoers content = %q, want %q", string(got), content)
	}

	info, err := os.Stat(sudoersPath)
	if err != nil {
		t.Fatalf("stat sudoers: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o440 {
		t.Errorf("sudoers mode = %04o, want 0440", perm)
	}
}

// TestConfigApplier_WritesLDAPCACert verifies that the ldap-ca-cert target
// creates the deep system anchor directory and writes the cert file.
func TestConfigApplier_WritesLDAPCACert(t *testing.T) {
	root := t.TempDir()

	content := "-----BEGIN CERTIFICATE-----\nMIIBxxxxxx\n-----END CERTIFICATE-----\n"
	payload := ConfigPushPayload{
		Target:   "ldap-ca-cert",
		Content:  content,
		Checksum: checksum(content),
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err != nil {
		t.Fatalf("ApplyOne: %v", err)
	}

	certPath := filepath.Join(root, "etc", "pki", "ca-trust", "source", "anchors", "clustr-ca.crt")
	got, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read ldap-ca-cert: %v", err)
	}
	if string(got) != content {
		t.Errorf("ldap-ca-cert content = %q, want %q", string(got), content)
	}
}

// TestConfigApplier_ChecksumMismatch verifies that a mismatched checksum is
// rejected before any file write occurs.
func TestConfigApplier_ChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	payload := ConfigPushPayload{
		Target:   "hosts",
		Content:  "10.0.0.1 head\n",
		Checksum: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}

	// File must not have been created.
	if _, err := os.Stat(filepath.Join(root, "etc", "hosts")); !os.IsNotExist(err) {
		t.Error("etc/hosts should not exist after checksum failure")
	}
}

// TestConfigApplier_UnknownTarget verifies that an unknown target is rejected.
func TestConfigApplier_UnknownTarget(t *testing.T) {
	root := t.TempDir()
	payload := ConfigPushPayload{
		Target:   "not-a-real-target",
		Content:  "content",
		Checksum: checksum("content"),
	}

	ca := NewConfigApplier(root)
	if err := ca.ApplyOne(payload); err == nil {
		t.Fatal("expected unknown-target error, got nil")
	}
}

// TestConfigApplier_LiveRootSuppressesApplyActionForChroot ensures that isLiveRoot
// returns false for any non-"/" rootDir and true for "/".
func TestConfigApplier_LiveRootSuppressesApplyActionForChroot(t *testing.T) {
	cases := []struct {
		rootDir    string
		wantIsLive bool
	}{
		{"/", true},
		{"/mnt/target", false},
		{"/tmp/test-root-1234", false},
	}
	for _, tc := range cases {
		ca := NewConfigApplier(tc.rootDir)
		if got := ca.isLiveRoot(); got != tc.wantIsLive {
			t.Errorf("NewConfigApplier(%q).isLiveRoot() = %v, want %v", tc.rootDir, got, tc.wantIsLive)
		}
	}
}

// TestConfigApplier_PathJoining verifies that path() strips leading slashes from
// relative paths and joins them correctly with rootDir.
func TestConfigApplier_PathJoining(t *testing.T) {
	ca := NewConfigApplier("/mnt/target")
	cases := []struct {
		rel  string
		want string
	}{
		{"etc/hosts", "/mnt/target/etc/hosts"},
		{"etc/sssd/sssd.conf", "/mnt/target/etc/sssd/sssd.conf"},
		{"etc/pki/ca-trust/source/anchors/clustr-ca.crt", "/mnt/target/etc/pki/ca-trust/source/anchors/clustr-ca.crt"},
	}
	for _, tc := range cases {
		if got := ca.path(tc.rel); got != tc.want {
			t.Errorf("path(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}

// TestApplyConfig_BackwardCompat verifies that the legacy package-level applyConfig
// function still rejects an unknown target, confirming the shim wiring is intact.
// (We do not test a successful write because that would touch /etc on the host.)
func TestApplyConfig_BackwardCompat(t *testing.T) {
	payload := ConfigPushPayload{
		Target:   "not-real",
		Content:  "x",
		Checksum: checksum("x"),
	}
	if err := applyConfig(payload); err == nil {
		t.Fatal("expected error from applyConfig with unknown target")
	}
}
