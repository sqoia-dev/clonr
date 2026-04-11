// Package deploy provides deployment engines for writing images to target nodes.
// Supported engines: rsync (filesystem-level tar extraction) and block (dd/partclone).
// The Deployer interface enforces a three-phase contract: Preflight → Deploy → Finalize.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/hardware"
)

// ErrNotImplemented is returned by engine stubs pending full implementation.
var ErrNotImplemented = errors.New("not implemented")

// ErrPreflightFailed is returned when preconditions for deployment are not met.
var ErrPreflightFailed = errors.New("preflight failed")

// ProgressFunc is called periodically during a Deploy operation to report progress.
// phase is a human-readable label such as "downloading", "partitioning", "writing".
type ProgressFunc func(bytesWritten, totalBytes int64, phase string)

// DeployOpts holds the resolved parameters for a single deployment run.
type DeployOpts struct {
	// ImageURL is the full URL to download the image blob from.
	ImageURL string
	// AuthToken is the Bearer token sent with the blob download request.
	AuthToken string
	// TargetDisk is the resolved block device path, e.g. /dev/nvme0n1.
	// Set by Preflight based on DiskLayout constraints and hardware profile.
	TargetDisk string
	// Format is "filesystem" (tar archive) or "block" (raw image).
	Format string
	// MountRoot is the temporary directory where partitions are mounted
	// during a filesystem-format deployment. Unused for block deployments.
	MountRoot string
}

// Deployer is the interface implemented by all deployment backends.
// Callers must invoke Preflight before Deploy, and Deploy before Finalize.
type Deployer interface {
	// Preflight validates that the target hardware can accept this deployment.
	// It resolves the target disk and writes it into opts.TargetDisk.
	Preflight(ctx context.Context, layout api.DiskLayout, hw hardware.SystemInfo) error

	// Deploy downloads the image and writes it to the target disk.
	// progress is called periodically and may be nil.
	Deploy(ctx context.Context, opts DeployOpts, progress ProgressFunc) error

	// Finalize applies node-specific identity (hostname, network, SSH keys)
	// to the freshly deployed filesystem rooted at mountRoot.
	Finalize(ctx context.Context, cfg api.NodeConfig, mountRoot string) error
}

// runCmd executes a command and returns a combined error message if it fails.
// The command's combined output is included in the error for debuggability.
func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deploy: %s %v: %w\noutput: %s", name, args, err, string(out))
	}
	return nil
}

// diskSizeBytes returns the size of a block device in bytes by reading
// /sys/class/block/<name>/size (512-byte sectors).
func diskSizeBytes(devPath string) (int64, error) {
	// Use blockdev --getsize64 for a direct byte count — simpler and cross-distro.
	cmd := exec.Command("blockdev", "--getsize64", devPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("deploy: blockdev --getsize64 %s: %w", devPath, err)
	}
	var size int64
	if _, err := fmt.Sscanf(string(out), "%d", &size); err != nil {
		return 0, fmt.Errorf("deploy: parse disk size for %s: %w", devPath, err)
	}
	return size, nil
}

// totalLayoutBytes returns the sum of all fixed-size partitions in the layout.
// Partitions with SizeBytes == 0 (fill-remaining) are excluded from the sum
// but imply that any remaining space is consumed.
func totalLayoutBytes(layout api.DiskLayout) int64 {
	var total int64
	for _, p := range layout.Partitions {
		total += p.SizeBytes
	}
	return total
}

// isBootDisk returns true if any partition on the disk is mounted at "/" or "/boot".
// This identifies the currently running system disk, which must not be overwritten.
func isBootDisk(disk hardware.Disk) bool {
	for _, p := range disk.Partitions {
		mp := strings.TrimSpace(p.MountPoint)
		if mp == "/" || mp == "/boot" || mp == "/boot/efi" {
			return true
		}
	}
	return false
}

// selectTargetDisk picks the best disk from hw for the given layout.
//
// Selection priority (highest to lowest):
//  1. layout.TargetDevice hint — if set, use that device if it exists and is large enough.
//  2. Exclude the active boot disk (any disk with "/" or "/boot" mounted).
//  3. Prefer non-removable, non-USB disks.
//  4. Among remaining candidates, pick the smallest disk that still fits
//     (avoids accidentally wiping a large data disk).
//
// The selected disk and the reason for the choice are logged for operator audit.
func selectTargetDisk(layout api.DiskLayout, hw hardware.SystemInfo) (string, error) {
	needed := totalLayoutBytes(layout)

	// 1. Honor an explicit target_device hint from the layout.
	if layout.TargetDevice != "" {
		for _, disk := range hw.Disks {
			if disk.Name == layout.TargetDevice {
				if int64(disk.Size) < needed {
					return "", fmt.Errorf("%w: hinted disk %s (%d bytes) is smaller than layout requires (%d bytes)",
						ErrPreflightFailed, disk.Name, disk.Size, needed)
				}
				devPath := "/dev/" + disk.Name
				log.Printf("deploy: selected disk %s (reason: target_device hint in layout)", devPath)
				return devPath, nil
			}
		}
		return "", fmt.Errorf("%w: hinted target_device %q not found in discovered disks",
			ErrPreflightFailed, layout.TargetDevice)
	}

	// Collect candidates: disks that are large enough and not the boot disk.
	type candidate struct {
		disk   hardware.Disk
		reason string
	}
	var preferred []candidate // non-removable, non-USB
	var fallback []candidate  // removable or USB (lower preference)

	for _, disk := range hw.Disks {
		if int64(disk.Size) < needed {
			continue // too small
		}
		if isBootDisk(disk) {
			log.Printf("deploy: skipping disk %s (boot disk — has / or /boot mounted)", disk.Name)
			continue
		}
		transport := strings.ToLower(disk.Transport)
		if transport == "usb" {
			fallback = append(fallback, candidate{disk, "usb/removable"})
		} else {
			preferred = append(preferred, candidate{disk, "non-removable, non-USB"})
		}
	}

	pool := preferred
	if len(pool) == 0 {
		pool = fallback
	}
	if len(pool) == 0 {
		return "", fmt.Errorf("%w: no disk >= %d bytes found that is not the active boot disk",
			ErrPreflightFailed, needed)
	}

	// 4. Pick the smallest disk that fits among the pool.
	best := pool[0]
	for _, c := range pool[1:] {
		if c.disk.Size < best.disk.Size {
			best = c
		}
	}

	devPath := "/dev/" + best.disk.Name
	log.Printf("deploy: selected disk %s (%d bytes, reason: smallest fitting %s disk)",
		devPath, best.disk.Size, best.reason)
	return devPath, nil
}
