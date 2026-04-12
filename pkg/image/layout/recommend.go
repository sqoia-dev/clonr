// Package layout provides disk layout recommendation and validation for clonr nodes.
//
// Recommend() produces a DiskLayout from discovered hardware so admins do not
// need to craft partition tables by hand. The reasoning behind each decision is
// returned alongside the layout so operators can understand and override it.
package layout

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/hardware"
)

const (
	mb = int64(1 << 20)
	gb = int64(1 << 30)

	// biosbootSize is the size of the BIOS boot (GPT BIOS grub) partition.
	biosbootSize = 1 * mb
	// espSize is the size of the EFI System Partition when running UEFI.
	espSize = 512 * mb
	// bootSmall is the /boot size for small disks.
	bootSmall = 1 * gb
	// bootLarge is the /boot size for disks >= 500 GB.
	bootLarge = 2 * gb
	// swapSmall is the swap size for disks < 500 GB.
	swapSmall = 4 * gb
	// swapMid is the swap size for mid-size disks.
	swapMid = 16 * gb
	// swapLarge is the swap size for large disks.
	swapLarge = 32 * gb
	// rootSmall is the / size for small-disk layouts.
	rootSmall = 0 // fill remaining
	// rootMid is the / size when a separate /scratch is present.
	rootMid = 100 * gb
	// rootLarge is the / size on very large disks.
	rootLarge = 200 * gb

	// smallDiskThreshold is the boundary between "small" and "mid" single-disk layouts.
	smallDiskThreshold = 500 * gb
	// largeDiskThreshold is the boundary between "mid" and "large" single-disk layouts.
	largeDiskThreshold = 2 * 1024 * gb // 2 TiB

	// highRAMThreshold: if RAM >= this value, skip a RAM-based swap partition.
	// Modern HPC nodes with large RAM don't benefit from swap — it just adds
	// latency and wastes partition space that could go to /scratch.
	highRAMThreshold = 64 * gb // 64 GiB in RAM (bytes — we convert from KB)

	// manyDisksThreshold: more than this many similarly-sized disks triggers
	// the "many small disks" heuristic (individual /scratch-N mounts, no RAID).
	manyDisksThreshold = 4
)

// Recommendation is returned by Recommend and contains the proposed layout
// plus the full reasoning chain so operators know why each decision was made.
type Recommendation struct {
	Layout    api.DiskLayout
	Reasoning string   // multi-line human-readable explanation
	Warnings  []string // non-fatal caveats the admin should review
}

// Recommend generates a DiskLayout from discovered hardware.
// imageFormat is one of api.ImageFormatFilesystem or api.ImageFormatBlock.
func Recommend(hw hardware.SystemInfo, imageFormat string) (Recommendation, error) {
	b := &recommendBuilder{
		hw:          hw,
		imageFormat: imageFormat,
	}
	return b.run()
}

// recommendBuilder holds state while building the recommendation.
type recommendBuilder struct {
	hw          hardware.SystemInfo
	imageFormat string
	reasons     []string
	warnings    []string
}

func (b *recommendBuilder) reason(msg string) {
	b.reasons = append(b.reasons, "- "+msg)
}

func (b *recommendBuilder) warn(msg string) {
	b.warnings = append(b.warnings, msg)
}

func (b *recommendBuilder) run() (Recommendation, error) {
	// Rule 1: No disks — cannot recommend.
	if len(b.hw.Disks) == 0 {
		return Recommendation{}, fmt.Errorf("layout: no disks discovered — cannot generate recommendation")
	}

	// Detect boot mode (UEFI or BIOS) from DMI data.
	// We look for "UEFI" or "EFI" in the BIOS vendor/version strings since
	// DMIInfo does not yet carry a BootMode field directly.
	isUEFI := b.detectUEFI()
	if isUEFI {
		b.reason("UEFI detected (DMI BIOS vendor/version contains 'EFI' or 'UEFI') — using ESP instead of biosboot partition")
	} else {
		b.reason("BIOS/legacy boot detected — using biosboot partition for GRUB2 on GPT")
	}

	// Determine RAM for swap-skip heuristic.
	ramBytes := int64(b.hw.Memory.TotalKB) * 1024
	skipSwap := ramBytes >= highRAMThreshold
	if skipSwap {
		b.reason(fmt.Sprintf("RAM is %s (>= 64 GiB) — skipping swap partition (HPC default)", fmtGB(ramBytes)))
	}

	// Filter candidate disks (exclude boot disk, USB, removable).
	candidates := b.candidateDisks()
	if len(candidates) == 0 {
		return Recommendation{}, fmt.Errorf("layout: no suitable install disks found (all disks are boot, USB, or too small)")
	}

	// Dispatch to the right heuristic based on disk count and geometry.
	switch {
	case len(candidates) == 1:
		return b.singleDisk(candidates[0], isUEFI, skipSwap)

	case len(candidates) == 2 && identicalSize(candidates[0], candidates[1]):
		b.reason("Two identically-sized disks found — recommending RAID1 for / (mirrored)")
		return b.twoIdenticalDisks(candidates[0], candidates[1], isUEFI, skipSwap)

	case hasMixedNVMeSATA(candidates):
		b.reason("Mixed NVMe + SATA disk topology detected — placing OS on NVMe, /scratch on SATA")
		return b.nvmeSataLayout(candidates, isUEFI, skipSwap)

	case len(candidates) > manyDisksThreshold:
		b.reason(fmt.Sprintf("%d candidate disks — using individual /scratch-N mounts (no RAID)", len(candidates)))
		return b.manyDisks(candidates, isUEFI, skipSwap)

	default:
		// Fallback: use the smallest suitable disk for /, ignore the rest.
		b.reason(fmt.Sprintf("%d candidate disks but no special topology matched — using smallest disk for OS install", len(candidates)))
		b.warn("Multiple non-identical disks found; only the smallest is used for the OS layout. Consider defining a RAID array or per-disk assignments in a custom override.")
		primary := smallestDisk(candidates)
		return b.singleDisk(primary, isUEFI, skipSwap)
	}
}

// detectUEFI returns true if DMI data suggests UEFI firmware.
// Checks BIOSVendor and BIOSVersion strings for common UEFI indicators.
func (b *recommendBuilder) detectUEFI() bool {
	vendor := strings.ToUpper(b.hw.DMI.BIOSVendor)
	version := strings.ToUpper(b.hw.DMI.BIOSVersion)
	for _, kw := range []string{"UEFI", "EFI", "TIANOCORE", "OVMF", "EDK"} {
		if strings.Contains(vendor, kw) || strings.Contains(version, kw) {
			return true
		}
	}
	return false
}

// candidateDisks returns disks that are eligible for OS installation:
// non-boot, non-USB, non-removable.
func (b *recommendBuilder) candidateDisks() []hardware.Disk {
	var out []hardware.Disk
	for _, d := range b.hw.Disks {
		if isBootDisk(d) {
			continue
		}
		if strings.EqualFold(d.Transport, "usb") {
			continue
		}
		out = append(out, d)
	}
	// Sort by size ascending so "primary" is the smallest candidate.
	sort.Slice(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// singleDisk generates a layout for a single target disk.
func (b *recommendBuilder) singleDisk(disk hardware.Disk, isUEFI, skipSwap bool) (Recommendation, error) {
	size := int64(disk.Size)
	b.reason(fmt.Sprintf("Primary disk: %s (%s, %s)", disk.Name, disk.Transport, fmtGB(size)))

	var partitions []api.PartitionSpec

	// Boot partition stack.
	partitions = append(partitions, b.bootPartitions(isUEFI)...)

	// Swap (unless skipped for high-RAM nodes).
	swapSize := b.swapSize(size, skipSwap)
	if swapSize > 0 {
		partitions = append(partitions, api.PartitionSpec{
			Label:      "swap",
			SizeBytes:  swapSize,
			Filesystem: "swap",
			MountPoint: "swap",
		})
		b.reason(fmt.Sprintf("swap: %s", fmtGB(swapSize)))
	}

	switch {
	case size < smallDiskThreshold:
		// Small disk: / gets all remaining space.
		b.reason(fmt.Sprintf("Disk < 500 GB — / gets all remaining space (no /scratch)"))
		partitions = append(partitions, api.PartitionSpec{
			Label:      "root",
			SizeBytes:  0, // fill remaining
			Filesystem: "xfs",
			MountPoint: "/",
		})

	case size < largeDiskThreshold:
		// Mid-size: fixed-size /, then /scratch fills the rest.
		b.reason(fmt.Sprintf("Disk 500 GB – 2 TiB — / is 100 GB, /scratch gets remaining %s", fmtGB(size-rootMid)))
		partitions = append(partitions, api.PartitionSpec{
			Label:      "root",
			SizeBytes:  rootMid,
			Filesystem: "xfs",
			MountPoint: "/",
		})
		partitions = append(partitions, api.PartitionSpec{
			Label:      "scratch",
			SizeBytes:  0, // fill remaining
			Filesystem: "xfs",
			MountPoint: "/scratch",
		})

	default:
		// Large disk: bigger / then /scratch.
		b.reason(fmt.Sprintf("Disk > 2 TiB — / is 200 GB, /scratch gets remaining %s", fmtGB(size-rootLarge)))
		partitions = append(partitions, api.PartitionSpec{
			Label:      "root",
			SizeBytes:  rootLarge,
			Filesystem: "xfs",
			MountPoint: "/",
		})
		partitions = append(partitions, api.PartitionSpec{
			Label:      "scratch",
			SizeBytes:  0, // fill remaining
			Filesystem: "xfs",
			MountPoint: "/scratch",
		})
	}

	layout := api.DiskLayout{
		Partitions:   partitions,
		Bootloader:   bootloaderFor(isUEFI),
		TargetDevice: disk.Name,
	}

	return Recommendation{
		Layout:    layout,
		Reasoning: strings.Join(b.reasons, "\n"),
		Warnings:  b.warnings,
	}, nil
}

// twoIdenticalDisks generates a RAID1 layout across two matched disks.
func (b *recommendBuilder) twoIdenticalDisks(d0, d1 hardware.Disk, isUEFI, skipSwap bool) (Recommendation, error) {
	b.reason(fmt.Sprintf("RAID1 members: %s (%s) and %s (%s)",
		d0.Name, fmtGB(int64(d0.Size)), d1.Name, fmtGB(int64(d1.Size))))

	swapSize := b.swapSize(int64(d0.Size), skipSwap)

	// The md0 array spans both disks.
	raid := api.RAIDSpec{
		Name:    "md0",
		Level:   "raid1",
		Members: []string{d0.Name, d1.Name},
	}

	var partitions []api.PartitionSpec

	// Boot partitions land on the md device.
	bootParts := b.bootPartitions(isUEFI)
	for i := range bootParts {
		bootParts[i].Device = "md0"
	}
	partitions = append(partitions, bootParts...)

	if swapSize > 0 {
		partitions = append(partitions, api.PartitionSpec{
			Device:     "md0",
			Label:      "swap",
			SizeBytes:  swapSize,
			Filesystem: "swap",
			MountPoint: "swap",
		})
	}

	partitions = append(partitions, api.PartitionSpec{
		Device:     "md0",
		Label:      "root",
		SizeBytes:  0, // fill remaining RAID space
		Filesystem: "xfs",
		MountPoint: "/",
	})

	layout := api.DiskLayout{
		RAIDArrays: []api.RAIDSpec{raid},
		Partitions: partitions,
		Bootloader: bootloaderFor(isUEFI),
	}

	b.warn("RAID1 layout requires mdadm to be present in the deployed image's initramfs. " +
		"If /scratch workloads don't need redundancy, consider a RAID0 /scratch on top of a single-disk OS layout.")

	return Recommendation{
		Layout:    layout,
		Reasoning: strings.Join(b.reasons, "\n"),
		Warnings:  b.warnings,
	}, nil
}

// nvmeSataLayout places the OS on the NVMe disk and /scratch/data on SATA.
func (b *recommendBuilder) nvmeSataLayout(candidates []hardware.Disk, isUEFI, skipSwap bool) (Recommendation, error) {
	var nvme, sata []hardware.Disk
	for _, d := range candidates {
		if strings.EqualFold(d.Transport, "nvme") {
			nvme = append(nvme, d)
		} else {
			sata = append(sata, d)
		}
	}
	// Use the smallest NVMe for OS.
	primary := smallestDisk(nvme)
	b.reason(fmt.Sprintf("OS disk: %s (NVMe, %s)", primary.Name, fmtGB(int64(primary.Size))))

	swapSize := b.swapSize(int64(primary.Size), skipSwap)
	var partitions []api.PartitionSpec
	partitions = append(partitions, b.bootPartitions(isUEFI)...)

	if swapSize > 0 {
		partitions = append(partitions, api.PartitionSpec{
			Label:      "swap",
			SizeBytes:  swapSize,
			Filesystem: "swap",
			MountPoint: "swap",
		})
	}
	partitions = append(partitions, api.PartitionSpec{
		Label:      "root",
		SizeBytes:  0,
		Filesystem: "xfs",
		MountPoint: "/",
	})

	// Each SATA disk becomes an independent /scratch-N or /data mount.
	for i, d := range sata {
		mp := fmt.Sprintf("/scratch-%d", i+1)
		if len(sata) == 1 {
			mp = "/scratch"
		}
		partitions = append(partitions, api.PartitionSpec{
			Device:     d.Name,
			Label:      fmt.Sprintf("scratch%d", i+1),
			SizeBytes:  0,
			Filesystem: "xfs",
			MountPoint: mp,
		})
		b.reason(fmt.Sprintf("SATA disk %s → %s", d.Name, mp))
	}

	layout := api.DiskLayout{
		Partitions:   partitions,
		Bootloader:   bootloaderFor(isUEFI),
		TargetDevice: primary.Name,
	}

	return Recommendation{
		Layout:    layout,
		Reasoning: strings.Join(b.reasons, "\n"),
		Warnings:  b.warnings,
	}, nil
}

// manyDisks assigns each disk an independent /scratch-N mount (no RAID).
func (b *recommendBuilder) manyDisks(candidates []hardware.Disk, isUEFI, skipSwap bool) (Recommendation, error) {
	// Use the smallest disk for OS install.
	primary := smallestDisk(candidates)
	b.reason(fmt.Sprintf("OS disk: %s (%s)", primary.Name, fmtGB(int64(primary.Size))))

	swapSize := b.swapSize(int64(primary.Size), skipSwap)
	var partitions []api.PartitionSpec
	partitions = append(partitions, b.bootPartitions(isUEFI)...)

	if swapSize > 0 {
		partitions = append(partitions, api.PartitionSpec{
			Label:      "swap",
			SizeBytes:  swapSize,
			Filesystem: "swap",
			MountPoint: "swap",
		})
	}
	partitions = append(partitions, api.PartitionSpec{
		Label:      "root",
		SizeBytes:  0,
		Filesystem: "xfs",
		MountPoint: "/",
	})

	// Remaining disks each get a /scratch-N.
	scratchIdx := 1
	for _, d := range candidates {
		if d.Name == primary.Name {
			continue
		}
		partitions = append(partitions, api.PartitionSpec{
			Device:     d.Name,
			Label:      fmt.Sprintf("scratch%d", scratchIdx),
			SizeBytes:  0,
			Filesystem: "xfs",
			MountPoint: fmt.Sprintf("/scratch-%d", scratchIdx),
		})
		b.reason(fmt.Sprintf("Extra disk %s → /scratch-%d", d.Name, scratchIdx))
		scratchIdx++
	}

	layout := api.DiskLayout{
		Partitions:   partitions,
		Bootloader:   bootloaderFor(isUEFI),
		TargetDevice: primary.Name,
	}

	return Recommendation{
		Layout:    layout,
		Reasoning: strings.Join(b.reasons, "\n"),
		Warnings:  b.warnings,
	}, nil
}

// bootPartitions returns the standard boot partition stack (biosboot or ESP + /boot).
func (b *recommendBuilder) bootPartitions(isUEFI bool) []api.PartitionSpec {
	if isUEFI {
		return []api.PartitionSpec{
			{
				Label:      "esp",
				SizeBytes:  espSize,
				Filesystem: "vfat",
				MountPoint: "/boot/efi",
				Flags:      []string{"esp", "boot"},
			},
			{
				Label:      "boot",
				SizeBytes:  bootLarge,
				Filesystem: "xfs",
				MountPoint: "/boot",
			},
		}
	}
	return []api.PartitionSpec{
		{
			Label:      "biosboot",
			SizeBytes:  biosbootSize,
			Filesystem: "biosboot",
			MountPoint: "",
			Flags:      []string{"bios_grub"},
		},
		{
			Label:      "boot",
			SizeBytes:  bootSmall,
			Filesystem: "xfs",
			MountPoint: "/boot",
		},
	}
}

// swapSize returns the appropriate swap partition size.
// Returns 0 when swap should be skipped (high-RAM nodes or disk too small to bother).
func (b *recommendBuilder) swapSize(diskSize int64, skipSwap bool) int64 {
	if skipSwap {
		return 0
	}
	switch {
	case diskSize < smallDiskThreshold:
		b.reason(fmt.Sprintf("swap: %s (small disk)", fmtGB(swapSmall)))
		return swapSmall
	case diskSize < largeDiskThreshold:
		b.reason(fmt.Sprintf("swap: %s (mid-size disk)", fmtGB(swapMid)))
		return swapMid
	default:
		b.reason(fmt.Sprintf("swap: %s (large disk)", fmtGB(swapLarge)))
		return swapLarge
	}
}

// bootloaderFor returns the correct Bootloader spec for the detected firmware mode.
func bootloaderFor(isUEFI bool) api.Bootloader {
	if isUEFI {
		return api.Bootloader{Type: "grub2", Target: "x86_64-efi"}
	}
	return api.Bootloader{Type: "grub2", Target: "i386-pc"}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func isBootDisk(d hardware.Disk) bool {
	for _, p := range d.Partitions {
		mp := strings.TrimSpace(p.MountPoint)
		if mp == "/" || mp == "/boot" || mp == "/boot/efi" {
			return true
		}
	}
	return false
}

func identicalSize(a, b hardware.Disk) bool {
	// Allow 1% variance to handle firmware/manufacturer rounding differences.
	if a.Size == 0 || b.Size == 0 {
		return false
	}
	delta := int64(a.Size) - int64(b.Size)
	if delta < 0 {
		delta = -delta
	}
	return delta*100/int64(a.Size) <= 1
}

func hasMixedNVMeSATA(disks []hardware.Disk) bool {
	hasNVMe, hasSATA := false, false
	for _, d := range disks {
		switch strings.ToLower(d.Transport) {
		case "nvme":
			hasNVMe = true
		case "sata", "sas", "ata", "":
			hasSATA = true
		}
	}
	return hasNVMe && hasSATA
}

func smallestDisk(disks []hardware.Disk) hardware.Disk {
	best := disks[0]
	for _, d := range disks[1:] {
		if d.Size < best.Size {
			best = d
		}
	}
	return best
}

func fmtGB(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	if bytes >= gb {
		return fmt.Sprintf("%.0f GB", float64(bytes)/float64(gb))
	}
	return fmt.Sprintf("%.0f MB", float64(bytes)/float64(mb))
}
