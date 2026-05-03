package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── detection fixture ────────────────────────────────────────────────────────

func buildDebian12Chroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "etc/network"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// Debian always has /etc/debian_version.
	if err := os.WriteFile(filepath.Join(root, "etc", "debian_version"),
		[]byte("12.5\n"), 0o644); err != nil {
		t.Fatalf("write debian_version: %v", err)
	}
	// os-release with ID=debian, VERSION_ID="12".
	osRelease := `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
HOME_URL="https://www.debian.org/"
`
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(osRelease), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	return root
}

// ─── detectDistro — Debian 12 ────────────────────────────────────────────────

func TestDetectDistro_Debian12(t *testing.T) {
	root := buildDebian12Chroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "debian" || got.Major != 12 {
		t.Errorf("got %+v, want {debian 12}", got)
	}
}

// ─── debian12Driver.Distro ────────────────────────────────────────────────────

func TestDebian12Driver_Distro(t *testing.T) {
	var d debian12Driver
	got := d.Distro()
	if got.Family != "debian" || got.Major != 12 {
		t.Errorf("Distro() = %+v, want {debian 12}", got)
	}
}

// ─── writeDebianInterfaces ────────────────────────────────────────────────────

func TestWriteDebianInterfaces_Empty(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDebianInterfaces(root, nil); err != nil {
		t.Fatalf("writeDebianInterfaces: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "network", "interfaces"))
	if err != nil {
		t.Fatalf("interfaces file not written: %v", err)
	}
	content := string(data)
	// Must contain loopback and a DHCP stanza for eth0.
	if !strings.Contains(content, "iface lo inet loopback") {
		t.Errorf("missing loopback stanza; got:\n%s", content)
	}
	if !strings.Contains(content, "iface eth0 inet dhcp") {
		t.Errorf("missing DHCP fallback for eth0; got:\n%s", content)
	}
}

func TestWriteDebianInterfaces_StaticIP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	ifaces := []api.InterfaceConfig{
		{
			Name:       "eth0",
			IPAddress:  "10.0.0.12/24",
			Gateway:    "10.0.0.1",
			DNS:        []string{"10.0.0.1"},
		},
	}
	if err := writeDebianInterfaces(root, ifaces); err != nil {
		t.Fatalf("writeDebianInterfaces: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "network", "interfaces"))
	if err != nil {
		t.Fatalf("interfaces file not written: %v", err)
	}
	content := string(data)

	checks := []struct {
		label, want string
	}{
		{"inet static", "iface eth0 inet static"},
		{"address", "address 10.0.0.12"},
		{"netmask", "netmask 255.255.255.0"},
		{"gateway", "gateway 10.0.0.1"},
		{"dns", "dns-nameservers 10.0.0.1"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.want) {
			t.Errorf("[%s] missing %q; got:\n%s", c.label, c.want, content)
		}
	}
}

func TestWriteDebianInterfaces_DHCP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	ifaces := []api.InterfaceConfig{{Name: "ens3"}}
	if err := writeDebianInterfaces(root, ifaces); err != nil {
		t.Fatalf("writeDebianInterfaces: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "network", "interfaces"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "iface ens3 inet dhcp") {
		t.Errorf("expected DHCP stanza for ens3; got:\n%s", content)
	}
}

// ─── prefixLenToMask ─────────────────────────────────────────────────────────

func TestPrefixLenToMask(t *testing.T) {
	cases := []struct {
		prefix string
		want   string
	}{
		{"24", "255.255.255.0"},
		{"16", "255.255.0.0"},
		{"8", "255.0.0.0"},
		{"32", "255.255.255.255"},
		{"0", "0.0.0.0"},
		{"", "0.0.0.0"},        // parseMajorVersion("") = 0 → /0 prefix → all zeros
	}
	for _, c := range cases {
		got := prefixLenToMask(c.prefix)
		if got != c.want {
			t.Errorf("prefixLenToMask(%q) = %q, want %q", c.prefix, got, c.want)
		}
	}
}

// ─── writeDebianResolvConf ────────────────────────────────────────────────────

func TestWriteDebianResolvConf(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDebianResolvConf(root); err != nil {
		t.Fatalf("writeDebianResolvConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("resolv.conf not written: %v", err)
	}
	if !strings.Contains(string(data), "nameserver") {
		t.Errorf("resolv.conf missing nameserver lines; got:\n%s", string(data))
	}
}

// ─── debian12Driver.WriteSystemFiles ─────────────────────────────────────────

func TestDebian12Driver_WriteSystemFiles_NoCloud(t *testing.T) {
	root := buildDebian12Chroot(t)
	drv := debian12Driver{}
	cfg := api.NodeConfig{
		Hostname: "node-deb12",
		Interfaces: []api.InterfaceConfig{
			{Name: "eth0", IPAddress: "192.168.1.20/24", Gateway: "192.168.1.1"},
		},
	}
	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}

	// /etc/network/interfaces must exist with static IP.
	data, err := os.ReadFile(filepath.Join(root, "etc", "network", "interfaces"))
	if err != nil {
		t.Fatalf("interfaces file not written: %v", err)
	}
	if !strings.Contains(string(data), "192.168.1.20") {
		t.Errorf("interfaces missing static IP; got:\n%s", string(data))
	}

	// resolv.conf must exist.
	if _, err := os.Stat(filepath.Join(root, "etc", "resolv.conf")); err != nil {
		t.Errorf("resolv.conf not written: %v", err)
	}

	// cloud-init disable file must NOT exist (no cloud dir in this fixture).
	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(cloudInitPath); err == nil {
		t.Error("cloud-init disable file written when cloud dir absent; unexpected")
	}
}

func TestDebian12Driver_WriteSystemFiles_WithCloud(t *testing.T) {
	root := buildDebian12Chroot(t)
	// Create /etc/cloud to simulate a cloud-init-enabled Debian image.
	if err := os.MkdirAll(filepath.Join(root, "etc", "cloud", "cloud.cfg.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv := debian12Driver{}
	cfg := api.NodeConfig{Hostname: "cloud-deb12"}
	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}

	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	data, err := os.ReadFile(cloudInitPath)
	if err != nil {
		t.Fatalf("cloud-init disable file not written: %v", err)
	}
	if !strings.Contains(string(data), "datasource_list: [None]") {
		t.Errorf("disable file missing datasource_list; got:\n%s", string(data))
	}
}

// ─── debian12Driver.InstallBootloader (unit — no real disk) ──────────────────

func TestDebian12Driver_InstallBootloader_NoTargets(t *testing.T) {
	drv := debian12Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		AllTargets: nil,
	}
	if err := drv.InstallBootloader(ctx); err != nil {
		t.Errorf("no targets must be no-op; got: %v", err)
	}
}

func TestDebian12Driver_InstallBootloader_BIOS_BootloaderError(t *testing.T) {
	drv := debian12Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		TargetDisk: "/dev/nonexistent",
		AllTargets: []string{"/dev/nonexistent"},
		IsEFI:      false,
	}
	err := drv.InstallBootloader(ctx)
	if err == nil {
		t.Log("grub-install unexpectedly succeeded")
		return
	}
	var be *BootloaderError
	if !isBootloaderError(err, &be) {
		t.Errorf("expected *BootloaderError; got %T: %v", err, err)
	}
}

func TestDebian12Driver_InstallBootloader_EFI_NoESP(t *testing.T) {
	drv := debian12Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		TargetDisk: "/dev/nonexistent",
		AllTargets: []string{"/dev/nonexistent"},
		IsEFI:      true,
	}
	err := drv.InstallBootloader(ctx)
	if err == nil {
		t.Fatal("expected error when ESP not mounted; got nil")
	}
}
