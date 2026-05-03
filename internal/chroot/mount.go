// Package chroot provides safe chroot mount management for HPC node imaging.
// It wraps Linux mount(2) syscalls to set up and tear down the standard
// virtual filesystems needed inside a chroot environment.
package chroot

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// MountSpec describes a single mount operation into a chroot root directory.
type MountSpec struct {
	Source string  // source device or filesystem name, e.g. "proc", "/dev"
	Target string  // destination path relative to chroot root, e.g. "proc"
	FSType string  // filesystem type: "proc", "sysfs", "devtmpfs", "" for bind mounts
	Flags  uintptr // mount flags, e.g. syscall.MS_BIND | syscall.MS_REC
}

// DefaultMounts returns the standard virtual filesystem mounts required by
// most chroot operations: /proc, /sys, /dev, /dev/pts, and /run.
// These are the minimum set needed to run package managers and bootloaders.
func DefaultMounts() []MountSpec {
	return []MountSpec{
		{
			Source: "proc",
			Target: "proc",
			FSType: "proc",
			Flags:  syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV,
		},
		{
			Source: "sysfs",
			Target: "sys",
			FSType: "sysfs",
			Flags:  syscall.MS_NOSUID | syscall.MS_NOEXEC | syscall.MS_NODEV,
		},
		{
			Source: "/dev",
			Target: "dev",
			FSType: "",
			Flags:  syscall.MS_BIND | syscall.MS_REC,
		},
		{
			Source: "devpts",
			Target: "dev/pts",
			FSType: "devpts",
			Flags:  syscall.MS_NOSUID | syscall.MS_NOEXEC,
		},
		{
			Source: "tmpfs",
			Target: "run",
			FSType: "tmpfs",
			Flags:  syscall.MS_NOSUID | syscall.MS_NODEV,
		},
	}
}

// MountAll mounts all specs into the given root directory in order.
// It returns a cleanup function that unmounts in reverse order. If any mount
// fails, already-mounted specs are unmounted immediately before returning the
// error. The cleanup function is safe to call even after an error.
func MountAll(root string, specs []MountSpec) (cleanup func() error, err error) {
	return mountAllWith(root, specs, syscallMount, syscallUnmount)
}

// syscallMount and syscallUnmount wrap the real kernel calls. They are
// package-level vars so tests can swap them without subprocesses.
var syscallMount = func(source, target, fstype string, flags uintptr, data string) error {
	return syscall.Mount(source, target, fstype, flags, data)
}

var syscallUnmount = func(target string, flags int) error {
	return syscall.Unmount(target, flags)
}

// mountAllWith is the injectable implementation used by both MountAll and tests.
func mountAllWith(
	root string,
	specs []MountSpec,
	mounter func(source, target, fstype string, flags uintptr, data string) error,
	unmounter func(target string, flags int) error,
) (cleanup func() error, err error) {
	var mounted []MountSpec

	doCleanup := func() error {
		var firstErr error
		for i := len(mounted) - 1; i >= 0; i-- {
			target := filepath.Join(root, mounted[i].Target)
			if err := unmounter(target, syscall.MNT_DETACH); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("chroot/mount: unmount %s: %w", target, err)
			}
		}
		return firstErr
	}

	for _, spec := range specs {
		target := filepath.Join(root, spec.Target)
		if err := mounter(spec.Source, target, spec.FSType, spec.Flags, ""); err != nil {
			// Roll back any mounts we already made before failing.
			_ = doCleanup()
			return func() error { return nil }, fmt.Errorf("chroot/mount: mount %s -> %s: %w", spec.Source, target, err)
		}
		mounted = append(mounted, spec)
	}

	return doCleanup, nil
}
