//go:build deploy_integration

package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FakeDisk represents a loopback-backed sparse file attached as a block device.
// Create one with NewFakeDisk. Cleanup is registered automatically via t.Cleanup.
type FakeDisk struct {
	// Path is the absolute path of the backing sparse file.
	Path string
	// LoopDev is the kernel loopback device (e.g. /dev/loop7).
	LoopDev string

	t *testing.T
}

// NewFakeDisk creates a sparse file of sizeGB gigabytes in t.TempDir(), attaches
// it as a loopback device with --partscan so partition nodes (loop0p1, loop0p2,
// …) appear under /dev, and registers cleanup via t.Cleanup.
//
// The test is skipped automatically if:
//   - the process is not root (loopback ioctl requires CAP_SYS_ADMIN)
//   - losetup(8) is not on PATH
func NewFakeDisk(t *testing.T, sizeGB int) *FakeDisk {
	t.Helper()
	requireRoot(t)
	requireBinary(t, "losetup")

	// Create the sparse backing file.
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "disk.img")

	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("NewFakeDisk: create sparse file: %v", err)
	}
	// Seek to the desired size and write one null byte — this makes it sparse
	// on filesystems that support it (ext4, xfs, btrfs, tmpfs on Linux).
	size := int64(sizeGB) << 30
	if _, err := f.Seek(size-1, 0); err != nil {
		f.Close()
		t.Fatalf("NewFakeDisk: seek sparse file: %v", err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		f.Close()
		t.Fatalf("NewFakeDisk: write sparse file tail: %v", err)
	}
	f.Close()

	// Attach with --partscan so the kernel creates partition device nodes.
	out, err := exec.Command("losetup", "--partscan", "--find", "--show", imgPath).Output()
	if err != nil {
		t.Fatalf("NewFakeDisk: losetup --find --show: %v", err)
	}
	loopDev := strings.TrimSpace(string(out))
	if loopDev == "" {
		t.Fatal("NewFakeDisk: losetup returned empty device path")
	}

	fd := &FakeDisk{Path: imgPath, LoopDev: loopDev, t: t}
	t.Cleanup(fd.Cleanup)
	return fd
}

// Cleanup detaches the loopback device and removes the backing file.
// It is safe to call more than once.
func (d *FakeDisk) Cleanup() {
	d.t.Helper()
	if d.LoopDev == "" {
		return
	}
	// --detach is idempotent if the device is already detached.
	if out, err := exec.Command("losetup", "--detach", d.LoopDev).CombinedOutput(); err != nil {
		d.t.Logf("FakeDisk.Cleanup: losetup --detach %s: %v\n%s", d.LoopDev, err, out)
	}
	d.LoopDev = ""
	// Backing file is inside t.TempDir() so it will also be removed by the
	// testing package, but we remove it explicitly to release the sparse
	// blocks early and to make the intent clear.
	_ = os.Remove(d.Path)
}

// Partition returns the kernel device node for partition number n (1-indexed).
// For example, if LoopDev is /dev/loop7, Partition(1) returns /dev/loop7p1.
func (d *FakeDisk) Partition(n int) string {
	return fmt.Sprintf("%sp%d", d.LoopDev, n)
}

// WaitForPartitions polls /dev until the first n partition nodes exist for this
// loopback device, or until timeout elapses. It returns an error if the nodes
// do not appear in time.
//
// Partition table creation on loopback is asynchronous: losetup returns before
// the kernel has processed the partition table. Callers must wait before
// opening partition devices.
func (d *FakeDisk) WaitForPartitions(n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready := 0
		for i := 1; i <= n; i++ {
			if _, err := os.Stat(d.Partition(i)); err == nil {
				ready++
			}
		}
		if ready == n {
			return nil
		}
		// Trigger udevadm settle to process any pending partition events.
		_ = exec.Command("udevadm", "settle", "--timeout=1").Run()
		time.Sleep(100 * time.Millisecond)
	}
	// Last resort: use partprobe to force the kernel to re-read.
	_ = exec.Command("partprobe", d.LoopDev).Run()
	_ = exec.Command("udevadm", "settle", "--timeout=2").Run()
	time.Sleep(200 * time.Millisecond)

	for i := 1; i <= n; i++ {
		if _, err := os.Stat(d.Partition(i)); err != nil {
			return fmt.Errorf("WaitForPartitions: partition %s not present after %s: %w",
				d.Partition(i), timeout, err)
		}
	}
	return nil
}

// requireRoot skips the test if the current process does not have root
// privileges. Most loopback ioctls require CAP_SYS_ADMIN.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("loopback tests require root (CAP_SYS_ADMIN); re-run with sudo")
	}
}

// requireBinary skips the test if the named binary is not on PATH.
func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("loopback tests require %q on PATH: %v", name, err)
	}
}
