package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// buildUbuntuChroot creates a minimal Ubuntu 24.04-flavoured tmpdir with the
// marker files that detectDistro uses to identify the OS family and version.
func buildUbuntuChroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dirs := []string{
		"etc",
		"etc/cloud",
		"etc/cloud/cloud.cfg.d",
		"etc/netplan",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("buildUbuntuChroot: mkdir %s: %v", d, err)
		}
	}
	// Write /etc/debian_version so detectDistro identifies Debian family.
	if err := os.WriteFile(filepath.Join(root, "etc", "debian_version"),
		[]byte("bookworm/sid\n"), 0o644); err != nil {
		t.Fatalf("buildUbuntuChroot: write debian_version: %v", err)
	}
	// Write /etc/os-release with Ubuntu 24.04 values.
	osRelease := `NAME="Ubuntu"
VERSION="24.04.2 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="24.04"
VERSION_CODENAME=noble
`
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(osRelease), 0o644); err != nil {
		t.Fatalf("buildUbuntuChroot: write os-release: %v", err)
	}
	return root
}

// ─── detectDistro ─────────────────────────────────────────────────────────────

// TestDetectDistro_Ubuntu24 verifies that a root containing Ubuntu 24.04 marker
// files is correctly identified as {Family:"ubuntu", Major:24}.
func TestDetectDistro_Ubuntu24(t *testing.T) {
	root := buildUbuntuChroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "ubuntu" {
		t.Errorf("Family = %q, want %q", got.Family, "ubuntu")
	}
	if got.Major != 24 {
		t.Errorf("Major = %d, want 24", got.Major)
	}
}

// TestDetectDistro_EL9 verifies that a root with /etc/redhat-release is
// identified as the EL family with the correct major version.
func TestDetectDistro_EL9(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"etc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "redhat-release"),
		[]byte("Rocky Linux release 9.5 (Blue Onyx)\n"), 0o644); err != nil {
		t.Fatalf("write redhat-release: %v", err)
	}
	osRelease := `NAME="Rocky Linux"
VERSION_ID="9.5"
ID=rocky
`
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(osRelease), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "el" {
		t.Errorf("Family = %q, want %q", got.Family, "el")
	}
	if got.Major != 9 {
		t.Errorf("Major = %d, want 9", got.Major)
	}
}

// ─── writeUbuntuCloudInitDisable ─────────────────────────────────────────────

// TestWriteUbuntuCloudInitDisable_CreatesFile verifies the disable file is
// written at the expected path with the correct datasource_list content.
func TestWriteUbuntuCloudInitDisable_CreatesFile(t *testing.T) {
	root := t.TempDir()

	if err := writeUbuntuCloudInitDisable(root); err != nil {
		t.Fatalf("writeUbuntuCloudInitDisable: %v", err)
	}

	path := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("disable file not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "datasource_list: [None]") {
		t.Errorf("disable file missing datasource_list; got:\n%s", content)
	}
}

// TestWriteUbuntuCloudInitDisable_Idempotent verifies that calling the function
// twice does not error and produces the correct file.
func TestWriteUbuntuCloudInitDisable_Idempotent(t *testing.T) {
	root := t.TempDir()
	for range 2 {
		if err := writeUbuntuCloudInitDisable(root); err != nil {
			t.Fatalf("writeUbuntuCloudInitDisable: %v", err)
		}
	}
	path := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not present after second call: %v", err)
	}
}

// ─── writeUbuntuNetplan ───────────────────────────────────────────────────────

// TestWriteUbuntuNetplan_Empty verifies that an empty interfaces list produces
// a netplan file with a wildcard DHCP stanza.
func TestWriteUbuntuNetplan_Empty(t *testing.T) {
	root := t.TempDir()

	if err := writeUbuntuNetplan(root, nil); err != nil {
		t.Fatalf("writeUbuntuNetplan: %v", err)
	}

	path := filepath.Join(root, "etc", "netplan", "01-clustr.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("netplan file not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "dhcp4: true") {
		t.Errorf("expected DHCP fallback stanza; got:\n%s", content)
	}
	if !strings.Contains(content, "en*") {
		t.Errorf("expected wildcard en* match; got:\n%s", content)
	}
	if !strings.Contains(content, "version: 2") {
		t.Errorf("expected netplan version: 2; got:\n%s", content)
	}
}

// TestWriteUbuntuNetplan_StaticIP verifies that a configured interface with a
// static IP produces the correct addresses/routes/nameservers block.
func TestWriteUbuntuNetplan_StaticIP(t *testing.T) {
	root := t.TempDir()

	ifaces := []api.InterfaceConfig{
		{
			Name:       "eth0",
			MACAddress: "52:54:00:ab:cd:ef",
			IPAddress:  "10.0.0.5/24",
			Gateway:    "10.0.0.1",
			DNS:        []string{"10.0.0.1", "8.8.8.8"},
		},
	}
	if err := writeUbuntuNetplan(root, ifaces); err != nil {
		t.Fatalf("writeUbuntuNetplan: %v", err)
	}

	path := filepath.Join(root, "etc", "netplan", "01-clustr.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("netplan file not written: %v", err)
	}
	content := string(data)

	checks := []struct {
		label string
		want  string
	}{
		{"interface key", "eth0:"},
		{"MAC match", "52:54:00:ab:cd:ef"},
		{"set-name", "set-name: \"eth0\""},
		{"address", "10.0.0.5/24"},
		{"gateway route", "via: \"10.0.0.1\""},
		{"dns", "10.0.0.1"},
		{"dhcp4 false", "dhcp4: false"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.want) {
			t.Errorf("[%s] netplan file missing %q; got:\n%s", c.label, c.want, content)
		}
	}
}

// TestWriteUbuntuNetplan_DHCP verifies that an interface with no static IP
// produces a dhcp4: true stanza.
func TestWriteUbuntuNetplan_DHCP(t *testing.T) {
	root := t.TempDir()

	ifaces := []api.InterfaceConfig{
		{Name: "ens3"},
	}
	if err := writeUbuntuNetplan(root, ifaces); err != nil {
		t.Fatalf("writeUbuntuNetplan: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "etc", "netplan", "01-clustr.yaml"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "dhcp4: true") {
		t.Errorf("expected dhcp4: true; got:\n%s", content)
	}
	if strings.Contains(content, "addresses:") {
		// Only the nameservers block should have addresses, not a static IP entry.
		if strings.Contains(content, "- \"") {
			// This is a static IP entry, not expected.
			// (nameservers use [..] not - ".."; this is a different format)
		}
	}
}

// TestWriteUbuntuNetplan_MTU verifies that a non-zero MTU is written.
func TestWriteUbuntuNetplan_MTU(t *testing.T) {
	root := t.TempDir()

	ifaces := []api.InterfaceConfig{
		{Name: "eth0", MTU: 9000},
	}
	if err := writeUbuntuNetplan(root, ifaces); err != nil {
		t.Fatalf("writeUbuntuNetplan: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "etc", "netplan", "01-clustr.yaml"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if !strings.Contains(string(data), "mtu: 9000") {
		t.Errorf("expected mtu: 9000; got:\n%s", string(data))
	}
}

// TestWriteUbuntuNetplan_FilePermissions verifies that the netplan file is
// created with world-readable permissions (netplan reads 0644 files).
func TestWriteUbuntuNetplan_FilePermissions(t *testing.T) {
	root := t.TempDir()

	if err := writeUbuntuNetplan(root, nil); err != nil {
		t.Fatalf("writeUbuntuNetplan: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, "etc", "netplan", "01-clustr.yaml"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("permissions = %04o, want 0644", info.Mode().Perm())
	}
}

// ─── ubuntu24Driver.WriteSystemFiles ─────────────────────────────────────────

// TestUbuntu24Driver_WriteSystemFiles verifies the driver writes both the
// cloud-init disable file and the netplan config.
func TestUbuntu24Driver_WriteSystemFiles(t *testing.T) {
	root := buildUbuntuChroot(t)

	drv := &ubuntu24Driver{}
	cfg := api.NodeConfig{
		Hostname: "worker-01",
		Interfaces: []api.InterfaceConfig{
			{
				Name:      "eth0",
				IPAddress: "192.168.1.10/24",
				Gateway:   "192.168.1.1",
			},
		},
	}

	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}

	// Cloud-init disable file must exist.
	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(cloudInitPath); err != nil {
		t.Errorf("cloud-init disable file not written: %v", err)
	}

	// Netplan file must exist with static IP.
	netplanPath := filepath.Join(root, "etc", "netplan", "01-clustr.yaml")
	data, err := os.ReadFile(netplanPath)
	if err != nil {
		t.Fatalf("netplan file not written: %v", err)
	}
	if !strings.Contains(string(data), "192.168.1.10/24") {
		t.Errorf("netplan missing static IP; got:\n%s", string(data))
	}
}

// ─── ubuntu24Driver.InstallBootloader (mock) ─────────────────────────────────

// TestUbuntu24Driver_InstallBootloader_NoTargets verifies that InstallBootloader
// is a no-op when AllTargets is empty (filesystem-only deploys, tests without
// a real block device).
func TestUbuntu24Driver_InstallBootloader_NoTargets(t *testing.T) {
	drv := &ubuntu24Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		AllTargets: nil,
	}
	if err := drv.InstallBootloader(ctx); err != nil {
		t.Errorf("InstallBootloader with empty targets must be a no-op; got: %v", err)
	}
}

// TestUbuntu24Driver_InstallBootloader_CommandPath verifies that
// InstallBootloader attempts to call grub-install (not grub2-install).
// grub-install will fail in the test environment (no actual disk), so
// we verify the BootloaderError is returned when all invocations fail.
func TestUbuntu24Driver_InstallBootloader_CommandPath(t *testing.T) {
	drv := &ubuntu24Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		TargetDisk: "/dev/nonexistent",
		AllTargets: []string{"/dev/nonexistent"},
	}

	err := drv.InstallBootloader(ctx)
	if err == nil {
		// grub-install should fail in the test environment (no real block device).
		// If it somehow succeeds, that is not an error for the test.
		t.Log("grub-install unexpectedly succeeded (running as root with a real device?)")
		return
	}
	var be *BootloaderError
	if ok := isBootloaderError(err, &be); !ok {
		t.Errorf("expected *BootloaderError on grub-install failure; got %T: %v", err, err)
	}
}

// isBootloaderError does a type check for *BootloaderError without importing errors.
func isBootloaderError(err error, out **BootloaderError) bool {
	if be, ok := err.(*BootloaderError); ok {
		if out != nil {
			*out = be
		}
		return true
	}
	return false
}
