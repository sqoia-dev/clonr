package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── detection fixtures ───────────────────────────────────────────────────────

func buildSLES15Chroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "etc/sysconfig", "etc/sysconfig/network"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// SLES 15 does NOT have /etc/debian_version or /etc/redhat-release.
	// Detection falls through to the generic os-release path.
	osRelease := `NAME="SLES"
VERSION="15-SP5"
VERSION_ID="15.5"
PRETTY_NAME="SUSE Linux Enterprise Server 15 SP5"
ID="sles"
ID_LIKE="suse"
ANSI_COLOR="0;32"
CPE_NAME="cpe:/o:suse:sles:15:sp5"
`
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(osRelease), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	return root
}

// buildSLESForSAPChroot simulates SLES for SAP which sets ID=sles_sap and
// ID_LIKE=sles.
func buildSLESForSAPChroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	osRelease := `NAME="SLES_SAP"
VERSION_ID="15.4"
ID="sles_sap"
ID_LIKE="sles"
`
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(osRelease), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	return root
}

// ─── detectDistro — SLES ─────────────────────────────────────────────────────

func TestDetectDistro_SLES15(t *testing.T) {
	root := buildSLES15Chroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "sles" || got.Major != 15 {
		t.Errorf("got %+v, want {sles 15}", got)
	}
}

func TestDetectDistro_SLES_ForSAP_IDLike(t *testing.T) {
	root := buildSLESForSAPChroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "sles" || got.Major != 15 {
		t.Errorf("got %+v, want {sles 15}", got)
	}
}

// ─── sles15Driver.Distro ──────────────────────────────────────────────────────

func TestSLES15Driver_Distro(t *testing.T) {
	var d sles15Driver
	got := d.Distro()
	if got.Family != "sles" || got.Major != 15 {
		t.Errorf("Distro() = %+v, want {sles 15}", got)
	}
}

// ─── writeSLESWickedConfig ────────────────────────────────────────────────────

func TestWriteSLESWickedConfig_Empty(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "sysconfig", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSLESWickedConfig(root, nil); err != nil {
		t.Fatalf("writeSLESWickedConfig: %v", err)
	}
	// Fallback eth0 DHCP config must exist.
	data, err := os.ReadFile(filepath.Join(root, "etc", "sysconfig", "network", "ifcfg-eth0"))
	if err != nil {
		t.Fatalf("ifcfg-eth0 not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "BOOTPROTO='dhcp'") {
		t.Errorf("expected DHCP bootproto; got:\n%s", content)
	}
	if !strings.Contains(content, "STARTMODE='auto'") {
		t.Errorf("expected STARTMODE=auto; got:\n%s", content)
	}
}

func TestWriteSLESWickedConfig_StaticIP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "sysconfig", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	ifaces := []api.InterfaceConfig{
		{
			Name:      "eth0",
			IPAddress: "10.0.0.15/24",
			Gateway:   "10.0.0.1",
			DNS:       []string{"10.0.0.1", "8.8.8.8"},
		},
	}
	if err := writeSLESWickedConfig(root, ifaces); err != nil {
		t.Fatalf("writeSLESWickedConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "etc", "sysconfig", "network", "ifcfg-eth0"))
	if err != nil {
		t.Fatalf("ifcfg-eth0 not written: %v", err)
	}
	content := string(data)
	checks := []struct{ label, want string }{
		{"BOOTPROTO static", "BOOTPROTO='static'"},
		{"IPADDR", "IPADDR='10.0.0.15/24'"},
		{"DNS", "NETCONFIG_DNS_STATIC_SERVERS='10.0.0.1 8.8.8.8'"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.want) {
			t.Errorf("[%s] missing %q; got:\n%s", c.label, c.want, content)
		}
	}

	// ifroute file for default gateway.
	route, err := os.ReadFile(filepath.Join(root, "etc", "sysconfig", "network", "ifroute-eth0"))
	if err != nil {
		t.Fatalf("ifroute-eth0 not written: %v", err)
	}
	if !strings.Contains(string(route), "default 10.0.0.1") {
		t.Errorf("expected default route; got:\n%s", string(route))
	}
}

func TestWriteSLESWickedConfig_MTU(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "sysconfig", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	ifaces := []api.InterfaceConfig{{Name: "eth0", MTU: 9000}}
	if err := writeSLESWickedConfig(root, ifaces); err != nil {
		t.Fatalf("writeSLESWickedConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "sysconfig", "network", "ifcfg-eth0"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "MTU='9000'") {
		t.Errorf("expected MTU=9000; got:\n%s", string(data))
	}
}

// TestWriteSLESWickedConfig_FilePermissions verifies ifcfg files are 0600
// (network credentials should not be world-readable).
func TestWriteSLESWickedConfig_FilePermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc", "sysconfig", "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSLESWickedConfig(root, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "etc", "sysconfig", "network", "ifcfg-eth0"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("ifcfg permissions = %04o, want 0600", info.Mode().Perm())
	}
}

// ─── writeSLESResolvConf ──────────────────────────────────────────────────────

func TestWriteSLESResolvConf(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeSLESResolvConf(root); err != nil {
		t.Fatalf("writeSLESResolvConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("resolv.conf not written: %v", err)
	}
	if !strings.Contains(string(data), "nameserver") {
		t.Errorf("resolv.conf missing nameserver lines; got:\n%s", string(data))
	}
}

// ─── sles15Driver.WriteSystemFiles ───────────────────────────────────────────

func TestSLES15Driver_WriteSystemFiles_NoCloud(t *testing.T) {
	root := buildSLES15Chroot(t)
	drv := sles15Driver{}
	cfg := api.NodeConfig{
		Hostname: "node-sles15",
		Interfaces: []api.InterfaceConfig{
			{Name: "eth0", IPAddress: "10.0.0.15/24", Gateway: "10.0.0.1"},
		},
	}
	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}

	// ifcfg-eth0 must exist.
	if _, err := os.Stat(filepath.Join(root, "etc", "sysconfig", "network", "ifcfg-eth0")); err != nil {
		t.Errorf("ifcfg-eth0 not written: %v", err)
	}
	// resolv.conf must exist.
	if _, err := os.Stat(filepath.Join(root, "etc", "resolv.conf")); err != nil {
		t.Errorf("resolv.conf not written: %v", err)
	}
	// cloud-init disable must NOT exist (no /etc/cloud/ in this fixture).
	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(cloudInitPath); err == nil {
		t.Error("cloud-init disable file written unexpectedly when /etc/cloud absent")
	}
}

func TestSLES15Driver_WriteSystemFiles_WithCloud(t *testing.T) {
	root := buildSLES15Chroot(t)
	if err := os.MkdirAll(filepath.Join(root, "etc", "cloud", "cloud.cfg.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv := sles15Driver{}
	if err := drv.WriteSystemFiles(root, api.NodeConfig{Hostname: "cloud-sles"}); err != nil {
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

// ─── sles15Driver.InstallBootloader (unit — no real disk) ────────────────────

func TestSLES15Driver_InstallBootloader_NoTargets(t *testing.T) {
	drv := sles15Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		AllTargets: nil,
	}
	if err := drv.InstallBootloader(ctx); err != nil {
		t.Errorf("no targets must be no-op; got: %v", err)
	}
}

func TestSLES15Driver_InstallBootloader_BIOS_BootloaderError(t *testing.T) {
	drv := sles15Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(),
		TargetDisk: "/dev/nonexistent",
		AllTargets: []string{"/dev/nonexistent"},
		IsEFI:      false,
	}
	err := drv.InstallBootloader(ctx)
	if err == nil {
		t.Log("grub2-install unexpectedly succeeded")
		return
	}
	var be *BootloaderError
	if !isBootloaderError(err, &be) {
		t.Errorf("expected *BootloaderError; got %T: %v", err, err)
	}
}

func TestSLES15Driver_InstallBootloader_EFI_NoESP(t *testing.T) {
	drv := sles15Driver{}
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
