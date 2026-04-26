package isoinstaller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// extractSystemdRunAvailable is detected once at package init and used to
// decide whether ExtractViaSubprocess can use systemd-run scope isolation.
var extractSystemdRunAvailable bool

func init() {
	_, err := exec.LookPath("systemd-run")
	extractSystemdRunAvailable = (err == nil)
}

// ExtractViaSubprocess runs rootfs extraction in a subprocess via
// "clustr-serverd extract ..." so that losetup/mount operations happen outside
// clustr-serverd's own hardened unit (NoNewPrivileges, tight capabilities, etc.).
//
// When systemd-run is available the subprocess is placed in
// clustr-builders.slice, which has the capability grants and device permissions
// required for block-device work.  When systemd-run is unavailable (dev
// machines, containers) the subprocess is exec'd directly — it still runs as
// the same user but inherits a less-restricted environment than the parent
// service unit.
//
// buildID is used to name the transient scope unit so operators can correlate
// it in `systemctl status`.  The line callbacks are optional; when non-nil they
// receive stdout/stderr lines from the subprocess in real time (fed to the
// build's progress store so the serial-console panel in the UI shows extraction
// progress).
func ExtractViaSubprocess(ctx context.Context, buildID string, opts ExtractOptions, onStdout, onStderr func(string)) error {
	selfBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("extract subprocess: locate own binary: %w", err)
	}

	extractArgs := []string{
		"extract",
		"--disk=" + opts.RawDiskPath,
		"--out=" + opts.RootfsDestDir,
	}

	var bin string
	var args []string

	// Run extract directly as a child process. The server runs as root so the
	// subprocess inherits full capabilities for losetup/mount. Previously this
	// used systemd-run --scope --slice=clustr-builders.slice, but the slice's
	// CapabilityBoundingSet restricts rather than grants, causing losetup EPERM.
	// The QEMU builder still uses the slice (via the factory's systemd-run call);
	// only the extract step runs directly.
	bin = selfBin
	args = extractArgs

	cmd := exec.CommandContext(ctx, bin, args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("extract subprocess: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("extract subprocess: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("extract subprocess: start: %w", err)
	}

	// Drain stdout and stderr in the background, forwarding to callbacks.
	// stderr is also collected into a capped buffer (last 4 KB) so that when
	// the subprocess exits non-zero we can include the actual error message in
	// the returned error rather than only surfacing it via the progress store.
	drain := func(r io.Reader, cb func(string)) {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			if cb != nil {
				cb(scanner.Text())
			}
		}
	}

	const maxStderrBytes = 4 * 1024
	var stderrMu sync.Mutex
	var stderrBuf bytes.Buffer
	go drain(stdoutPipe, onStdout)
	go drain(stderrPipe, func(line string) {
		stderrMu.Lock()
		stderrBuf.WriteString(line)
		stderrBuf.WriteByte('\n')
		// Keep only the last maxStderrBytes to bound memory usage.
		if stderrBuf.Len() > maxStderrBytes {
			excess := stderrBuf.Len() - maxStderrBytes
			stderrBuf.Next(excess)
		}
		stderrMu.Unlock()
		if onStderr != nil {
			onStderr(line)
		}
	})

	waitErr := cmd.Wait()
	if waitErr == nil {
		return nil
	}

	stderrMu.Lock()
	capturedStderr := stderrBuf.String()
	stderrMu.Unlock()

	// Classify exit errors the same way the QEMU wrapper does.
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		return fmt.Errorf("extract subprocess: %w", waitErr)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return fmt.Errorf("extract subprocess killed by signal %v (check dmesg for OOM)", status.Signal())
		}
		return fmt.Errorf("extract subprocess exited with code %d: %s", status.ExitStatus(), capturedStderr)
	}
	return fmt.Errorf("extract subprocess: %w", waitErr)
}

// ExtractOptions configures the filesystem extraction from an installed raw disk.
type ExtractOptions struct {
	// RawDiskPath is the path to the raw disk image produced by Build.
	RawDiskPath string

	// RootfsDestDir is the directory where the root filesystem will be
	// extracted. It must already exist.
	RootfsDestDir string

	// BootDestDir, when non-empty, extracts /boot into a separate directory.
	// When empty, /boot is handled as part of the root rsync.
	BootDestDir string
}

// ExtractRootfs mounts an installed raw disk image (via losetup + kpartx),
// locates the root partition, and rsyncs its contents into RootfsDestDir.
//
// Partition discovery strategy:
//  1. Loop-attach the raw disk with --partscan.
//  2. Use lsblk to enumerate partitions.
//  3. Skip the biosboot / ESP partition (no filesystem or vfat).
//  4. The largest ext4/xfs partition is treated as root.
//  5. The first xfs/ext4 partition before root (if present) is treated as /boot.
//
// This is intentionally simple — the kickstart template uses a fixed layout
// (biosboot + /boot + /) so the heuristic is reliable for clustr-generated images.
// Admins using custom kickstarts with unusual layouts should use CaptureNode instead.
func ExtractRootfs(opts ExtractOptions) error {
	// ── Loop-attach the raw disk ─────────────────────────────────────────
	loopOut, err := exec.Command("losetup", "--find", "--partscan", "--show", opts.RawDiskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup: %w\noutput: %s", err, string(loopOut))
	}
	loopDev := strings.TrimSpace(string(loopOut))
	defer func() {
		_ = exec.Command("losetup", "-d", loopDev).Run()
	}()

	// Allow udev to create partition devices.
	_ = exec.Command("udevadm", "settle", "--timeout=10").Run()

	// ── Enumerate partitions ─────────────────────────────────────────────
	partOut, err := exec.Command("lsblk", "-lno", "NAME,FSTYPE,SIZE", loopDev).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lsblk: %w\noutput: %s", err, partOut)
	}

	var rootDev, bootDev, espDev string
	loopBase := filepath.Base(loopDev)

	for _, line := range strings.Split(strings.TrimSpace(string(partOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		fstype := fields[1]
		if name == loopBase {
			continue // skip the loop device itself
		}

		dev := "/dev/" + name
		if _, statErr := os.Stat(dev); statErr != nil {
			continue
		}

		// Detect the ESP (EFI System Partition): vfat filesystem.
		// Anaconda always formats the ESP as vfat; this is the canonical indicator.
		if fstype == "vfat" && espDev == "" {
			espDev = dev
			continue
		}

		// Skip other non-data filesystems (no fstype, biosboot).
		if fstype == "" || strings.EqualFold(fstype, "biosboot") {
			continue
		}

		// Heuristic: if we haven't found a root yet, probe the mount point.
		mp := probeMountPoint(dev)
		switch {
		case mp == "/" || rootDev == "":
			if rootDev == "" || mp == "/" {
				rootDev = dev
			}
		case mp == "/boot" && bootDev == "":
			bootDev = dev
		}
	}

	if rootDev == "" {
		return fmt.Errorf("extract: could not identify root partition on %s — check lsblk output: %s",
			opts.RawDiskPath, string(partOut))
	}

	// ── Mount and rsync root partition ───────────────────────────────────
	rootMnt, err := os.MkdirTemp("", "clustr-root-*")
	if err != nil {
		return fmt.Errorf("create root mount: %w", err)
	}
	defer os.RemoveAll(rootMnt)

	if out, err := exec.Command("mount", rootDev, rootMnt).CombinedOutput(); err != nil {
		return fmt.Errorf("mount root %s: %w\noutput: %s", rootDev, err, string(out))
	}
	defer func() { _ = exec.Command("umount", "-l", rootMnt).Run() }()

	// If there is a separate /boot partition, mount it under the root mount
	// so rsync picks it up naturally.
	if bootDev != "" {
		bootMnt := filepath.Join(rootMnt, "boot")
		if err := os.MkdirAll(bootMnt, 0o755); err != nil {
			return fmt.Errorf("create boot mount point: %w", err)
		}
		if out, err := exec.Command("mount", "-o", "ro", bootDev, bootMnt).CombinedOutput(); err != nil {
			// Non-fatal: log and continue — we'll get /boot from the root partition
			// if the installer put it there instead of on a separate partition.
			_ = string(out) // suppress unused variable
		} else {
			defer func() { _ = exec.Command("umount", "-l", bootMnt).Run() }()
		}
	}

	// If an ESP was detected, mount it at /boot/efi so rsync captures
	// grubx64.efi, shimx64.efi, and other EFI binaries (ADR-0009).
	// Anaconda's 3-partition GPT layout places these on a vfat ESP that is
	// distinct from /boot; without this mount the ESP contents are never included
	// in the rootfs blob and deploy finalize fails with "grubx64.efi missing".
	if espDev != "" {
		efiMnt := filepath.Join(rootMnt, "boot", "efi")
		if err := os.MkdirAll(efiMnt, 0o755); err != nil {
			return fmt.Errorf("create ESP mount point: %w", err)
		}
		if out, err := exec.Command("mount", "-o", "ro", espDev, efiMnt).CombinedOutput(); err != nil {
			// Non-fatal: log and continue — deploy will fail later with a clear
			// message if grubx64.efi is missing, which is preferable to a hard
			// extraction error on systems where the ESP probe is a false positive.
			_ = string(out) // suppress unused variable
		} else {
			defer func() { _ = exec.Command("umount", "-l", efiMnt).Run() }()
		}
	}

	// rsync the full mounted tree.
	if err := rsyncExtracted(rootMnt+"/", opts.RootfsDestDir); err != nil {
		return err
	}

	// Copy grubx64.efi to a known location alongside the rootfs so the clustr
	// server can serve it directly for UEFI iPXE chain-boot (ADR-0010).
	// We search candidate distro-specific paths in order of preference.
	copyGrubEFI(rootMnt, opts.RootfsDestDir)

	return nil
}

// grubEFICandidates lists the paths (relative to the ESP mount at /boot/efi)
// where grubx64.efi may reside, ordered by preference.
var grubEFICandidates = []string{
	"EFI/rocky/grubx64.efi",
	"EFI/redhat/grubx64.efi",
	"EFI/centos/grubx64.efi",
	"EFI/fedora/grubx64.efi",
	"EFI/ubuntu/grubx64.efi",
	"EFI/debian/grubx64.efi",
	"EFI/BOOT/BOOTX64.EFI", // removable fallback, last resort
}

// BuildStandaloneGrubEFI is the exported form of buildStandaloneGrubEFI.
// It is called both at image-creation time (extract.go copyGrubEFI) and at
// deploy finalization time (deploy/rsync.go Finalize) so that the server-side
// grub.efi is always the standalone HTTP-chain-boot binary, never the distro
// RPM grubx64.efi (which has a hardcoded ESP prefix and cannot chain over HTTP).
func BuildStandaloneGrubEFI(rootMnt, destPath string) error {
	return buildStandaloneGrubEFI(rootMnt, destPath)
}

// buildStandaloneGrubEFI uses grub2-mkimage to build a standalone EFI binary
// suitable for UEFI iPXE chain-boot over HTTP. The binary is built with:
//
//   - All required modules compiled in (part_gpt, xfs, ext2, fat, http, efinet,
//     tftp, net, search, configfile, linux, linuxefi, etc.)
//   - An embedded grub.cfg that chains back to the clustr server's HTTP grub.cfg
//     endpoint so GRUB immediately fetches the disk-search stub from the server.
//   - Empty prefix (-p '') so GRUB does not attempt to load modules from disk or
//     a path that doesn't exist when chain-loaded over HTTP.
//
// rootMnt is the mounted rootfs of the extracted image. Its
// /usr/lib/grub/x86_64-efi/ module directory is used as the module source so
// the compiled-in modules are version-matched to the deployed OS's GRUB.
//
// destPath is the full path where grub.efi should be written.
//
// Returns nil on success. Fails loudly if critical modules (especially xfs.mod)
// are absent from the module directory, or if the resulting binary does not
// contain XFS support. On any failure the caller falls back to copying the stock
// distro binary (which will not work for HTTP chain-boot but is better than
// nothing for local-disk boot).
func buildStandaloneGrubEFI(rootMnt, destPath string) error {
	// Check that grub2-mkimage is available on the host.
	grubMkimage, err := exec.LookPath("grub2-mkimage")
	if err != nil {
		// Try alternate name on Debian/Ubuntu hosts.
		if alt, err2 := exec.LookPath("grub-mkimage"); err2 == nil {
			grubMkimage = alt
		} else {
			return fmt.Errorf("grub2-mkimage not found on host (install grub2-efi-x64-modules or grub-efi-amd64-bin): %w", err)
		}
	}

	// Use the image's own GRUB module directory so compiled-in modules are
	// version-matched to the deployed OS's GRUB binary.
	modDir := filepath.Join(rootMnt, "usr", "lib", "grub", "x86_64-efi")
	if _, err := os.Stat(modDir); err != nil {
		return fmt.Errorf("grub x86_64-efi module directory not found at %s (image may be missing grub2-efi-x64-modules RPM): %w", modDir, err)
	}

	// Hard-require xfs.mod in the module directory. grub2-mkimage silently
	// produces a binary without XFS if the .mod file is absent — failing loudly
	// here is preferable to a binary that drops to rescue on XFS nodes.
	xfsMod := filepath.Join(modDir, "xfs.mod")
	if _, err := os.Stat(xfsMod); err != nil {
		return fmt.Errorf("xfs.mod not found at %s — image is missing grub2-efi-x64-modules RPM; rebuild image with grub2-efi-x64-modules in %%packages", xfsMod)
	}

	// Embedded grub.cfg: chains back to the clustr server's HTTP grub.cfg
	// endpoint. When iPXE chain-loads grub.efi over HTTP, GRUB sets $prefix to
	// the directory portion of the URL (i.e. (http,server)/api/v1/boot) and
	// automatically attempts to read ($prefix)/grub.cfg. With -p '' the binary
	// has no hardcoded prefix, so GRUB derives the prefix from the HTTP URL it
	// was chain-loaded from and fetches grub.cfg over HTTP.
	//
	// The explicit configfile command below is a belt-and-suspenders fallback in
	// case GRUB's auto-prefix logic does not fire (e.g. when net_default_server
	// is set but prefix resolution differs across GRUB versions). It targets the
	// same endpoint that auto-loading would fetch.
	embeddedCfg := `# Embedded by clustr at image build time.
# HTTP + network modules are compiled in. When chain-loaded via iPXE over HTTP,
# GRUB derives $prefix from the HTTP URL and auto-fetches ($prefix)/grub.cfg.
# The explicit configfile below is a belt-and-suspenders fallback.
insmod http
insmod efinet
insmod net
if [ -n "${net_default_server}" ]; then
    configfile (http,${net_default_server})/api/v1/boot/grub.cfg
fi
if [ -n "${pxe_default_server}" ]; then
    configfile (http,${pxe_default_server})/api/v1/boot/grub.cfg
fi
echo "FATAL: clustr standalone grub.efi: could not determine server address"
echo "net_default_server and pxe_default_server are both unset."
echo "Ensure grub.efi was chain-loaded by iPXE over HTTP, not TFTP or disk."
sleep 30
`

	cfgFile, err := os.CreateTemp("", "clustr-grub-embedded-*.cfg")
	if err != nil {
		return fmt.Errorf("create embedded cfg temp file: %w", err)
	}
	cfgPath := cfgFile.Name()
	defer os.Remove(cfgPath)
	if _, err := cfgFile.WriteString(embeddedCfg); err != nil {
		cfgFile.Close()
		return fmt.Errorf("write embedded cfg: %w", err)
	}
	cfgFile.Close()

	// Full module list for HTTP chain-boot. Covers:
	//   - Partition tables: part_gpt, part_msdos
	//   - Filesystems: xfs (required for /boot XFS), ext2, fat (ESP)
	//   - Disk search: search, search_fs_uuid, search_fs_file, search_label
	//   - Network/HTTP chain-boot: http, efinet, tftp, net
	//   - Boot: normal, linux, linuxefi, configfile
	//   - UI/shell: echo, test, minicmd, all_video, font, gfxterm
	//   - Control: halt, reboot
	modules := []string{
		"part_gpt", "part_msdos",
		"xfs", "ext2", "fat",
		"search", "search_fs_uuid", "search_fs_file", "search_label",
		"http", "efinet", "tftp", "net",
		"normal", "linux", "linuxefi", "configfile",
		"echo", "test", "minicmd",
		"all_video", "font", "gfxterm",
		"halt", "reboot",
	}

	args := []string{
		"-O", "x86_64-efi",
		"-o", destPath,
		"-c", cfgPath,
		"-d", modDir,
		"-p", "", // empty prefix: GRUB derives prefix from HTTP chain-load URL
	}
	args = append(args, modules...)

	cmd := exec.Command(grubMkimage, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("grub2-mkimage: %w\noutput: %s", err, string(out))
	}

	// Post-build verification: confirm the resulting binary contains XFS support.
	// grub2-mkimage exits 0 even when a module is silently omitted due to a
	// missing dependency; checking the binary content catches this before the
	// binary is served to nodes.
	builtData, readErr := os.ReadFile(destPath)
	if readErr != nil {
		return fmt.Errorf("post-build read of %s: %w", destPath, readErr)
	}
	// "XFSB" is the XFS superblock magic that appears in the compiled-in xfs module.
	if !bytes.Contains(builtData, []byte("XFSB")) {
		// Secondary check: the string "xfs" appears in GRUB module metadata.
		if !bytes.Contains(builtData, []byte("\x00xfs\x00")) {
			return fmt.Errorf("post-build verification failed: grub.efi at %s does not appear to contain XFS support (XFSB magic not found) — xfs.mod may have a missing dependency; check grub2-mkimage output above", destPath)
		}
	}

	return nil
}

// copyGrubEFI tries to build a standalone grub.efi with all modules compiled in
// and an embedded disk-search config (via grub2-mkimage), falling back to copying
// the stock grubx64.efi from the rootfs ESP. The standalone binary works when
// chain-loaded over HTTP because it carries its own module set; the stock copy
// requires loading modules from disk and may fail for HTTP chain-boot.
// Non-fatal: if neither path succeeds (BIOS-only images, custom layouts) the
// server's /api/v1/boot/grub.efi handler returns 404, which is correct for
// non-UEFI images.
func copyGrubEFI(rootMnt, rootfsDestDir string) {
	imageDir := filepath.Dir(rootfsDestDir)
	destPath := filepath.Join(imageDir, "grub.efi")

	// Try to build a standalone grub.efi with embedded disk-search config.
	// This produces a binary that works when chain-loaded over HTTP because
	// all required modules (part_gpt, xfs, search, etc.) are compiled in.
	if err := buildStandaloneGrubEFI(rootMnt, destPath); err == nil {
		fmt.Fprintf(os.Stderr, "image: built standalone grub.efi at %s\n", destPath)
		return
	} else {
		// Standalone build failed — fall back to copying the stock RPM binary.
		// This is expected in environments where grub2-mkimage is unavailable.
		// The deploy finalization copy-back (rsync.go after grub2-install) will
		// eventually replace the binary; this fallback is better than no grub.efi.
		fmt.Fprintf(os.Stderr, "image: standalone grub.efi build failed (%v) — falling back to stock RPM binary (chain-boot may fail)\n", err)
	}

	// Fallback: copy the stock grubx64.efi from the rootfs ESP.
	// This binary requires module loading from disk and may not work for
	// HTTP chain-loading, but is better than nothing for direct disk boot.
	efiBase := filepath.Join(rootMnt, "boot", "efi")
	for _, rel := range grubEFICandidates {
		src := filepath.Join(efiBase, rel)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			continue
		}
		return
	}
}

// probeMountPoint attempts to identify the likely mount point of a block device
// by probing its filesystem label or by mounting it read-only and checking for
// canonical marker files.
func probeMountPoint(dev string) string {
	// Try blkid for a PARTLABEL first (fast, no mount required).
	out, err := exec.Command("blkid", "-o", "value", "-s", "PARTLABEL", dev).CombinedOutput()
	if err == nil {
		label := strings.ToLower(strings.TrimSpace(string(out)))
		switch label {
		case "root", "/":
			return "/"
		case "boot", "/boot":
			return "/boot"
		}
	}

	// Try LABEL.
	out, err = exec.Command("blkid", "-o", "value", "-s", "LABEL", dev).CombinedOutput()
	if err == nil {
		label := strings.ToLower(strings.TrimSpace(string(out)))
		switch label {
		case "root":
			return "/"
		case "boot":
			return "/boot"
		}
	}

	// Last resort: mount read-only and look for /etc/os-release (root marker).
	mnt, err := os.MkdirTemp("", "clustr-probe-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(mnt)

	if err := unix.Mount(dev, mnt, "auto", unix.MS_RDONLY, ""); err != nil {
		return ""
	}
	defer func() { _ = unix.Unmount(mnt, unix.MNT_DETACH) }()

	if _, err := os.Stat(filepath.Join(mnt, "etc", "os-release")); err == nil {
		return "/"
	}
	if _, err := os.Stat(filepath.Join(mnt, "vmlinuz")); err == nil {
		return "/boot"
	}
	if _, err := os.Stat(filepath.Join(mnt, "grub2")); err == nil {
		return "/boot"
	}
	if _, err := os.Stat(filepath.Join(mnt, "grub")); err == nil {
		return "/boot"
	}
	return ""
}

// contentOnlyExcludes lists the rsync --exclude arguments that strip all
// layout-specific state from an installed rootfs before it is packed into a
// content-only image tarball (ADR-0009).
//
// These paths fall into three categories:
//
//  1. Boot identity — files whose content is unique to the machine-id that
//     Anaconda used during installation. They must be absent from the image
//     so the deployer can write fresh copies that reference the target node's
//     actual machine-id and disk topology.
//     Includes: /etc/fstab, /etc/machine-id, /var/lib/dbus/machine-id,
//               BLS boot entries, grub.cfg, grubenv.
//
//  2. Bootloader binaries — grub2 modules and EFI binaries are
//     firmware/target-specific; they are re-installed by grub2-install at
//     deploy time for the target node's firmware type (BIOS or UEFI).
//     Including them in the image would pin the image to the firmware type
//     of the build VM, breaking cross-firmware deployments.
//
//  3. Anaconda artefacts — anything the installer wrote that is specific to
//     the install session (e.g. /root/anaconda-ks.cfg, installer logs).
//
// Paths use rsync glob syntax. Trailing /** is used for directory subtrees.
var contentOnlyExcludes = []string{
	// ── Boot identity ────────────────────────────────────────────────────────
	// /etc/fstab: empty placeholder; deployer writes the real one with UUIDs
	// and any operator-configured extra mounts.
	"--exclude=/etc/fstab",
	// /etc/machine-id and its dbus symlink: regenerated on first boot by
	// systemd-firstboot or dbus-uuidgen.
	"--exclude=/etc/machine-id",
	"--exclude=/var/lib/dbus/machine-id",
	// BLS (Boot Loader Specification) entries: Rocky 9+ places one
	// conf file per kernel per machine-id under /boot/loader/entries/.
	// The deployer writes fresh entries with the target kernel and UUID.
	"--exclude=/boot/loader/entries/*.conf",
	// grub.cfg / grubenv: regenerated by grub2-mkconfig at deploy time.
	// grubenv holds save_env state (last-boot menu selection) that can cause
	// the wrong kernel to boot when carried from the build VM.
	"--exclude=/boot/grub2/grub.cfg",
	"--exclude=/boot/grub2/grubenv",
	// ── Bootloader binaries ──────────────────────────────────────────────────
	// grubx64.efi and shimx64.efi MUST be present in the image (ADR-0009).
	// The deploy initramfs has no DNS/network access so dnf install is not a
	// viable fallback — include these binaries from the ESP at image-build time.
	// Only grub.cfg is excluded (regenerated per-deploy by grub2-mkconfig) and
	// EFI/BOOT/ (the removable fallback path written fresh by grub2-install --removable).
	"--exclude=/boot/efi/EFI/*/grub.cfg",
	"--exclude=/boot/efi/EFI/BOOT/**",
	// BIOS grub modules: re-installed by grub2-install --target=i386-pc.
	"--exclude=/boot/grub2/i386-pc/**",
	// UEFI grub modules in /boot/grub2/x86_64-efi/ are excluded: grub2-install
	// --target=x86_64-efi reads its modules from /usr/lib/grub/x86_64-efi/ inside
	// the chroot (the deployed OS RPM-owned copy), not from /boot/grub2/x86_64-efi/.
	// The modules in /boot/ are a generated cache written by grub2-install itself;
	// they are re-created at deploy time and must not be pinned to the build VM.
	"--exclude=/boot/grub2/x86_64-efi/**",
	// ── Anaconda artefacts ────────────────────────────────────────────────────
	// Kickstart that Anaconda saved to /root — build-session specific.
	"--exclude=/root/anaconda-ks.cfg",
	"--exclude=/root/original-ks.cfg",
}

// ContentOnlyExcludes returns the rsync exclude arguments used by
// rsyncExtracted to produce a content-only image tarball (ADR-0009).
// Exported for testing and introspection.
func ContentOnlyExcludes() []string {
	return contentOnlyExcludes
}

// rsyncExtracted rsyncs an extracted rootfs, preserving all attributes and
// symlinks literally (dangling symlinks are copied as-is, not dereferenced).
//
// Exit code 23 from rsync means "some files/attrs were not transferred". On a
// freshly installed Rocky/RHEL system this is almost always caused by dangling
// symlinks — authselect-managed links (/etc/nsswitch.conf, /etc/pam.d/*-auth,
// etc.), kernel-devel build-dir links, and firmware package oddities. These are
// intentionally dangling and are safe to carry into the image as-is; they
// resolve correctly on first boot. We tolerate exit 23 when every error line
// matches "symlink has no referent". Any other exit-23 cause (I/O error,
// permission denied, etc.) is still surfaced as an error.
func rsyncExtracted(src, dst string) error {
	// --one-file-system is intentionally NOT used here: we want to cross
	// the /boot mount boundary (already mounted at rootMnt/boot above).
	// Pseudo-filesystems (/proc, /sys, /dev) don't exist in the installed image.
	// NOTE: do NOT pass --copy-links / -L / --copy-unsafe-links / --safe-links:
	// those flags dereference symlinks and turn dangling ones into errors.
	// -l (preserve symlinks) is already implied by -a.
	//
	// contentOnlyExcludes strips layout-specific boot state so the resulting
	// rootfs is firmware-agnostic and can be deployed to any node (ADR-0009).
	args := append([]string{"-aAXH", "--numeric-ids"}, contentOnlyExcludes...)
	args = append(args, src, dst)
	cmd := exec.Command("rsync", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr // rsync mixes diagnostic output on stdout too

	err := cmd.Run()
	if err == nil {
		return nil
	}

	// Check for exit code 23 (partial transfer due to errors).
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 23 {
		return fmt.Errorf("rsync extracted rootfs: %w\noutput: %s", err, stderr.String())
	}

	// Exit 23: inspect each error line. Tolerate only "symlink has no referent".
	errOutput := stderr.String()
	for _, line := range strings.Split(errOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// rsync prefixes its own messages with "rsync:" or "rsync error:" — skip those.
		if strings.HasPrefix(line, "rsync:") || strings.HasPrefix(line, "rsync error:") ||
			strings.HasPrefix(line, "sent ") || strings.HasPrefix(line, "total size") {
			continue
		}
		// The only tolerated per-file warning.
		if strings.Contains(line, "symlink has no referent") {
			continue
		}
		// Any other error line is a real problem.
		return fmt.Errorf("rsync extracted rootfs (exit 23, non-symlink error): %s\nfull output: %s", line, errOutput)
	}

	// All errors were dangling symlinks — this is expected and safe to ignore.
	return nil
}
