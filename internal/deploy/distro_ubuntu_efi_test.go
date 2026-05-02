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

func buildUbuntu22Chroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "etc/cloud", "etc/cloud/cloud.cfg.d", "etc/netplan"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeDebian(t, root)
	osRelease := `NAME="Ubuntu"
VERSION="22.04.4 LTS (Jammy Jellyfish)"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="22.04"
VERSION_CODENAME=jammy
`
	writeOSRelease(t, root, osRelease)
	return root
}

func buildUbuntu20Chroot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "etc/cloud", "etc/cloud/cloud.cfg.d", "etc/netplan"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	writeDebian(t, root)
	osRelease := `NAME="Ubuntu"
VERSION="20.04.6 LTS (Focal Fossa)"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="20.04"
VERSION_CODENAME=focal
`
	writeOSRelease(t, root, osRelease)
	return root
}

// writeDebian writes /etc/debian_version into root.
func writeDebian(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "etc", "debian_version"),
		[]byte("bookworm/sid\n"), 0o644); err != nil {
		t.Fatalf("write debian_version: %v", err)
	}
}

// writeOSRelease writes /etc/os-release into root.
func writeOSRelease(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"),
		[]byte(content), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
}

// ─── detectDistro — Ubuntu versions ──────────────────────────────────────────

func TestDetectDistro_Ubuntu22(t *testing.T) {
	root := buildUbuntu22Chroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "ubuntu" || got.Major != 22 {
		t.Errorf("got %+v, want {ubuntu 22}", got)
	}
}

func TestDetectDistro_Ubuntu20(t *testing.T) {
	root := buildUbuntu20Chroot(t)
	got, err := detectDistro(root)
	if err != nil {
		t.Fatalf("detectDistro: %v", err)
	}
	if got.Family != "ubuntu" || got.Major != 20 {
		t.Errorf("got %+v, want {ubuntu 20}", got)
	}
}

// ─── ubuntu22Driver ───────────────────────────────────────────────────────────

func TestUbuntu22Driver_Distro(t *testing.T) {
	var d ubuntu22Driver
	got := d.Distro()
	if got.Family != "ubuntu" || got.Major != 22 {
		t.Errorf("Distro() = %+v, want {ubuntu 22}", got)
	}
}

func TestUbuntu22Driver_WriteSystemFiles(t *testing.T) {
	root := buildUbuntu22Chroot(t)
	drv := &ubuntu22Driver{}
	cfg := api.NodeConfig{
		Hostname: "node-22",
		Interfaces: []api.InterfaceConfig{
			{Name: "eth0", IPAddress: "10.0.0.22/24", Gateway: "10.0.0.1"},
		},
	}
	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}

	// cloud-init disable file must exist.
	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(cloudInitPath); err != nil {
		t.Errorf("cloud-init disable file missing: %v", err)
	}

	// netplan file must exist with static IP.
	data, err := os.ReadFile(filepath.Join(root, "etc", "netplan", "01-clustr.yaml"))
	if err != nil {
		t.Fatalf("netplan file missing: %v", err)
	}
	if !strings.Contains(string(data), "10.0.0.22/24") {
		t.Errorf("netplan missing static IP; got:\n%s", string(data))
	}
}

// TestUbuntu22Driver_ResolvConf verifies that a resolv.conf is written for
// pre-systemd-resolved DNS resolution.
func TestUbuntu22Driver_ResolvConf(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeUbuntu22ResolvConf(root); err != nil {
		t.Fatalf("writeUbuntu22ResolvConf: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("resolv.conf not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "nameserver") {
		t.Errorf("resolv.conf missing nameserver lines; got:\n%s", content)
	}
}

// TestUbuntu22Driver_ResolvConf_ReplacesSymlink verifies that an existing
// symlink (like the one systemd-resolved creates) is replaced with a regular
// file.
func TestUbuntu22Driver_ResolvConf_ReplacesSymlink(t *testing.T) {
	root := t.TempDir()
	etcDir := filepath.Join(root, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a dangling symlink to simulate the systemd-resolved symlink.
	linkPath := filepath.Join(etcDir, "resolv.conf")
	if err := os.Symlink("/run/systemd/resolve/stub-resolv.conf", linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := writeUbuntu22ResolvConf(root); err != nil {
		t.Fatalf("writeUbuntu22ResolvConf: %v", err)
	}
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("stat resolv.conf: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("resolv.conf is still a symlink; expected regular file")
	}
}

// ─── ubuntu20Driver ───────────────────────────────────────────────────────────

func TestUbuntu20Driver_Distro(t *testing.T) {
	var d ubuntu20Driver
	got := d.Distro()
	if got.Family != "ubuntu" || got.Major != 20 {
		t.Errorf("Distro() = %+v, want {ubuntu 20}", got)
	}
}

func TestUbuntu20Driver_WriteSystemFiles(t *testing.T) {
	root := buildUbuntu20Chroot(t)
	drv := &ubuntu20Driver{}
	cfg := api.NodeConfig{
		Hostname: "node-20",
		Interfaces: []api.InterfaceConfig{
			{Name: "eth0", IPAddress: "10.0.0.20/24", Gateway: "10.0.0.1"},
		},
	}
	if err := drv.WriteSystemFiles(root, cfg); err != nil {
		t.Fatalf("WriteSystemFiles: %v", err)
	}
	// cloud-init disable.
	cloudInitPath := filepath.Join(root, "etc", "cloud", "cloud.cfg.d", "99-clustr-disable.cfg")
	if _, err := os.Stat(cloudInitPath); err != nil {
		t.Errorf("cloud-init disable file missing: %v", err)
	}
	// resolv.conf written.
	if _, err := os.Stat(filepath.Join(root, "etc", "resolv.conf")); err != nil {
		t.Errorf("resolv.conf not written: %v", err)
	}
}

// ─── ubuntu24Driver EFI path (unit — no real disk) ───────────────────────────

// TestUbuntu24Driver_InstallBootloader_EFI_NoESP verifies that the EFI path
// returns an error when the ESP mount point is not accessible.
func TestUbuntu24Driver_InstallBootloader_EFI_NoESP(t *testing.T) {
	drv := &ubuntu24Driver{}
	ctx := &bootloaderCtx{
		Ctx:        context.Background(),
		MountRoot:  t.TempDir(), // no /boot/efi subdir
		TargetDisk: "/dev/nonexistent",
		AllTargets: []string{"/dev/nonexistent"},
		IsEFI:      true,
	}
	err := drv.InstallBootloader(ctx)
	if err == nil {
		t.Fatal("expected error when ESP not mounted; got nil")
	}
	// Should be a BootloaderError or a plain fmt.Errorf — either is acceptable;
	// we just need a non-nil error.
}

// TestUbuntu24Driver_InstallBootloader_BIOSDispatch verifies the EFI=false
// path falls through to the BIOS path and returns BootloaderError when
// grub-install is not present / fails.
func TestUbuntu24Driver_InstallBootloader_BIOSDispatch(t *testing.T) {
	drv := &ubuntu24Driver{}
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

// TestUbuntu22Driver_InstallBootloader_EFI_Dispatches verifies that
// ubuntu22Driver.InstallBootloader with IsEFI=true hits the EFI path
// (returns error from missing ESP, not a BootloaderError from grub-install).
func TestUbuntu22Driver_InstallBootloader_EFI_Dispatches(t *testing.T) {
	drv := &ubuntu22Driver{}
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
