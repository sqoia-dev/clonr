package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// dracutPortabilityArgs is the load-bearing argv used to regenerate the
// initramfs in-chroot after the OS image is in place but before the
// bootloader is committed.
//
// Sprint 33 DRACUT-REGEN. Each flag is here for a reason — do not drop one
// without understanding the failure it prevents:
//
//	-fv                — force overwrite + verbose. Verbose is the only
//	                     handle the operator gets when an initramfs build
//	                     silently omits a module; routing through runAndLog
//	                     surfaces it line-by-line in the deploy stream.
//	-N (--no-hostonly) — build a hardware-agnostic image. We are running
//	                     dracut from inside a PXE initramfs whose detected
//	                     hardware is the deploy-host's, NOT the target
//	                     node's. Tailoring the initramfs to deploy-host
//	                     hardware is exactly the bug class this sprint
//	                     kills (the captured-on-virtio / deployed-to-PERC
//	                     boot regression).
//	--lvmconf          — emit lvm.conf into the initramfs. Required for
//	                     LVM-on-root images; without it the initramfs has
//	                     no view of vg/lv naming and root mount fails at
//	                     "switch_root" with "/dev/mapper/<vg>-<lv> does
//	                     not exist".
//	--force-add mdraid — force the mdraid dracut module in even when the
//	                     deploy-host has no md devices. Captured-on-virtio
//	                     (no md) → deployed-to-RAID-controller (md root)
//	                     would otherwise miss this entirely.
//	--force-add lvm    — same logic for the lvm dracut module.
//
// We run dracut once per installed kernel version (discovered by
// listKernelVersionsForDracut) instead of using --regenerate-all so that
// progress reporting can checkpoint on each kver and the operator sees
// progress rather than a single monolithic 30-90s pause.
var dracutPortabilityArgs = []string{
	"-fv", "-N",
	"--lvmconf",
	"--force-add", "mdraid",
	"--force-add", "lvm",
}

// vmlinuzPrefix is the file-name prefix dracut and grub.cfg both use for
// kernels copied into /boot. listKernelVersionsForDracut splits on this
// prefix to extract the kernel version.
const vmlinuzPrefix = "vmlinuz-"

// listKernelVersionsForDracut enumerates installed kernel versions by
// scanning <mountRoot>/boot/vmlinuz-*. Returns the version strings in
// sorted order (sort makes test output deterministic and gives the
// operator a stable progress sequence on the deploy log).
//
// Returns an empty slice if /boot is empty or unreadable; the caller
// (runDracutInChroot) treats that as a non-fatal warning — boot will
// still proceed via whatever initramfs the image already shipped, but
// the cross-controller portability fix is lost for that deploy.
func listKernelVersionsForDracut(mountRoot string) ([]string, error) {
	bootDir := filepath.Join(mountRoot, "boot")
	entries, err := os.ReadDir(bootDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", bootDir, err)
	}
	var kvers []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, vmlinuzPrefix) {
			continue
		}
		kver := strings.TrimPrefix(name, vmlinuzPrefix)
		if kver == "" {
			continue
		}
		// Skip rescue kernels — dracut would happily build an initramfs
		// for "0-rescue-<machine-id>" too, but we delete the rescue BLS
		// entry later in applyBootConfig anyway, so the build is wasted
		// time. Match conservatively on the well-known prefix.
		if strings.HasPrefix(kver, "0-rescue-") {
			continue
		}
		kvers = append(kvers, kver)
	}
	sort.Strings(kvers)
	return kvers, nil
}

// dracutCmdForKernel builds the chroot+dracut argv for a single kernel
// version. Exposed as a top-level function so tests can assert the shape
// of the produced argv without spawning a process.
//
// The produced command is, conceptually:
//
//	chroot <mountRoot> dracut <portability flags> <initramfs path> <kver>
//
// The explicit initramfs output path (/boot/initramfs-<kver>.img) and
// trailing kver are what dracut needs in non-regenerate-all mode — without
// them, dracut tries to introspect the running kernel (i.e. the deploy
// host's kernel, which is not the target node's), produces the wrong
// initramfs name, and the bootloader cannot find it.
func dracutCmdForKernel(ctx context.Context, mountRoot, kver string) *exec.Cmd {
	args := []string{mountRoot, "dracut"}
	args = append(args, dracutPortabilityArgs...)
	// Output path (inside the chroot, hence /boot/...) and target kver.
	args = append(args, "/boot/initramfs-"+kver+".img", kver)
	return exec.CommandContext(ctx, "chroot", args...)
}

// runDracutInChroot is the per-deploy entry point invoked by applyBootConfig.
// It enumerates kernels in <mountRoot>/boot, builds an initramfs for each
// using dracutCmdForKernel, and streams progress through the existing
// runAndLog (which threads into the structured-log ProgressReporter so the
// v0.1.22 install_log heartbeat sees each kver's progress).
//
// Failures are logged and counted but not propagated. A failed dracut for
// kernel N does not block kernel N+1, and a node where dracut fails for
// every kernel is still booted on the image's pre-existing initramfs (the
// node may not boot on a different storage controller — that is the known
// failure mode this sprint targets — but a partial success is strictly
// better than aborting the entire deploy).
//
// Returns the number of kernels for which dracut succeeded. Zero means
// the deploy proceeds with whatever initramfs the image shipped.
func runDracutInChroot(ctx context.Context, mountRoot string, reportStep func(string)) int {
	log := logger()

	kvers, err := listKernelVersionsForDracut(mountRoot)
	if err != nil {
		log.Warn().Err(err).
			Msg("WARNING finalize/boot: could not enumerate kernels in /boot — skipping dracut regeneration")
		return 0
	}
	if len(kvers) == 0 {
		log.Warn().
			Msg("WARNING finalize/boot: no vmlinuz-* found in /boot — skipping dracut regeneration")
		return 0
	}

	log.Info().Strs("kvers", kvers).Int("count", len(kvers)).
		Msg("finalize/boot: regenerating initramfs per kernel via dracut (Sprint 33 DRACUT-REGEN)")

	successCount := 0
	for i, kver := range kvers {
		stepMsg := fmt.Sprintf("Rebuilding initramfs (%d/%d): %s", i+1, len(kvers), kver)
		if reportStep != nil {
			reportStep(stepMsg)
		}
		log.Info().Str("kver", kver).Int("step", i+1).Int("total", len(kvers)).
			Msg("finalize/boot: dracut: " + stepMsg)

		cmd := dracutCmdForKernel(ctx, mountRoot, kver)
		if err := runAndLog(ctx, "dracut/"+kver, cmd); err != nil {
			log.Warn().Err(err).Str("kver", kver).
				Msg("WARNING finalize/boot: dracut failed for kernel — initramfs may lack hardware drivers; node may not boot on a different storage controller")
			continue
		}
		successCount++
		log.Info().Str("kver", kver).Msg("finalize/boot: dracut complete for kernel")
	}

	log.Info().Int("succeeded", successCount).Int("total", len(kvers)).
		Msg("finalize/boot: dracut regeneration finished")
	return successCount
}
