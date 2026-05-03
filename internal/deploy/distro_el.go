package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// elBase is the shared implementation for all EL (Enterprise Linux) family
// drivers: el8, el9, el10.
//
// EL covers Rocky Linux, RHEL, AlmaLinux, and CentOS Stream.
//
// Network configuration: NetworkManager keyfiles under
//
//	/etc/NetworkManager/system-connections/
//
// Bootloader: GRUB2 via grub2-install. Handles both BIOS (i386-pc) and UEFI
// (x86_64-efi inside chroot) paths through InstallBootloader.
type elBase struct {
	major int
}

// WriteSystemFiles writes NetworkManager connection profiles for EL nodes.
// Delegates to the same helpers used by applyNodeConfig (writeNetworkConfig,
// writeClustrDHCPProfile) so behaviour is identical — no change in commit 1.
func (e *elBase) WriteSystemFiles(root string, cfg api.NodeConfig) error {
	if err := writeNetworkConfig(root, cfg.Interfaces); err != nil {
		return fmt.Errorf("el%d WriteSystemFiles: network config: %w", e.major, err)
	}
	if err := writeClustrDHCPProfile(root); err != nil {
		// Non-fatal — matches the non-fatal treatment in applyNodeConfig.
		logger().Warn().Err(err).
			Msgf("el%d WriteSystemFiles: could not write clustr-dhcp profile (non-fatal)", e.major)
	}
	return nil
}

// InstallBootloader installs the GRUB2 bootloader for EL nodes.
// Dispatches to the UEFI chroot path when ctx.IsEFI is true, otherwise runs
// grub2-install for each BIOS target disk.
// When AllTargets is empty and IsEFI is false, this is a no-op.
func (e *elBase) InstallBootloader(ctx *bootloaderCtx) error {
	if ctx.IsEFI {
		return installELGRUBEFI(ctx)
	}
	if len(ctx.AllTargets) == 0 {
		return nil
	}
	return installELGRUBBIOS(ctx)
}

// installELGRUBEFI installs the UEFI bootloader for EL nodes.
// It verifies that grubx64.efi is present in the deployed image (ADR-0009:
// content-only images must ship all boot dependencies), runs grub2-install
// --target=x86_64-efi inside the deployed chroot, and then verifies that
// BOOTX64.EFI was written (the load-bearing removable-media boot binary).
func installELGRUBEFI(ctx *bootloaderCtx) error {
	log := logger()
	mountRoot := ctx.MountRoot
	efiDir := filepath.Join(mountRoot, "boot", "efi")
	grubx64Path := filepath.Join(efiDir, "EFI", "rocky", "grubx64.efi")

	// Verify the ESP is mounted at <mountRoot>/boot/efi.
	if _, err := os.Stat(efiDir); err != nil {
		return fmt.Errorf("el InstallBootloader: UEFI ESP mount point %s not accessible: %w",
			efiDir, err)
	}

	// Fail fast if grubx64.efi is absent. The image must be rebuilt with
	// grub2-efi-x64, grub2-efi-x64-modules, and shim-x64 (ADR-0009).
	log.Info().Msg("  → Checking grubx64.efi presence in image...")
	if _, err := os.Stat(grubx64Path); os.IsNotExist(err) {
		return &BootloaderError{
			Targets: []string{ctx.TargetDisk},
			Cause: fmt.Errorf("UEFI: %s is missing from the deployed image — "+
				"rebuild the image with grub2-efi-x64, grub2-efi-x64-modules, and shim-x64 "+
				"in the kickstart %%packages section (ADR-0009: content-only images must ship all boot dependencies)",
				grubx64Path),
		}
	}
	log.Info().Str("path", grubx64Path).
		Msg("finalize: grubx64.efi present on ESP — proceeding with grub2-install")

	// Run grub2-install --target=x86_64-efi inside the deployed chroot.
	// Running inside the chroot ensures module version consistency.
	goCtx := context.Background()
	if ctx.Ctx != nil {
		if c, ok := ctx.Ctx.(context.Context); ok {
			goCtx = c
		}
	}
	if err := runGrub2InstallEFIInChroot(goCtx, mountRoot); err != nil {
		return &BootloaderError{
			Targets: []string{ctx.TargetDisk},
			Cause:   fmt.Errorf("UEFI: grub2-install --target=x86_64-efi in chroot: %w", err),
		}
	}
	log.Info().Msg("finalize: grub2-install --target=x86_64-efi (chroot) succeeded")

	// Verify BOOTX64.EFI exists — the load-bearing removable-media boot binary.
	bootx64Path := filepath.Join(efiDir, "EFI", "BOOT", "BOOTX64.EFI")
	if _, err := os.Stat(bootx64Path); err != nil {
		return &BootloaderError{
			Targets: []string{ctx.TargetDisk},
			Cause: fmt.Errorf("UEFI: grub2-install --removable exited 0 but %s is missing — "+
				"removable-media boot will fail: %w", bootx64Path, err),
		}
	}
	log.Info().Str("path", bootx64Path).Msg("  ✓ BOOTX64.EFI verified post-install (removable-media boot target)")

	// Soft check — \EFI\rocky\grubx64.efi is the RPM-shipped binary, not load-bearing.
	if _, err := os.Stat(grubx64Path); err != nil {
		log.Warn().Err(err).Str("path", grubx64Path).
			Msg("finalize: \\EFI\\rocky\\grubx64.efi missing (non-fatal — BOOTX64.EFI is load-bearing)")
	}
	log.Info().Msg("finalize: skipping NVRAM entry creation — relying on UEFI removable-media discovery of \\EFI\\BOOT\\BOOTX64.EFI (see docs/boot-architecture.md §8)")
	return nil
}

// installELGRUBBIOS runs grub2-install for all targets in a BIOS/GPT layout.
func installELGRUBBIOS(ctx *bootloaderCtx) error {
	log := logger()
	goCtx := context.Background()
	if ctx.Ctx != nil {
		if c, ok := ctx.Ctx.(context.Context); ok {
			goCtx = c
		}
	}

	bootDir := filepath.Join(ctx.MountRoot, "boot")
	var succeeded int
	var lastErr error

	for _, disk := range ctx.AllTargets {
		args := []string{
			"--target=i386-pc",
			"--boot-directory=" + bootDir,
			"--recheck",
		}
		if ctx.IsRAID {
			args = append(args, "--force")
		}
		if ctx.IsRAIDOnWholeDisk {
			args = append(args,
				"--skip-fs-probe",
				"--modules=mdraid1x diskfilter part_gpt xfs ext2",
			)
		}
		args = append(args, disk)

		cmd := exec.CommandContext(goCtx, "grub2-install", args...)
		if err := runAndLog(goCtx, "grub2-install", cmd); err != nil {
			lastErr = err
			log.Warn().Err(err).Str("disk", disk).
				Msg("el InstallBootloader: grub2-install failed on disk")
		} else {
			succeeded++
			log.Info().Str("disk", disk).Msg("el InstallBootloader: GRUB installed")
		}
	}

	if succeeded == 0 {
		return &BootloaderError{
			Targets: ctx.AllTargets,
			Cause:   lastErr,
		}
	}
	return nil
}
