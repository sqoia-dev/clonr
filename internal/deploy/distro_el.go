package deploy

import (
	"context"
	"fmt"
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
// Bootloader: GRUB2 via grub2-install (BIOS path).
// EFI installation remains in FilesystemDeployer.Finalize in rsync.go and
// is not yet delegated through InstallBootloader. Sprint 26 target.
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

// InstallBootloader runs grub2-install on all BIOS target disks for EL nodes.
// When ctx.AllTargets is empty (e.g. for filesystem-only test paths) this is a no-op.
// EFI installation is not handled here; see FilesystemDeployer.Finalize.
func (e *elBase) InstallBootloader(ctx *bootloaderCtx) error {
	if len(ctx.AllTargets) == 0 {
		return nil
	}
	return installELGRUBBIOS(ctx)
}

// installELGRUBBIOS runs grub2-install for all targets in a BIOS/GPT layout.
// This consolidates the logic that was previously inline in rsync.go Finalize.
// Both this function and the rsync.go caller produce identical grub2-install
// invocations; rsync.go still calls grub2-install directly so there is no
// double-install. Once sprint 26 migrates the full bootloader path here,
// rsync.go will call InstallBootloader instead.
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
