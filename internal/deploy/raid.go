package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/hardware"
)

// CreateRAIDArrays assembles md arrays according to the DiskLayout spec.
// It must be called before partitioning — the resulting md devices are then
// treated as ordinary block devices by the rest of the deployment flow.
func CreateRAIDArrays(ctx context.Context, layout api.DiskLayout, hw hardware.SystemInfo) error {
	for _, spec := range layout.RAIDArrays {
		if err := createRAIDArray(ctx, spec, hw); err != nil {
			return fmt.Errorf("deploy/raid: create %s: %w", spec.Name, err)
		}
	}
	return nil
}

// createRAIDArray creates a single md array from a RAIDSpec.
func createRAIDArray(ctx context.Context, spec api.RAIDSpec, hw hardware.SystemInfo) error {
	if spec.Name == "" {
		return fmt.Errorf("raid spec missing name")
	}
	if spec.Level == "" {
		return fmt.Errorf("raid spec %s missing level", spec.Name)
	}
	if len(spec.Members) == 0 {
		return fmt.Errorf("raid spec %s has no members", spec.Name)
	}

	// Resolve member device paths. Members may be explicit device names (e.g.
	// "sda") or size-based selectors ("smallest-N").
	members, err := resolveRAIDMembers(spec.Members, hw)
	if err != nil {
		return fmt.Errorf("resolve members: %w", err)
	}

	// Validate spare count before computing --raid-devices to prevent passing
	// zero or negative values to mdadm, which would cause mdadm corruption.
	if spec.Spare >= len(members) {
		return fmt.Errorf("RAID array %s: spare count (%d) must be less than member count (%d)", spec.Name, spec.Spare, len(members))
	}

	devPath := "/dev/" + spec.Name

	// Stop any existing array on this device first (idempotent).
	_ = exec.CommandContext(ctx, "mdadm", "--stop", devPath).Run()

	// Wipe member devices to clear any existing superblocks.
	for _, m := range members {
		_ = exec.CommandContext(ctx, "mdadm", "--zero-superblock", m).Run()
		_ = exec.CommandContext(ctx, "wipefs", "-a", m).Run()
	}

	// Build mdadm create arguments.
	numDevices := len(members)
	args := []string{
		"--create", devPath,
		"--level", spec.Level,
		"--raid-devices", strconv.Itoa(numDevices - spec.Spare),
		"--run", // don't wait for user confirmation
	}

	if spec.ChunkKB > 0 {
		args = append(args, "--chunk", strconv.Itoa(spec.ChunkKB)+"K")
	}
	if spec.Spare > 0 {
		args = append(args, "--spare-devices", strconv.Itoa(spec.Spare))
	}

	args = append(args, members...)

	log := logger()
	log.Info().Str("device", devPath).Str("level", spec.Level).Strs("members", members).Msg("creating RAID array")
	if err := runAndLog(ctx, "mdadm", exec.CommandContext(ctx, "mdadm", args...)); err != nil {
		return fmt.Errorf("mdadm create: %w", err)
	}
	log.Info().Str("device", devPath).Str("level", spec.Level).Msg("RAID array created")

	// Wait for udev to settle so the new md device is visible.
	_ = runCmd(ctx, "udevadm", "settle")

	return nil
}

// DestroyRAIDArrays stops all md arrays that use any of the provided devices
// as members. Used during pre-deployment cleanup.
func DestroyRAIDArrays(ctx context.Context, devices []string) error {
	// Read /proc/mdstat to find active arrays.
	raw, err := os.ReadFile("/proc/mdstat")
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no md support
		}
		return fmt.Errorf("deploy/raid: read mdstat: %w", err)
	}

	// Find md devices that contain any of the target devices.
	arraysToStop := findArraysForDevices(string(raw), devices)

	for _, mdName := range arraysToStop {
		mdDev := "/dev/" + mdName
		logger().Info().Str("device", mdDev).Msg("stopping RAID array")
		if err := runCmd(ctx, "mdadm", "--stop", mdDev); err != nil {
			logger().Warn().Str("device", mdDev).Err(err).Msg("mdadm --stop failed (non-fatal)")
		}
		// Wipe the superblocks on all members.
		members := findMembersForArray(string(raw), mdName)
		for _, m := range members {
			_ = exec.CommandContext(ctx, "mdadm", "--zero-superblock", "/dev/"+m).Run()
		}
	}

	return nil
}

// GenerateMdadmConf writes /etc/mdadm.conf inside the deployed filesystem so
// the initramfs can assemble the arrays on next boot.
//
// This function only writes the conf file — it does NOT regenerate the
// initramfs. The caller (BlockDeployer.Deploy) ensures that applyBootConfig
// runs AFTER this function, so the single dracut invocation in applyBootConfig
// picks up the freshly-written mdadm.conf. A separate dracut run here would
// overwrite the initramfs that applyBootConfig builds with RAID module support.
func GenerateMdadmConf(ctx context.Context, mountRoot string) error {
	etcDir := filepath.Join(mountRoot, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return fmt.Errorf("deploy/raid: mkdir %s: %w", etcDir, err)
	}

	confPath := filepath.Join(etcDir, "mdadm.conf")

	// Use mdadm --detail --scan to generate the conf from the running arrays.
	cmd := exec.CommandContext(ctx, "mdadm", "--detail", "--scan")
	out, err := cmd.Output()
	if err != nil {
		// Non-fatal: if there are no active arrays or mdadm isn't available,
		// we skip conf generation. The node can still boot without it if the
		// kernel finds the superblocks.
		logger().Warn().Err(err).Msg("mdadm --detail --scan failed (non-fatal) — skipping mdadm.conf generation")
		return nil
	}

	content := "# Generated by clustr during deployment.\n# Do not edit manually.\n\nMAILADDR root\n\n" + string(out)
	if err := os.WriteFile(confPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("deploy/raid: write mdadm.conf: %w", err)
	}
	logger().Info().Str("path", confPath).Msg("wrote mdadm.conf")

	return nil
}

// resolveRAIDMembers converts member specs to absolute device paths.
// Supported formats:
//   - "sda", "nvme0n1" → "/dev/sda", "/dev/nvme0n1"
//   - "/dev/sda"       → "/dev/sda" (already absolute)
//   - "smallest-N"     → the N smallest non-boot disks from hw
func resolveRAIDMembers(specs []string, hw hardware.SystemInfo) ([]string, error) {
	var resolved []string

	for _, spec := range specs {
		if strings.HasPrefix(spec, "/dev/") {
			resolved = append(resolved, spec)
			continue
		}

		if strings.HasPrefix(spec, "smallest-") {
			n, err := strconv.Atoi(strings.TrimPrefix(spec, "smallest-"))
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid member selector %q", spec)
			}
			disks := smallestNDisks(hw.Disks, n)
			if len(disks) < n {
				return nil, fmt.Errorf("selector %q requires %d disks, only %d available", spec, n, len(disks))
			}
			for _, d := range disks {
				resolved = append(resolved, "/dev/"+d.Name)
			}
			continue
		}

		// Plain device name.
		resolved = append(resolved, "/dev/"+spec)
	}

	return resolved, nil
}

// smallestNDisks returns the N smallest non-boot disks sorted by size ascending.
func smallestNDisks(disks []hardware.Disk, n int) []hardware.Disk {
	// Filter boot disks.
	var candidates []hardware.Disk
	for _, d := range disks {
		if !isBootDisk(d) {
			candidates = append(candidates, d)
		}
	}

	// Simple selection sort for N smallest — N is typically 2-8.
	result := make([]hardware.Disk, 0, n)
	used := make([]bool, len(candidates))
	for i := 0; i < n && i < len(candidates); i++ {
		best := -1
		for j, d := range candidates {
			if used[j] {
				continue
			}
			if best == -1 || d.Size < candidates[best].Size {
				best = j
			}
		}
		if best != -1 {
			result = append(result, candidates[best])
			used[best] = true
		}
	}
	return result
}

// findArraysForDevices scans /proc/mdstat content and returns md array names
// that contain any of the given device base names.
func findArraysForDevices(mdstat string, devices []string) []string {
	// Build a set of device base names for fast lookup.
	devSet := make(map[string]bool)
	for _, d := range devices {
		base := filepath.Base(d)
		devSet[base] = true
	}

	var arrays []string
	for _, line := range strings.Split(mdstat, "\n") {
		if !strings.HasPrefix(line, "md") || !strings.Contains(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mdName := fields[0]
		for _, f := range fields[3:] {
			// Strip role annotation to get bare device name.
			devName := f
			if idx := strings.Index(devName, "["); idx != -1 {
				devName = devName[:idx]
			}
			if devSet[devName] {
				arrays = append(arrays, mdName)
				break
			}
		}
	}
	return arrays
}

// findMembersForArray returns the member device base names for a given md array
// from /proc/mdstat content.
func findMembersForArray(mdstat, mdName string) []string {
	for _, line := range strings.Split(mdstat, "\n") {
		if !strings.HasPrefix(line, mdName+" ") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		rest := strings.Fields(line[colonIdx+1:])
		var members []string
		for _, f := range rest[2:] { // skip state + level
			devName := f
			if idx := strings.Index(devName, "["); idx != -1 {
				devName = devName[:idx]
			}
			if idx := strings.Index(devName, "("); idx != -1 {
				devName = devName[:idx]
			}
			if devName != "" {
				members = append(members, devName)
			}
		}
		return members
	}
	return nil
}

// waitForRAIDSync polls /proc/mdstat until all md arrays have finished their
// initial resync (or recovery). This is called before applyBootConfig on RAID
// layouts to ensure a clean array state before heavy I/O from dracut.
//
// A newly created RAID1 array starts an initial sync immediately after creation.
// While the filesystem is writable during resync, concurrent heavy sequential
// I/O (e.g. dracut writing a large initramfs) on virtual disks can trigger
// transient I/O errors during resync, leaving the array degraded before first boot.
// Waiting for the resync removes this race condition.
//
// The function times out after the context deadline or after a hard 10-minute
// wall limit, whichever comes first. A resync in progress is logged every 30s so
// the operator can follow progress. A timeout is non-fatal: deploy continues.
func waitForRAIDSync(ctx context.Context) {
	log := logger()
	const maxWait = 10 * time.Minute
	const pollInterval = 5 * time.Second
	const logInterval = 30 * time.Second

	deadline := time.Now().Add(maxWait)
	lastLog := time.Now()

	log.Info().Msg("finalize/raid: waiting for RAID array initial resync to complete before applyBootConfig")

	for {
		// Check context cancellation.
		select {
		case <-ctx.Done():
			log.Warn().Msg("finalize/raid: context cancelled while waiting for RAID resync — proceeding anyway")
			return
		default:
		}

		if time.Now().After(deadline) {
			log.Error().Dur("waited", maxWait).
				Msg("RAID array still resyncing after timeout; deploy continuing but node may have degraded boot reliability")
			return
		}

		raw, err := os.ReadFile("/proc/mdstat")
		if err != nil {
			log.Warn().Err(err).Msg("finalize/raid: could not read /proc/mdstat — skipping resync wait")
			return
		}

		// /proc/mdstat lines look like:
		//   md1 : active raid1 sdb2[1] sda2[0]
		//         123456 blocks [2/2] [UU]
		//         [=======>.........]  resync = 42.5% (52428/123456) finish=1.2min speed=12345K/sec
		//
		// A resync/check/recovery in progress is indicated by a line starting with
		// spaces that contains "resync", "recovery", or "check" followed by a percentage.
		resyncing := false
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "resync") ||
				strings.HasPrefix(trimmed, "recovery") ||
				strings.HasPrefix(trimmed, "check") {
				resyncing = true
				if time.Since(lastLog) >= logInterval {
					log.Info().Str("mdstat_line", trimmed).
						Msg("finalize/raid: RAID resync in progress — waiting")
					lastLog = time.Now()
				}
				break
			}
		}

		if !resyncing {
			log.Info().Msg("finalize/raid: all RAID arrays synced — proceeding with applyBootConfig")
			return
		}

		// Poll again after interval.
		select {
		case <-ctx.Done():
			log.Warn().Msg("finalize/raid: context cancelled while waiting for RAID resync — proceeding anyway")
			return
		case <-time.After(pollInterval):
		}
	}
}
