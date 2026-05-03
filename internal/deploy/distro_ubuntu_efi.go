package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ubuntu22Driver targets Ubuntu 22.04 LTS (Jammy Jellyfish).
//
// Differences from ubuntu24:
//   - systemd-resolved is the default DNS stub resolver; netplan must point to
//     the systemd-resolved symlink (/run/systemd/resolve/stub-resolv.conf) rather
//     than writing /etc/resolv.conf directly.  The set-name: and renderer:
//     settings are identical.
//   - cloud-init 23.x ships with Ubuntu 22.04.  The 99-clustr-disable.cfg
//     "datasource_list: [None]" approach works identically.
//   - grub-install binary name and flags are the same as ubuntu24.
type ubuntu22Driver struct{ ubuntu24Driver }

func init() { RegisterDriver(&ubuntu22Driver{}) }

func (d *ubuntu22Driver) Distro() Distro { return Distro{Family: "ubuntu", Major: 22} }

// WriteSystemFiles overrides ubuntu24Driver to use the jammy-specific netplan
// template.  cloud-init disable is identical.
func (d *ubuntu22Driver) WriteSystemFiles(root string, cfg api.NodeConfig) error {
	if err := writeUbuntuCloudInitDisable(root); err != nil {
		return fmt.Errorf("ubuntu22 WriteSystemFiles: cloud-init disable: %w", err)
	}
	if err := writeUbuntu22Netplan(root, cfg.Interfaces); err != nil {
		return fmt.Errorf("ubuntu22 WriteSystemFiles: netplan: %w", err)
	}
	return nil
}

// writeUbuntu22Netplan generates /etc/netplan/01-clustr.yaml for Ubuntu 22.04.
// The only structural difference from ubuntu24 is that the renderer is
// "networkd" (same) but we emit a nameservers block that resolves correctly
// through systemd-resolved by letting networkd manage DNS — identical to
// ubuntu24.  The /etc/resolv.conf → /run/systemd/resolve/stub-resolv.conf
// symlink is created by systemd-resolved itself on first boot; netplan does
// not need to touch it.  We write the nameservers stanza in the netplan YAML
// so networkd passes the servers to systemd-resolved via D-Bus at link-up.
func writeUbuntu22Netplan(root string, ifaces []api.InterfaceConfig) error {
	// Create /run/systemd/resolve symlink so that if systemd-resolved is not
	// yet running on first boot, /etc/resolv.conf still resolves.  Write a
	// static resolv.conf pointing at the cluster DNS servers.
	if err := writeUbuntu22ResolvConf(root); err != nil {
		// Non-fatal: the node will still have DNS via netplan/resolved on first
		// boot; we just can't guarantee pre-first-boot name resolution.
		logger().Warn().Err(err).Msg("ubuntu22 WriteSystemFiles: resolv.conf symlink/write (non-fatal)")
	}
	return writeUbuntuNetplan(root, ifaces)
}

// writeUbuntu22ResolvConf writes /etc/resolv.conf as a regular file rather
// than a symlink pointing at systemd-resolved's stub, so that the node has
// working DNS immediately in the initrd phase before systemd-resolved is
// started.  systemd-resolved will replace this with its own symlink on first
// boot once the service is running.
func writeUbuntu22ResolvConf(root string) error {
	etcDir := filepath.Join(root, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("mkdir /etc: %w", err)
	}
	// Remove any existing symlink so os.WriteFile can create a regular file.
	target := filepath.Join(etcDir, "resolv.conf")
	_ = os.Remove(target)

	var sb strings.Builder
	sb.WriteString("# Written by clustr at deploy time.\n")
	sb.WriteString("# systemd-resolved replaces this on first boot.\n")
	for _, s := range clusterDNSServers() {
		sb.WriteString("nameserver " + s + "\n")
	}
	return os.WriteFile(target, []byte(sb.String()), 0o644)
}

// ubuntu20Driver targets Ubuntu 20.04 LTS (Focal Fossa).
//
// Differences from ubuntu24/22:
//   - Older cloud-init (19.x): the disable override file is in
//     /etc/cloud/cloud.cfg.d/ as "99-clustr-disable.cfg" — same path, same
//     "datasource_list: [None]" content — compatible with cloud-init 19.x.
//   - Older netplan (0.99 vs 0.103+): the schema is compatible; renderer
//     "networkd" and version 2 work on both.
//   - Same grub-install binary; same flags.
//   - systemd-resolved present (same as 22); same resolv.conf treatment.
type ubuntu20Driver struct{ ubuntu24Driver }

func init() { RegisterDriver(&ubuntu20Driver{}) }

func (d *ubuntu20Driver) Distro() Distro { return Distro{Family: "ubuntu", Major: 20} }

// WriteSystemFiles overrides ubuntu24Driver: uses the focal cloud-init disable
// path (same content, same path — but documents intent) and writes resolv.conf
// for pre-resolved compatibility.
func (d *ubuntu20Driver) WriteSystemFiles(root string, cfg api.NodeConfig) error {
	if err := writeUbuntuCloudInitDisable(root); err != nil {
		return fmt.Errorf("ubuntu20 WriteSystemFiles: cloud-init disable: %w", err)
	}
	// Focal ships cloud-init 19.x; the disable path is the same.
	// Write a static resolv.conf for pre-systemd-resolved name resolution.
	if err := writeUbuntu22ResolvConf(root); err != nil {
		logger().Warn().Err(err).Msg("ubuntu20 WriteSystemFiles: resolv.conf write (non-fatal)")
	}
	if err := writeUbuntuNetplan(root, cfg.Interfaces); err != nil {
		return fmt.Errorf("ubuntu20 WriteSystemFiles: netplan: %w", err)
	}
	return nil
}

// ─── ubuntu24 EFI path ────────────────────────────────────────────────────────

// installUbuntuGRUBEFI installs the UEFI bootloader for Ubuntu nodes.
// Ubuntu ships grub-install (not grub2-install); the EFI chroot procedure is
// otherwise identical to the EL path.
//
// It verifies that the ESP is mounted at <mountRoot>/boot/efi, runs
// grub-install --target=x86_64-efi inside a chroot (for module version
// consistency), and verifies that grubx64.efi was written.
//
// Ubuntu packages required in the image:
//   grub-efi-amd64  grub-efi-amd64-bin  shim-signed
func installUbuntuGRUBEFI(ctx *bootloaderCtx) error {
	log := logger()
	mountRoot := ctx.MountRoot
	efiDir := filepath.Join(mountRoot, "boot", "efi")

	if _, err := os.Stat(efiDir); err != nil {
		return fmt.Errorf("ubuntu InstallBootloader: UEFI ESP mount point %s not accessible: %w",
			efiDir, err)
	}

	goCtx := context.Background()
	if ctx.Ctx != nil {
		if c, ok := ctx.Ctx.(context.Context); ok {
			goCtx = c
		}
	}

	if err := runGrubInstallEFIInChrootUbuntu(goCtx, mountRoot); err != nil {
		return &BootloaderError{
			Targets: []string{ctx.TargetDisk},
			Cause:   fmt.Errorf("ubuntu UEFI: grub-install --target=x86_64-efi in chroot: %w", err),
		}
	}
	log.Info().Msg("ubuntu InstallBootloader: grub-install --target=x86_64-efi (chroot) succeeded")

	// Verify the vendor EFI binary is present.
	grubx64Path := filepath.Join(efiDir, "EFI", "ubuntu", "grubx64.efi")
	if _, err := os.Stat(grubx64Path); err != nil {
		return &BootloaderError{
			Targets: []string{ctx.TargetDisk},
			Cause: fmt.Errorf("ubuntu UEFI: grub-install exited 0 but %s is missing — "+
				"rebuild the image with grub-efi-amd64, grub-efi-amd64-bin, and shim-signed: %w",
				grubx64Path, err),
		}
	}
	log.Info().Str("path", grubx64Path).Msg("ubuntu InstallBootloader: grubx64.efi verified")
	return nil
}

// runGrubInstallEFIInChrootUbuntu runs grub-install --target=x86_64-efi inside
// the deployed Ubuntu chroot.  Ubuntu uses grub-install (not grub2-install),
// --bootloader-id=ubuntu (not rocky), and update-grub (not grub2-mkconfig).
//
// Bind-mounts /proc, /sys, /dev, /dev/pts into the chroot for the same reasons
// as the EL path (see runGrub2InstallEFIInChroot in finalize.go).
func runGrubInstallEFIInChrootUbuntu(ctx context.Context, mountRoot string) error {
	log := logger()

	type bindMount struct{ src, dst string }
	binds := []bindMount{
		{"/proc", filepath.Join(mountRoot, "proc")},
		{"/sys", filepath.Join(mountRoot, "sys")},
		{"/dev", filepath.Join(mountRoot, "dev")},
		{"/dev/pts", filepath.Join(mountRoot, "dev", "pts")},
	}
	for _, b := range binds {
		if err := os.MkdirAll(b.dst, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", b.dst, err)
		}
		if out, err := exec.CommandContext(ctx, "mount", "--bind", b.src, b.dst).CombinedOutput(); err != nil {
			return fmt.Errorf("bind-mount %s → %s: %w\n%s", b.src, b.dst, err, string(out))
		}
	}
	defer func() {
		for i := len(binds) - 1; i >= 0; i-- {
			_ = exec.Command("umount", "-l", binds[i].dst).Run()
		}
	}()

	devFD := filepath.Join(mountRoot, "dev", "fd")
	if _, err := os.Lstat(devFD); os.IsNotExist(err) {
		_ = os.Symlink("/proc/self/fd", devFD)
	}

	args := []string{
		mountRoot,
		"grub-install",
		"--target=x86_64-efi",
		"--efi-directory=/boot/efi",
		"--boot-directory=/boot",
		"--bootloader-id=ubuntu",
		"--removable",
		"--no-nvram",
		"--recheck",
		"--force",
	}
	log.Info().Strs("chroot_args", args[1:]).Msg("ubuntu: running grub-install --target=x86_64-efi inside chroot")
	if err := runAndLog(ctx, "grub-install-efi-chroot", exec.CommandContext(ctx, "chroot", args...)); err != nil {
		return fmt.Errorf("chroot grub-install --target=x86_64-efi: %w", err)
	}

	// Run update-grub to regenerate /boot/grub/grub.cfg.
	updateArgs := []string{mountRoot, "update-grub"}
	log.Info().Msg("ubuntu: running update-grub inside chroot")
	if err := runAndLog(ctx, "update-grub-chroot", exec.CommandContext(ctx, "chroot", updateArgs...)); err != nil {
		// update-grub failure is non-fatal: the EFI shim will still boot using
		// the grub.cfg written by grub-install --removable.
		log.Warn().Err(err).Msg("ubuntu: update-grub inside chroot failed (non-fatal)")
	}
	return nil
}
