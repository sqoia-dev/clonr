package deploy

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// setupChrootMounts prepares a target rootfs for `chroot` execution by
// bind-mounting the host's virtual filesystems and DNS resolver into it.
//
// Without these mounts, any binary executed inside the chroot that depends on:
//   - /proc (e.g. systemctl, anything reading /proc/self/...)
//   - /sys (e.g. udev queries, kernel module probes)
//   - /dev (e.g. block-device tools, /dev/null, /dev/urandom)
//   - /etc/resolv.conf (e.g. dnf, curl, ssh — anything that resolves DNS)
//
// will silently fail or hang. The DNS case is particularly insidious during
// PXE-driven deploys: the image's baked-in /etc/resolv.conf often points at
// an unreachable nameserver (e.g. QEMU NAT 10.0.2.3) and `dnf install` blocks
// on every package fetch for tens of seconds before timing out.
//
// THE BUG THIS REPLACES:
// Before this helper existed, install_instructions script payloads attempted
// to set up these mounts themselves with lines like:
//
//	mount --bind /etc/resolv.conf /etc/resolv.conf
//
// That ran INSIDE the chroot, which made it a no-op self-bind onto the
// chroot's already-broken resolv.conf. The fix is to do the bind from the
// HOST side onto <targetRoot>/etc/resolv.conf BEFORE chrooting. This file
// owns that lifecycle.
//
// Returns a cleanup function that unmounts everything in reverse order.
// The caller MUST defer cleanup(); leaks on the deploy host accumulate
// across deploys and eventually exhaust the mount table or pin the rootfs
// against unmount/reboot.
//
// On any partial-setup failure, the cleanup of already-completed mounts
// runs before returning the error — callers do not need to call cleanup()
// when err != nil.
func setupChrootMounts(targetRoot string) (cleanup func(), err error) {
	log := deployLogger(nil)

	// Track successful mounts for ordered teardown.
	var mounted []string
	doCleanup := func() {
		// Unmount in reverse order. Use MNT_DETACH so a busy mount (rare
		// but possible if a chrooted process leaks fds) doesn't strand the
		// mountpoint — detach removes it from the namespace immediately
		// and the kernel finalises the unmount when the last ref drops.
		for i := len(mounted) - 1; i >= 0; i-- {
			path := mounted[i]
			if uerr := unix.Unmount(path, unix.MNT_DETACH); uerr != nil {
				log.Warn().Err(uerr).Str("path", path).
					Msg("setupChrootMounts: cleanup unmount failed")
			}
		}
	}

	// fail wraps error returns so we run cleanup on partial setup.
	fail := func(format string, args ...any) (func(), error) {
		doCleanup()
		return nil, fmt.Errorf(format, args...)
	}

	// 1. /proc — required by systemctl, ps, /proc/self lookups.
	procPath := filepath.Join(targetRoot, "proc")
	if err := os.MkdirAll(procPath, 0o555); err != nil {
		return fail("mkdir %s: %w", procPath, err)
	}
	if err := unix.Mount("proc", procPath, "proc", 0, ""); err != nil {
		return fail("mount proc on %s: %w", procPath, err)
	}
	mounted = append(mounted, procPath)

	// 2. /sys — required by some udev queries and kernel-module tooling.
	sysPath := filepath.Join(targetRoot, "sys")
	if err := os.MkdirAll(sysPath, 0o555); err != nil {
		return fail("mkdir %s: %w", sysPath, err)
	}
	if err := unix.Mount("sysfs", sysPath, "sysfs", 0, ""); err != nil {
		return fail("mount sysfs on %s: %w", sysPath, err)
	}
	mounted = append(mounted, sysPath)

	// 3. /dev — recursive bind so /dev/pts and /dev/shm come along.
	// Recursive bind is the most reliable path: any nested mount inside
	// /dev (devpts, shm, mqueue) is replicated into the chroot. Compare
	// to mounting devtmpfs fresh, which would not pick up devpts and
	// would leave any tooling that opens /dev/pts/* hanging.
	devPath := filepath.Join(targetRoot, "dev")
	if err := os.MkdirAll(devPath, 0o755); err != nil {
		return fail("mkdir %s: %w", devPath, err)
	}
	// MS_BIND | MS_REC = recursive bind mount.
	if err := unix.Mount("/dev", devPath, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fail("rbind /dev on %s: %w", devPath, err)
	}
	mounted = append(mounted, devPath)

	// 4. /etc/resolv.conf — THE KEY FIX.
	//
	// We bind-mount the HOST's /etc/resolv.conf onto the target's
	// /etc/resolv.conf so anything in the chroot that resolves DNS uses
	// the deploy host's resolver (which has reachable nameservers, since
	// the deploy host is by definition online). This eliminates the
	// 35+min hang where `dnf install` would block on every package fetch
	// trying to resolve repo hostnames against an unreachable QEMU NAT
	// DNS address (10.0.2.3) baked into the image.
	//
	// Behaviour notes:
	//   - If the target's /etc/resolv.conf does not exist, we create an
	//     empty placeholder file before binding (bind-mount requires the
	//     target to exist).
	//   - If /etc does not exist in the target rootfs, we create it. This
	//     should never happen for a real OS image but defends against
	//     callers passing partial roots in tests.
	//   - The host's /etc/resolv.conf might itself be a symlink (systemd-
	//     resolved stub on the deploy host). bind-mount follows the link
	//     on the source side, so the underlying file is what gets bound.
	//     If for some reason the host has no /etc/resolv.conf, we skip
	//     this mount and warn — the chroot will still work for non-DNS
	//     operations.
	hostResolv := "/etc/resolv.conf"
	if _, statErr := os.Stat(hostResolv); statErr != nil {
		log.Warn().Err(statErr).
			Msg("setupChrootMounts: host /etc/resolv.conf missing — chroot DNS will be broken; skipping bind")
	} else {
		targetEtc := filepath.Join(targetRoot, "etc")
		if err := os.MkdirAll(targetEtc, 0o755); err != nil {
			return fail("mkdir %s: %w", targetEtc, err)
		}
		targetResolv := filepath.Join(targetEtc, "resolv.conf")

		// Ensure target file exists for bind. If the existing file is a
		// symlink (some images symlink resolv.conf to systemd-resolved's
		// stub at /run/systemd/resolve/stub-resolv.conf), replace it with
		// a regular empty file so the bind has a stable target. The
		// symlink is restored implicitly when we unmount in cleanup —
		// EXCEPT when we replaced a symlink with a real file. In that
		// case, the original symlink is gone. We accept that tradeoff:
		// the post-deploy node will have its NM-injected DNS overlaying
		// /etc/resolv.conf at first boot anyway (see writeClustrDHCPProfile
		// and clusterDNSServers).
		if info, lerr := os.Lstat(targetResolv); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			if rerr := os.Remove(targetResolv); rerr != nil {
				return fail("remove symlink %s: %w", targetResolv, rerr)
			}
		}
		if _, serr := os.Stat(targetResolv); os.IsNotExist(serr) {
			if werr := os.WriteFile(targetResolv, nil, 0o644); werr != nil {
				return fail("create placeholder %s: %w", targetResolv, werr)
			}
		}
		if err := unix.Mount(hostResolv, targetResolv, "", unix.MS_BIND, ""); err != nil {
			return fail("bind %s on %s: %w", hostResolv, targetResolv, err)
		}
		mounted = append(mounted, targetResolv)
	}

	log.Info().Str("root", targetRoot).Int("mounts", len(mounted)).
		Msg("setupChrootMounts: chroot virtual filesystems and DNS ready")
	return doCleanup, nil
}
