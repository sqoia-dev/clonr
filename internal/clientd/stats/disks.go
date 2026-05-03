package stats

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DisksPlugin collects per-device disk I/O rates from /proc/diskstats and
// per-filesystem usage via statfs on each mount in /proc/mounts.
//
// Sensors produced (device-scoped, label device=<name>):
//   - "read_bps"   — read bytes/sec  (unit: bps)
//   - "write_bps"  — write bytes/sec (unit: bps)
//   - "read_iops"  — read ops/sec    (unit: iops)
//   - "write_iops" — write ops/sec   (unit: iops)
//
// Sensors produced (mountpoint-scoped, label mount=<path>):
//   - "used_pct"   — filesystem used percent (unit: pct)
type DisksPlugin struct {
	prevDiskStats map[string]diskStat
	prevTime      time.Time
	procDiskstats string // injectable for tests
	procMounts    string // injectable for tests
}

type diskStat struct {
	readsCompleted  uint64
	readSectors     uint64
	writesCompleted uint64
	writeSectors    uint64
}

// NewDisksPlugin creates a DisksPlugin reading /proc/diskstats and /proc/mounts.
func NewDisksPlugin() *DisksPlugin {
	return &DisksPlugin{
		prevDiskStats: make(map[string]diskStat),
		procDiskstats: "/proc/diskstats",
		procMounts:    "/proc/mounts",
	}
}

func (p *DisksPlugin) Name() string { return "disks" }

const sectorBytes = 512 // Linux always uses 512-byte logical sector size in /proc/diskstats

func (p *DisksPlugin) Collect(_ context.Context) []Sample {
	now := time.Now().UTC()
	var samples []Sample

	// --- I/O rates from /proc/diskstats ---
	current, err := parseDiskstats(p.procDiskstats)
	if err == nil && !p.prevTime.IsZero() {
		elapsed := now.Sub(p.prevTime).Seconds()
		if elapsed > 0 {
			for name, cur := range current {
				if prev, ok := p.prevDiskStats[name]; ok {
					readBPS := float64(cur.readSectors-prev.readSectors) * sectorBytes / elapsed
					writeBPS := float64(cur.writeSectors-prev.writeSectors) * sectorBytes / elapsed
					readIOPS := float64(cur.readsCompleted-prev.readsCompleted) / elapsed
					writeIOPS := float64(cur.writesCompleted-prev.writesCompleted) / elapsed
					labels := map[string]string{"device": name}
					samples = append(samples,
						Sample{Sensor: "read_bps", Value: readBPS, Unit: "bps", Labels: labels, TS: now},
						Sample{Sensor: "write_bps", Value: writeBPS, Unit: "bps", Labels: labels, TS: now},
						Sample{Sensor: "read_iops", Value: readIOPS, Unit: "iops", Labels: labels, TS: now},
						Sample{Sensor: "write_iops", Value: writeIOPS, Unit: "iops", Labels: labels, TS: now},
					)
				}
			}
		}
	}
	p.prevDiskStats = current
	p.prevTime = now

	// --- Filesystem usage from statfs ---
	mounts, err := parseMounts(p.procMounts)
	if err == nil {
		for _, mp := range mounts {
			var stat syscall.Statfs_t
			if err := syscall.Statfs(mp, &stat); err != nil {
				continue
			}
			if stat.Blocks == 0 {
				continue
			}
			usedBlocks := stat.Blocks - stat.Bavail
			usedPct := float64(usedBlocks) / float64(stat.Blocks) * 100.0
			samples = append(samples, Sample{
				Sensor: "used_pct",
				Value:  usedPct,
				Unit:   "pct",
				Labels: map[string]string{"mount": mp},
				TS:     now,
			})
		}
	}

	return samples
}

// parseDiskstats reads /proc/diskstats and returns per-device stat counters.
// We only care about physical block devices (skip loop, dm, ram, zram).
func parseDiskstats(path string) (map[string]diskStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]diskStat)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// /proc/diskstats has at least 14 fields per line.
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		// Skip virtual/loopback/ramdisk devices.
		if strings.HasPrefix(name, "loop") ||
			strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") {
			continue
		}
		// Skip partition entries (e.g. sda1, nvme0n1p1) — aggregate at device level.
		if isPartition(name) {
			continue
		}
		reads, _ := strconv.ParseUint(fields[3], 10, 64)
		readSec, _ := strconv.ParseUint(fields[5], 10, 64)
		writes, _ := strconv.ParseUint(fields[7], 10, 64)
		writeSec, _ := strconv.ParseUint(fields[9], 10, 64)
		result[name] = diskStat{
			readsCompleted:  reads,
			readSectors:     readSec,
			writesCompleted: writes,
			writeSectors:    writeSec,
		}
	}
	return result, scanner.Err()
}

// isPartition returns true if name looks like a disk partition rather than a
// whole-disk device. Rules:
//
//   - nvme*: partition if the name contains 'p' after the namespace part
//     (nvme0n1 = disk, nvme0n1p1 = partition).
//   - sd*/vd*/hd*/xvd*: partition if name ends in one or more digits after
//     an initial alphabetic base (sda = disk, sda1 = partition).
//   - loop*, ram*, zram*: always a device (no partition concept for our purposes).
//   - All others: not considered a partition (include in collection).
func isPartition(name string) bool {
	if len(name) == 0 {
		return false
	}

	// NVMe: device names match nvme<ctrl>n<ns> e.g. nvme0n1.
	// Partitions match nvme<ctrl>n<ns>p<part> e.g. nvme0n1p1.
	if strings.HasPrefix(name, "nvme") {
		// A partition has a 'p' followed by digits after the namespace number.
		// Find the last 'p' in the name; if everything after it is digits,
		// and the 'p' is not the only alphabetic after 'nvme', it's a partition.
		lastP := strings.LastIndex(name, "p")
		if lastP > 0 {
			suffix := name[lastP+1:]
			if len(suffix) > 0 {
				allDigits := true
				for _, ch := range suffix {
					if ch < '0' || ch > '9' {
						allDigits = false
						break
					}
				}
				if allDigits {
					return true
				}
			}
		}
		return false
	}

	// loop, ram, zram — never partitions.
	if strings.HasPrefix(name, "loop") ||
		strings.HasPrefix(name, "ram") ||
		strings.HasPrefix(name, "zram") {
		return false
	}

	// sd*, vd*, hd*, xvd*, etc.: partition if ends with digits following an
	// alphabetic base (sda vs sda1).
	// Find the rightmost non-digit; if there are trailing digits it's a partition.
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] < '0' || name[i] > '9' {
			// i is the last alphabetic index. Partition if i < len(name)-1.
			return i < len(name)-1
		}
	}
	return false
}

// interestingFS is the set of filesystem types we report usage for.
// We skip pseudo-filesystems that don't represent real disk space.
var interestingFS = map[string]bool{
	"ext4":       true,
	"ext3":       true,
	"ext2":       true,
	"xfs":        true,
	"btrfs":      true,
	"vfat":       true,
	"nfs":        true,
	"nfs4":       true,
	"tmpfs":      false,
	"sysfs":      false,
	"proc":       false,
	"devtmpfs":   false,
	"cgroup":     false,
	"cgroup2":    false,
	"pstore":     false,
	"securityfs": false,
	"debugfs":    false,
	"tracefs":    false,
	"configfs":   false,
}

// parseMounts returns the list of real filesystem mount points from /proc/mounts.
func parseMounts(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]bool)
	var result []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		mountPoint := fields[1]
		fsType := fields[2]

		// Skip if we explicitly know this is a pseudo-FS.
		if skip, known := interestingFS[fsType]; known && !skip {
			continue
		}
		// Skip /proc and /sys subtrees.
		if strings.HasPrefix(mountPoint, "/proc") ||
			strings.HasPrefix(mountPoint, "/sys") ||
			strings.HasPrefix(mountPoint, "/dev") {
			continue
		}
		if !seen[mountPoint] {
			seen[mountPoint] = true
			result = append(result, mountPoint)
		}
	}
	return result, scanner.Err()
}
