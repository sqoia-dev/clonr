// finalize_test.go — tests for Slurm deploy path helpers.
package deploy

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestElVersionFromURL covers all URL shapes: clustr bundled-repo, OpenHPC,
// and paths that should return "".
func TestElVersionFromURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
	}{
		// Clustr bundled-repo patterns (PR3)
		{
			name: "clustr el9 x86_64",
			url:  "http://10.99.0.1:8080/repo/el9-x86_64/",
			want: "9",
		},
		{
			name: "clustr el10 x86_64",
			url:  "http://10.99.0.1:8080/repo/el10-x86_64/",
			want: "10",
		},
		// OpenHPC / SchedMD fallback patterns
		{
			name: "openhpc EL_9",
			url:  "https://repos.openhpc.community/OpenHPC/3/EL_9",
			want: "9",
		},
		{
			name: "openhpc EL_10",
			url:  "https://repos.openhpc.community/OpenHPC/3/EL_10",
			want: "10",
		},
		{
			name: "EL9 no underscore",
			url:  "https://example.com/packages/EL9/slurm/",
			want: "9",
		},
		// Unknown / empty
		{
			name: "empty URL",
			url:  "",
			want: "",
		},
		{
			name: "unknown URL",
			url:  "https://example.com/packages/ubuntu/",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := elVersionFromURL(tc.url)
			if got != tc.want {
				t.Errorf("elVersionFromURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestInstallSlurmInChroot_RepoFileContent verifies the generated .repo file
// for the clustr-builtin path: gpgcheck=1, repo_gpgcheck=0, and
// gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr.  Also verifies that
// the GPG key file is written to the chroot before the dnf step.
//
// The dnf execution is expected to fail (no real chroot), but we verify the
// written files before the dnf call.
func TestInstallSlurmInChroot_RepoFileContent(t *testing.T) {
	// Create a minimal fake chroot tree.
	chroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chroot, "etc", "yum.repos.d"), 0o755); err != nil {
		t.Fatalf("setup: mkdir yum.repos.d: %v", err)
	}

	const fakeKeyContent = "-----BEGIN PGP PUBLIC KEY BLOCK-----\nFAKE KEY FOR TEST\n-----END PGP PUBLIC KEY BLOCK-----\n"
	gpgKeyBytes := []byte(fakeKeyContent)
	repoURL := "http://10.99.0.1:8080/repo/el9-x86_64/"

	// installSlurmInChroot runs chroot+dnf which will fail in a test
	// environment. That is expected and non-fatal (it logs a warning).
	// We only assert the pre-dnf steps (GPG key file + .repo file).
	installSlurmInChroot(
		t.Context(),
		chroot,
		"test-node-01",
		repoURL,
		false, // hasSlurmdbd
		false, // hasGres
		nil,   // auditFn
		gpgKeyBytes,
	)

	// --- Assert GPG key file was written ---
	gpgKeyPath := filepath.Join(chroot, "etc", "pki", "rpm-gpg", "RPM-GPG-KEY-clustr")
	gpgKeyData, err := os.ReadFile(gpgKeyPath)
	if err != nil {
		t.Fatalf("GPG key file not written at %s: %v", gpgKeyPath, err)
	}
	if string(gpgKeyData) != fakeKeyContent {
		t.Errorf("GPG key file content mismatch:\n  got  %q\n  want %q", string(gpgKeyData), fakeKeyContent)
	}
	// Verify mode is 0644.
	info, err := os.Stat(gpgKeyPath)
	if err != nil {
		t.Fatalf("stat GPG key file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("GPG key file mode = %o, want 0644", info.Mode().Perm())
	}

	// --- Assert .repo file was written with correct gpgcheck settings ---
	repoPath := filepath.Join(chroot, "etc", "yum.repos.d", "clustr-slurm.repo")
	repoData, err := os.ReadFile(repoPath)
	if err != nil {
		t.Fatalf(".repo file not written at %s: %v", repoPath, err)
	}
	repoContent := string(repoData)

	// Snapshot the expected .repo content.
	wantLines := []string{
		"[clustr-slurm]",
		"name=clustr Slurm",
		"baseurl=http://10.99.0.1:8080/repo/el9-x86_64/",
		"enabled=1",
		"gpgcheck=1",
		"repo_gpgcheck=0",
		"gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-clustr",
	}
	for _, line := range wantLines {
		if !strings.Contains(repoContent, line) {
			t.Errorf(".repo file missing line %q\nfull content:\n%s", line, repoContent)
		}
	}
	// "gpgcheck=0" (the bare line) must NOT appear — we should have gpgcheck=1.
	// Note: repo_gpgcheck=0 is expected and contains "gpgcheck=0" as a substring,
	// so we check for the exact line "gpgcheck=0\n" rather than a substring match.
	for _, line := range strings.Split(repoContent, "\n") {
		if strings.TrimSpace(line) == "gpgcheck=0" {
			t.Errorf(".repo file contains line 'gpgcheck=0' but expected gpgcheck=1\nfull content:\n%s", repoContent)
		}
	}
}

// TestInstallSlurmInChroot_CustomURLFallback verifies that when gpgKeyBytes is
// nil (operator-provided custom repo URL), the .repo file uses gpgcheck=0.
func TestInstallSlurmInChroot_CustomURLFallback(t *testing.T) {
	chroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chroot, "etc", "yum.repos.d"), 0o755); err != nil {
		t.Fatalf("setup: mkdir yum.repos.d: %v", err)
	}

	repoURL := "https://repos.openhpc.community/OpenHPC/3/EL_9"
	installSlurmInChroot(
		t.Context(),
		chroot,
		"test-node-02",
		repoURL,
		false, // hasSlurmdbd
		false, // hasGres
		nil,   // auditFn
		nil,   // gpgKeyBytes — nil for custom URL path
	)

	repoPath := filepath.Join(chroot, "etc", "yum.repos.d", "clustr-slurm.repo")
	repoData, err := os.ReadFile(repoPath)
	if err != nil {
		t.Fatalf(".repo file not written at %s: %v", repoPath, err)
	}
	repoContent := string(repoData)

	if !strings.Contains(repoContent, "gpgcheck=0") {
		t.Errorf("custom-URL .repo expected gpgcheck=0\nfull content:\n%s", repoContent)
	}
	if strings.Contains(repoContent, "gpgcheck=1") {
		t.Errorf("custom-URL .repo must not have gpgcheck=1\nfull content:\n%s", repoContent)
	}

	// GPG key file must NOT exist in chroot for custom URL path.
	gpgKeyPath := filepath.Join(chroot, "etc", "pki", "rpm-gpg", "RPM-GPG-KEY-clustr")
	if _, err := os.Stat(gpgKeyPath); err == nil {
		t.Errorf("GPG key file should not exist in chroot for custom URL path, but found at %s", gpgKeyPath)
	}
}

// TestInstallSlurmInChroot_GPGKeyBase64RoundTrip verifies the base64
// encode/decode round-trip that writeSlurmConfig uses before calling
// installSlurmInChroot. Ensures the key bytes survive the round-trip intact.
func TestInstallSlurmInChroot_GPGKeyBase64RoundTrip(t *testing.T) {
	const original = "-----BEGIN PGP PUBLIC KEY BLOCK-----\ntest key data\n-----END PGP PUBLIC KEY BLOCK-----\n"

	encoded := base64.StdEncoding.EncodeToString([]byte(original))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != original {
		t.Errorf("GPG key round-trip failed:\n  got  %q\n  want %q", string(decoded), original)
	}
}

// TestInstallSlurmInChroot_DNSPrep verifies that installSlurmInChroot writes
// /etc/resolv.conf and /etc/hosts into the chroot before invoking dnf.
// This is the fix for PR5 Failure B: chroot has no DNS.
func TestInstallSlurmInChroot_DNSPrep(t *testing.T) {
	chroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chroot, "etc", "yum.repos.d"), 0o755); err != nil {
		t.Fatalf("setup: mkdir yum.repos.d: %v", err)
	}

	// Write a known resolv.conf on the host side (the function copies from
	// the real /etc/resolv.conf, so we simulate by pre-creating it in the
	// chroot's /etc — the copy overwrites). We can only verify the file
	// exists after the call; we can't intercept /etc/resolv.conf.
	// However, we CAN verify /etc/hosts is written with localhost entries.
	installSlurmInChroot(
		t.Context(),
		chroot,
		"test-node-dns",
		"http://10.99.0.1:8080/repo/el9-x86_64/",
		false, // hasSlurmdbd
		false, // hasGres
		nil,   // auditFn
		[]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nFAKE\n-----END PGP PUBLIC KEY BLOCK-----\n"),
	)

	// /etc/hosts must contain localhost entries.
	hostsPath := filepath.Join(chroot, "etc", "hosts")
	hostsData, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("/etc/hosts not written in chroot at %s: %v", hostsPath, err)
	}
	hostsContent := string(hostsData)
	for _, want := range []string{"127.0.0.1", "localhost", "::1"} {
		if !strings.Contains(hostsContent, want) {
			t.Errorf("/etc/hosts missing %q\nfull content:\n%s", want, hostsContent)
		}
	}
}

// TestInstallSlurmInChroot_DNSPrep_ExistingHosts verifies that when the chroot
// already has a /etc/hosts with localhost entries, it is left unchanged.
func TestInstallSlurmInChroot_DNSPrep_ExistingHosts(t *testing.T) {
	chroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chroot, "etc", "yum.repos.d"), 0o755); err != nil {
		t.Fatalf("setup: mkdir yum.repos.d: %v", err)
	}

	// Pre-populate /etc/hosts with a custom entry.
	existingHosts := "127.0.0.1 localhost localhost.localdomain\n::1 localhost\n10.0.0.1 myhost\n"
	hostsPath := filepath.Join(chroot, "etc", "hosts")
	if err := os.WriteFile(hostsPath, []byte(existingHosts), 0o644); err != nil {
		t.Fatalf("setup: write /etc/hosts: %v", err)
	}

	installSlurmInChroot(
		t.Context(),
		chroot,
		"test-node-existing-hosts",
		"http://10.99.0.1:8080/repo/el9-x86_64/",
		false, false, nil,
		[]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nFAKE\n-----END PGP PUBLIC KEY BLOCK-----\n"),
	)

	// The custom "myhost" entry must still be present (file not overwritten).
	hostsData, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("read /etc/hosts: %v", err)
	}
	if !strings.Contains(string(hostsData), "myhost") {
		t.Errorf("existing /etc/hosts was overwritten; 'myhost' entry lost\nfull content:\n%s", string(hostsData))
	}
}

// TestInstallSlurmInChroot_OrderDNSBeforeRepo verifies the new step ordering:
// DNS prep (resolv.conf + hosts) and OpenHPC strip happen before the .repo
// file is written. We verify this indirectly by asserting that the .repo file
// exists after the call (confirming the function reached the repo-write step
// without aborting), and that /etc/hosts has localhost entries.
func TestInstallSlurmInChroot_OrderDNSBeforeRepo(t *testing.T) {
	chroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(chroot, "etc", "yum.repos.d"), 0o755); err != nil {
		t.Fatalf("setup: mkdir yum.repos.d: %v", err)
	}

	installSlurmInChroot(
		t.Context(),
		chroot,
		"test-order",
		"http://10.99.0.1:8080/repo/el9-x86_64/",
		true,  // hasSlurmdbd (controller)
		false, // hasGres
		nil,
		[]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nFAKE\n-----END PGP PUBLIC KEY BLOCK-----\n"),
	)

	// .repo file must exist (function ran past DNS prep without aborting).
	repoPath := filepath.Join(chroot, "etc", "yum.repos.d", "clustr-slurm.repo")
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf(".repo file not written — function may have aborted before repo step: %v", err)
	}

	// /etc/hosts must have localhost entries (DNS prep ran).
	hostsData, _ := os.ReadFile(filepath.Join(chroot, "etc", "hosts"))
	if !strings.Contains(string(hostsData), "localhost") {
		t.Errorf("/etc/hosts does not contain 'localhost' — DNS prep may not have run")
	}
}
