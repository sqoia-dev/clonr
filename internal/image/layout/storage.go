package layout

// storage.go — ZFS storage role layout recommender.
//
// RecommendStorage() produces a StorageRecommendation for nodes whose role is
// "storage" (NAS / Lustre OSS or MDS / NFS / BeeGFS).  The OS always lands on
// an mdadm RAID1 of the 2 smallest drives; all remaining drives are assigned to
// ZFS pools with topology chosen by drive count and type.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sqoia-dev/clustr/internal/hardware"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// slogPartitionBytes is the size of each SLOG (ZIL) partition on an NVMe drive.
// 10 GiB is enough for a write-optimised ZIL on most workloads.
const slogPartitionBytes = int64(10) * gb

// RecommendStorage produces a storage-optimised layout for a node whose role
// is "storage".  The function:
//
//  1. Classifies disks into HDD / SSD / NVMe categories.
//  2. Picks the 2 smallest drives for an mdadm RAID1 OS install.
//  3. Selects the ZFS vdev topology for remaining HDD/SSD data drives using the
//     table described in the design: stripe for 1–3, raidz1 for 4–5, raidz2
//     bands for 6 and above.
//  4. If 2+ NVMe drives remain after OS selection, carves a small mirrored SLOG
//     partition (10 GiB each) and uses the rest as L2ARC.  A single NVMe gets
//     L2ARC only (SLOG without a mirror is too risky).
//
// imageFirmware follows the same "bios"/"uefi"/auto semantics as Recommend().
func RecommendStorage(hw hardware.SystemInfo, imageFirmware string) (api.StorageRecommendation, error) {
	b := &storageBuilder{
		hw:            hw,
		imageFirmware: imageFirmware,
	}
	return b.run()
}

// storageBuilder accumulates state while building the recommendation.
type storageBuilder struct {
	hw            hardware.SystemInfo
	imageFirmware string
	reasons       []string
	warnings      []string
}

func (b *storageBuilder) reason(msg string) { b.reasons = append(b.reasons, "- "+msg) }
func (b *storageBuilder) warn(msg string)   { b.warnings = append(b.warnings, msg) }

// run is the main entry point; it calls each sub-step in sequence.
func (b *storageBuilder) run() (api.StorageRecommendation, error) {
	if len(b.hw.Disks) == 0 {
		return api.StorageRecommendation{}, fmt.Errorf("storage layout: no disks discovered")
	}

	// ── 1. Classify drives ────────────────────────────────────────────────────
	hdds, ssds, nvmes := classifyDisks(b.hw.Disks)

	b.reason(fmt.Sprintf("disk inventory: %d HDD, %d SSD (non-NVMe), %d NVMe",
		len(hdds), len(ssds), len(nvmes)))
	if len(hdds) > 0 {
		b.reason(fmt.Sprintf("%d HDDs detected (%s, %s each)",
			len(hdds), hdds[0].Model, fmtGB(int64(hdds[0].Size))))
	}
	if len(nvmes) > 0 {
		b.reason(fmt.Sprintf("%d NVMe detected (%s, %s each)",
			len(nvmes), nvmes[0].Model, fmtGB(int64(nvmes[0].Size))))
	}
	if len(ssds) > 0 {
		b.reason(fmt.Sprintf("%d SSDs detected (%s, %s each)",
			len(ssds), ssds[0].Model, fmtGB(int64(ssds[0].Size))))
	}

	// ── 2. Firmware detection ─────────────────────────────────────────────────
	isUEFI := b.detectUEFI()

	// ── 3. Pick 2 OS drives from the global pool (smallest first) ─────────────
	allDrives := concat(hdds, ssds, nvmes)
	if len(allDrives) < 2 {
		return api.StorageRecommendation{}, fmt.Errorf(
			"storage layout: need at least 2 drives (found %d)", len(allDrives))
	}
	// Sort all drives by size ascending; take the first 2.
	sort.Slice(allDrives, func(i, j int) bool { return allDrives[i].Size < allDrives[j].Size })
	osDrives := allDrives[:2]
	b.reason(fmt.Sprintf("OS: RAID1 on %s + %s (smallest 2 drives, %s each)",
		osDrives[0].Name, osDrives[1].Name, fmtGB(int64(osDrives[0].Size))))

	// Remove OS drives from each category pool.
	osNames := map[string]bool{osDrives[0].Name: true, osDrives[1].Name: true}
	hdds = removeDrives(hdds, osNames)
	ssds = removeDrives(ssds, osNames)
	nvmes = removeDrives(nvmes, osNames)

	// ── 4. OS layout (mdadm RAID1) ────────────────────────────────────────────
	osLayout := b.buildOSLayout(osDrives[0], osDrives[1], isUEFI)

	// ── 5. Determine data drives ──────────────────────────────────────────────
	// If the node has HDDs, use HDDs for data (plus SSDs as L2ARC).
	// If no HDDs, treat SSDs as data drives.
	var dataDrives []hardware.Disk
	var l2arcSSDs []hardware.Disk
	if len(hdds) > 0 {
		dataDrives = hdds
		l2arcSSDs = ssds // SSDs become L2ARC when HDDs are the data tier
	} else {
		dataDrives = ssds // all-SSD node: SSDs are the data tier
		// No l2arcSSDs in this case
	}

	if len(dataDrives) == 0 && len(nvmes) == 0 {
		return api.StorageRecommendation{}, fmt.Errorf(
			"storage layout: no data drives remain after OS selection")
	}

	// ── 6. Build ZFS data pool ────────────────────────────────────────────────
	var zfsPools []api.ZFSPool
	var dataStats dataPoolStats

	if len(dataDrives) > 0 {
		pool, stats, err := b.buildDataPool("tank", dataDrives)
		if err != nil {
			return api.StorageRecommendation{}, err
		}
		zfsPools = append(zfsPools, pool)
		dataStats = stats
	} else {
		b.warn("no HDD or SSD data drives found; ZFS data pool is empty — only cache devices configured")
	}

	// ── 7. NVMe → SLOG + L2ARC ───────────────────────────────────────────────
	slogDrives, l2arcNVMes := b.assignNVMeRoles(nvmes)
	allL2ARC := append(l2arcSSDs, l2arcNVMes...) //nolint:gocritic

	if len(slogDrives) >= 2 {
		slogPool := buildSLOGPool(slogDrives[:2])
		zfsPools = append(zfsPools, slogPool)
		b.reason(fmt.Sprintf("SLOG: mirror on %s + %s (%s each) — write-ahead log",
			slogDrives[0].Name+"p1", slogDrives[1].Name+"p1", fmtGB(slogPartitionBytes)))
	}
	if len(allL2ARC) > 0 {
		l2arcPool := buildL2ARCPool(allL2ARC, len(slogDrives) >= 2)
		zfsPools = append(zfsPools, l2arcPool)
		names := make([]string, len(allL2ARC))
		for i, d := range allL2ARC {
			if len(slogDrives) >= 2 && isNVMe(d) {
				names[i] = d.Name + "p2"
			} else {
				names[i] = d.Name
			}
		}
		b.reason(fmt.Sprintf("L2ARC: %s (%s each) — read cache",
			strings.Join(names, " + "), fmtGB(int64(allL2ARC[0].Size))))
	}

	// ── 8. Stats ──────────────────────────────────────────────────────────────
	cacheCount := len(nvmes) + len(l2arcSSDs)
	stats := api.StorageStats{
		RawCapacityBytes:    dataStats.rawBytes,
		UsableCapacityBytes: dataStats.usableBytes,
		VdevCount:           dataStats.vdevCount,
		DrivesForOS:         2,
		DrivesForData:       len(dataDrives),
		DrivesForCache:      cacheCount,
		ParityOverhead:      dataStats.parityOverhead,
	}

	b.reason(fmt.Sprintf("parity overhead: %.0f%% (%s)",
		dataStats.parityOverhead*100, dataStats.parityDescription))
	if dataStats.rawBytes > 0 {
		b.reason(fmt.Sprintf("capacity: ~%s raw, ~%s usable",
			fmtTB(dataStats.rawBytes), fmtTB(dataStats.usableBytes)))
	}

	return api.StorageRecommendation{
		OSLayout:  osLayout,
		ZFSPools:  zfsPools,
		Reasoning: b.reasons,
		Warnings:  b.warnings,
		Stats:     stats,
	}, nil
}

// detectUEFI uses the same firmware detection logic as the general recommender.
func (b *storageBuilder) detectUEFI() bool {
	switch strings.ToLower(b.imageFirmware) {
	case "uefi":
		b.reason("firmware=uefi override — using ESP partition")
		return true
	case "bios":
		b.reason("firmware=bios override — using biosboot partition")
		return false
	}
	// Auto-detect from DMI.
	vendor := strings.ToUpper(b.hw.DMI.BIOSVendor)
	version := strings.ToUpper(b.hw.DMI.BIOSVersion)
	for _, kw := range []string{"UEFI", "EFI", "TIANOCORE", "OVMF", "EDK"} {
		if strings.Contains(vendor, kw) || strings.Contains(version, kw) {
			b.reason("UEFI detected from DMI — using ESP partition")
			return true
		}
	}
	b.reason("BIOS/legacy detected from DMI — using biosboot partition")
	return false
}

// buildOSLayout constructs the mdadm RAID1 layout for the OS drives.
// The RAM-based swap heuristic from the general recommender is also applied.
func (b *storageBuilder) buildOSLayout(d0, d1 hardware.Disk, isUEFI bool) api.DiskLayout {
	// Re-use the general recommendBuilder plumbing for the OS layout.
	rb := &recommendBuilder{
		hw:            b.hw,
		imageFirmware: b.imageFirmware,
	}
	rec, err := rb.twoIdenticalDisks(d0, d1, isUEFI, false)
	if err != nil {
		// Fallback: single disk layout on d0.
		rb2 := &recommendBuilder{hw: b.hw, imageFirmware: b.imageFirmware}
		rec2, _ := rb2.singleDisk(d0, isUEFI, false)
		return rec2.Layout
	}
	return rec.Layout
}

// ─── Data pool builder ────────────────────────────────────────────────────────

// dataPoolStats records the pool topology outcomes for Stats population.
type dataPoolStats struct {
	rawBytes          int64
	usableBytes       int64
	vdevCount         int
	parityOverhead    float64
	parityDescription string
}

// buildDataPool creates the "tank" ZFS pool and returns capacity stats.
func (b *storageBuilder) buildDataPool(name string, drives []hardware.Disk) (api.ZFSPool, dataPoolStats, error) {
	n := len(drives)
	if n == 0 {
		return api.ZFSPool{}, dataPoolStats{}, fmt.Errorf("storage layout: no data drives for pool %q", name)
	}

	driveSize := int64(drives[0].Size)
	rawBytes := driveSize * int64(n)

	vdevType, numVdevs, vdevWidth, desc := chooseVdevTopology(n)

	b.reason(fmt.Sprintf("data pool %q: %d %s vdev(s) × %d drives each (total %d drives)",
		name, numVdevs, vdevType, vdevWidth, n))

	// Build member device names.
	members := make([]string, n)
	for i, d := range drives {
		members[i] = d.Name
	}

	// Calculate usable capacity.
	parityDrivesPerVdev := parityDrives(vdevType)
	usableDrivesPerVdev := vdevWidth - parityDrivesPerVdev
	usableFraction := float64(usableDrivesPerVdev) / float64(vdevWidth)
	usableBytes := int64(float64(rawBytes) * usableFraction)
	parityOverhead := 1.0 - usableFraction

	// Rebuild time estimate for HDDs.
	if drives[0].Rotational {
		estimateHours := estimateRebuildHours(driveSize)
		b.reason(fmt.Sprintf("rebuild time estimate: ~%d–%d hours per drive (%s HDD)",
			estimateHours/2, estimateHours, fmtTB(driveSize)))
	}

	pool := api.ZFSPool{
		Name:       name,
		VdevType:   vdevType,
		Members:    members,
		Mountpoint: "/tank",
		Properties: map[string]string{
			"ashift":          "12",
			"compression":     "lz4",
			"atime":           "off",
			"xattr":           "sa",
			"dnodesize":       "auto",
			"recordsize":      "1M",
			"special_small_blocks": "128K",
		},
	}

	// For multi-vdev configs we encode vdev count in a property so the deployer
	// can split the members list accordingly.
	if numVdevs > 1 {
		pool.Properties["clustr:vdev_count"] = fmt.Sprintf("%d", numVdevs)
		pool.Properties["clustr:vdev_width"] = fmt.Sprintf("%d", vdevWidth)
	}

	stats := dataPoolStats{
		rawBytes:          rawBytes,
		usableBytes:       usableBytes,
		vdevCount:         numVdevs,
		parityOverhead:    parityOverhead,
		parityDescription: desc,
	}

	return pool, stats, nil
}

// chooseVdevTopology returns the vdev type string, number of vdevs, vdev width,
// and a human description for the given number of data drives.
//
// Design table:
//
//	1–3   drives → stripe  (1 vdev, all drives)
//	4–5   drives → raidz1  (1 vdev)
//	6–10  drives → raidz2  (1 vdev)
//	11–20 drives → raidz2  (2 vdevs, split evenly)
//	21–40 drives → raidz2  (3–4 vdevs, 8–10 drives each)
//	41–60 drives → raidz2  (5–6 vdevs, 8–10 drives each)
//	61–100 drives → raidz2 (7–10 vdevs, 8–10 drives each)
//	100+  drives → raidz2  (⌈n/10⌉ vdevs, max 10 drives each)
func chooseVdevTopology(n int) (vdevType string, numVdevs, vdevWidth int, desc string) {
	switch {
	case n <= 3:
		return "stripe", 1, n,
			fmt.Sprintf("stripe of %d drives (too few for parity; add drives to switch to raidz)", n)

	case n <= 5:
		return "raidz1", 1, n,
			fmt.Sprintf("1 parity drive per vdev (raidz1), %d-wide", n)

	case n <= 10:
		return "raidz2", 1, n,
			fmt.Sprintf("2 parity drives per vdev (raidz2), %d-wide", n)

	case n <= 20:
		// 2 vdevs, split evenly (round up for first vdev).
		w := (n + 1) / 2
		return "raidz2", 2, w,
			fmt.Sprintf("2 parity drives per vdev (raidz2), 2×%d-wide", w)

	default:
		// Target 8–10 drives per vdev; round up to keep vdev count reasonable.
		targetWidth := 10
		nv := (n + targetWidth - 1) / targetWidth
		// Rebalance so all vdevs are as equal as possible.
		w := n / nv
		if n%nv != 0 {
			w++ // first few vdevs get one extra drive
		}
		return "raidz2", nv, w,
			fmt.Sprintf("2 parity drives per vdev (raidz2), %d×%d-wide", nv, w)
	}
}

// parityDrives returns the number of parity drives for a given vdev type.
func parityDrives(vdevType string) int {
	switch vdevType {
	case "raidz1":
		return 1
	case "raidz2":
		return 2
	case "raidz3":
		return 3
	default:
		return 0 // stripe / mirror handled elsewhere
	}
}

// estimateRebuildHours returns a rough single-drive rebuild duration in hours,
// assuming ~100 MB/s sequential read throughput from remaining drives.
func estimateRebuildHours(driveSizeBytes int64) int {
	const throughputBytesPerSec = 100 * 1024 * 1024 // 100 MB/s
	secs := driveSizeBytes / throughputBytesPerSec
	hours := int(secs / 3600)
	if hours < 1 {
		return 1
	}
	return hours
}

// ─── NVMe role assignment ─────────────────────────────────────────────────────

// assignNVMeRoles partitions the remaining NVMe drives into SLOG candidates
// and L2ARC candidates.  SLOG requires a mirror (2+ drives).  A single NVMe
// is L2ARC only to avoid data loss on failure.
func (b *storageBuilder) assignNVMeRoles(nvmes []hardware.Disk) (slogDrives, l2arcDrives []hardware.Disk) {
	if len(nvmes) == 0 {
		return nil, nil
	}
	if len(nvmes) == 1 {
		b.warn(fmt.Sprintf("only 1 NVMe available (%s) — using as L2ARC only; "+
			"SLOG (ZIL) requires a mirror of 2 NVMe drives to be safe", nvmes[0].Name))
		return nil, nvmes
	}
	// 2+ NVMes: first 2 get SLOG partitions; all get L2ARC on remaining space.
	slogDrives = nvmes[:2]
	l2arcDrives = nvmes // all NVMes contribute L2ARC (p2 partition)
	return slogDrives, l2arcDrives
}

// buildSLOGPool creates a mirrored SLOG (ZIL) pool using small partitions on
// the first 2 NVMe drives.  Each partition is named <devname>p1.
func buildSLOGPool(slogDrives []hardware.Disk) api.ZFSPool {
	members := []string{
		slogDrives[0].Name + "p1",
		slogDrives[1].Name + "p1",
	}
	return api.ZFSPool{
		Name:       "slog",
		VdevType:   "mirror",
		Members:    members,
		Mountpoint: "", // SLOG is a special device, not a filesystem mountpoint
		Properties: map[string]string{
			"clustr:role":           "slog",
			"clustr:partition_size": fmt.Sprintf("%d", slogPartitionBytes),
		},
	}
}

// buildL2ARCPool creates an L2ARC cache pool.  When SLOG was already allocated
// (hasSLOG=true), NVMe drives are referenced by their second partition (p2) to
// avoid overlapping with the p1 SLOG partition.  Non-NVMe SSDs use the full
// device since they are not partitioned for SLOG.
func buildL2ARCPool(drives []hardware.Disk, hasSLOG bool) api.ZFSPool {
	members := make([]string, len(drives))
	for i, d := range drives {
		if hasSLOG && isNVMe(d) {
			members[i] = d.Name + "p2"
		} else {
			members[i] = d.Name
		}
	}
	return api.ZFSPool{
		Name:       "l2arc",
		VdevType:   "stripe",
		Members:    members,
		Mountpoint: "", // L2ARC is a cache device, not a filesystem mountpoint
		Properties: map[string]string{
			"clustr:role": "l2arc",
		},
	}
}

// ─── Drive classification helpers ────────────────────────────────────────────

// classifyDisks partitions a slice of hardware.Disk into HDD, SSD, and NVMe
// buckets.  Boot/USB drives are excluded.  Each bucket is sorted size-ascending.
//
// Classification rules (in priority order):
//  1. Transport "nvme" OR name prefix "nvme" → NVMe bucket.
//  2. Rotational=true                        → HDD bucket.
//  3. Everything else (rotational=false, non-NVMe transport) → SSD bucket.
func classifyDisks(disks []hardware.Disk) (hdds, ssds, nvmes []hardware.Disk) {
	for _, d := range disks {
		// Skip boot and USB drives (same criteria as the general recommender).
		if isBootDisk(d) || strings.EqualFold(d.Transport, "usb") {
			continue
		}
		switch {
		case isNVMe(d):
			nvmes = append(nvmes, d)
		case d.Rotational:
			hdds = append(hdds, d)
		default:
			ssds = append(ssds, d)
		}
	}
	sortBySize := func(s []hardware.Disk) {
		sort.Slice(s, func(i, j int) bool { return s[i].Size < s[j].Size })
	}
	sortBySize(hdds)
	sortBySize(ssds)
	sortBySize(nvmes)
	return
}

// isNVMe returns true when the disk is NVMe based on transport string or name prefix.
func isNVMe(d hardware.Disk) bool {
	return strings.EqualFold(d.Transport, "nvme") ||
		strings.HasPrefix(strings.ToLower(d.Name), "nvme")
}

// removeDrives returns a new slice with drives whose names are in the exclusion set removed.
func removeDrives(drives []hardware.Disk, exclude map[string]bool) []hardware.Disk {
	out := drives[:0:0] // zero-length, same backing array is fine since we copy
	for _, d := range drives {
		if !exclude[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// concat returns a single slice concatenating all input slices.
func concat(slices ...[]hardware.Disk) []hardware.Disk {
	var total int
	for _, s := range slices {
		total += len(s)
	}
	out := make([]hardware.Disk, 0, total)
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// fmtTB formats bytes as TB (1 TB = 10^12 bytes, matching marketing convention)
// for human-readable capacity strings.  Values below 1 TB fall back to fmtGB.
func fmtTB(bytes int64) string {
	const tb = int64(1e12)
	if bytes >= tb {
		return fmt.Sprintf("%.0f TB", float64(bytes)/float64(tb))
	}
	return fmtGB(bytes)
}
