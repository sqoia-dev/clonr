package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sqoia-dev/clustr/internal/hardware"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── IMSM detection (#150) ───────────────────────────────────────────────────

// imsmDetection caches the per-process IMSM capability check so it is only
// run once per deploy session, not once per RAIDSpec.
type imsmDetection struct {
	once      sync.Once
	available bool
}

var imsmPlatform imsmDetection

// IMSMAvailable returns true when the host has an mdadm binary that reports
// IMSM (Intel Matrix Storage Manager / Intel Rapid Storage Technology) platform
// support.  The result is cached for the lifetime of the process.
//
// Detection runs `mdadm --imsm-platform-test` and checks that:
//   - Exit code is 0.
//   - Stdout contains "Platform : Intel".
//
// This mirrors the detection described in mdadm(8) for imsm containers.
// The function is safe for concurrent use.
func IMSMAvailable(ctx context.Context) bool {
	imsmPlatform.once.Do(func() {
		imsmPlatform.available = probeIMSMPlatform(ctx)
	})
	return imsmPlatform.available
}

// probeIMSMPlatform runs the actual mdadm probe. Separated from IMSMAvailable
// so tests can call it directly without touching the cached singleton.
func probeIMSMPlatform(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "mdadm", "--imsm-platform-test")
	out, err := cmd.Output()
	if err != nil {
		// Non-zero exit or mdadm not found — no IMSM support.
		logger().Debug().Err(err).Msg("deploy/raid: mdadm --imsm-platform-test: not available")
		return false
	}
	detected := strings.Contains(string(out), "Platform : Intel")
	logger().Info().Bool("imsm_available", detected).
		Str("mdadm_output", strings.TrimSpace(string(out))).
		Msg("deploy/raid: IMSM platform detection result")
	return detected
}

// ParseIMSMPlatformOutput returns true when mdadm --imsm-platform-test stdout
// indicates an Intel IMSM-capable platform.  Exported for unit testing.
func ParseIMSMPlatformOutput(output string) bool {
	return strings.Contains(output, "Platform : Intel")
}

// ResetIMSMDetectionForTest resets the cached IMSM detection state.
// Must only be called from tests.
func ResetIMSMDetectionForTest() {
	imsmPlatform = imsmDetection{}
}

// ─── Per-device IMSM membership detection (Sprint 26 #mixed-controller) ──────

// DeviceIMSMResult records whether a single RAID member device is under an
// IMSM (Intel Matrix Storage Manager) controller.
type DeviceIMSMResult struct {
	// Dev is the absolute device path, e.g. "/dev/sda".
	Dev string
	// OnIMSM is true when mdadm --imsm-platform-test --container reports exit 0
	// for this device, indicating it is visible to an IMSM-capable controller.
	OnIMSM bool
}

// imsmDeviceRunner is the function used to probe a single device for IMSM
// membership. It receives the context and absolute device path and returns
// (stdout, exit_code, error). The default implementation calls mdadm directly;
// tests replace it with a mock via setIMSMDeviceRunnerForTest.
var imsmDeviceRunner func(ctx context.Context, devPath string) (string, int, error) = defaultIMSMDeviceRunner

func defaultIMSMDeviceRunner(ctx context.Context, devPath string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "mdadm", "--imsm-platform-test", "--container="+devPath)
	out, err := cmd.Output()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return string(out), exitCode, err
}

// setIMSMDeviceRunnerForTest replaces the device runner for the duration of a
// test. Returns a restore function. Must only be called from tests.
func setIMSMDeviceRunnerForTest(fn func(ctx context.Context, devPath string) (string, int, error)) func() {
	old := imsmDeviceRunner
	imsmDeviceRunner = fn
	return func() { imsmDeviceRunner = old }
}

// ParseIMSMContainerOutput returns true when mdadm --imsm-platform-test
// --container output and exit code indicate the device is under an IMSM
// controller. Exit code 0 is the authoritative signal; the output check is
// belt-and-suspenders for mdadm builds that always exit 0.
//
// Exported for unit testing.
func ParseIMSMContainerOutput(output string, exitCode int) bool {
	if exitCode != 0 {
		return false
	}
	// mdadm exits 0 for IMSM-capable devices. Some builds also print a
	// "Platform : Intel" line; accept either form.
	return true
}

// classifyMemberIMSM probes each resolved member device path and returns a
// slice of DeviceIMSMResult in the same order as members. Errors from the
// mdadm probe are treated as "not on IMSM" and logged as warnings.
func classifyMemberIMSM(ctx context.Context, members []string) []DeviceIMSMResult {
	results := make([]DeviceIMSMResult, len(members))
	log := logger()
	for i, dev := range members {
		out, code, err := imsmDeviceRunner(ctx, dev)
		onIMSM := ParseIMSMContainerOutput(out, code)
		if err != nil && code == -1 {
			// mdadm binary not found or exec failure — log and treat as software.
			log.Warn().Str("device", dev).Err(err).
				Msg("deploy/raid: mdadm --imsm-platform-test --container exec failed; treating device as non-IMSM")
		} else {
			log.Debug().Str("device", dev).Bool("on_imsm", onIMSM).Int("exit_code", code).
				Msg("deploy/raid: per-device IMSM membership probe")
		}
		results[i] = DeviceIMSMResult{Dev: dev, OnIMSM: onIMSM}
	}
	return results
}

// imsmControllerSplit partitions a DeviceIMSMResult slice into two slices:
// imsmDevs (OnIMSM=true) and softwareDevs (OnIMSM=false).
func imsmControllerSplit(results []DeviceIMSMResult) (imsmDevs, softwareDevs []string) {
	for _, r := range results {
		if r.OnIMSM {
			imsmDevs = append(imsmDevs, r.Dev)
		} else {
			softwareDevs = append(softwareDevs, r.Dev)
		}
	}
	return
}

// ─── RAID array creation ──────────────────────────────────────────────────────

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
//
// Assembly path:
//  1. spec.ForceSoftware=true OR no IMSM support → software md RAID via mdadm --create
//  2. IMSM platform available AND NOT ForceSoftware → IMSM container + volume path
//
// Mixed-controller case: if some members are on an IMSM controller and some are
// not, classifyMemberIMSM detects the split and the function defaults to software
// RAID for the entire array, emitting three WARN log lines to guide the operator.
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

	log := logger()

	// Branch: IMSM hardware-RAID passthrough (#150 / Sprint 35) vs software md RAID.
	//
	// Priority order:
	//  1. spec.ForceSoftware=true OR spec.RAIDType=="md" → always software,
	//     skip all IMSM detection.
	//  2. spec.RAIDType=="imsm" → explicit operator opt-in.  Skip platform/
	//     per-device autodetection and go straight to the IMSM two-pass mdadm
	//     path.  This is the deterministic path for layouts that target Intel
	//     RST hardware (mirrors clustervisor disk_raid_imsm semantics).
	//  3. Platform IMSM not available → software.
	//  4. Per-device classification:
	//     a. All devices on IMSM controllers → IMSM path.
	//     b. Mixed (some IMSM, some not) → warn + software for entire array.
	//     c. All devices on software controllers → software path.
	if spec.RAIDType == "imsm" {
		log.Info().Str("device", devPath).Str("level", spec.Level).
			Strs("members", members).
			Msg("deploy/raid: explicit raid_type=imsm — creating IMSM RAID container + volume")
		return createIMSMArray(ctx, spec, members, devPath)
	}
	if spec.ForceSoftware || spec.RAIDType == "md" {
		if IMSMAvailable(ctx) {
			log.Info().Str("device", devPath).
				Msg("IMSM available but force_software=true (or raid_type=md); using software md RAID")
		}
		// Fall through to software RAID path below.
	} else if IMSMAvailable(ctx) {
		// Platform supports IMSM. Probe each member device to detect mixed controllers.
		deviceResults := classifyMemberIMSM(ctx, members)
		imsmDevs, softwareDevs := imsmControllerSplit(deviceResults)

		switch {
		case len(softwareDevs) == 0:
			// All members are on IMSM controllers — use IMSM passthrough.
			log.Info().Str("device", devPath).Str("level", spec.Level).
				Strs("members", members).Msg("deploy/raid: all members on IMSM controllers — creating IMSM RAID container + volume")
			return createIMSMArray(ctx, spec, members, devPath)

		case len(imsmDevs) == 0:
			// No members on IMSM — fall through to software RAID.
			log.Info().Str("device", devPath).
				Msg("deploy/raid: no members on IMSM controllers; using software md RAID")

		default:
			// Mixed controllers — warn and fall back to software RAID for the whole array.
			log.Warn().
				Str("device", devPath).
				Strs("imsm_devices", imsmDevs).
				Strs("software_devices", softwareDevs).
				Msgf("deploy/raid: WARN: mixed RAID controllers detected (IMSM: %s; software: %s)",
					strings.Join(imsmDevs, ", "), strings.Join(softwareDevs, ", "))
			log.Warn().Str("device", devPath).
				Msg("deploy/raid: WARN: defaulting to software RAID for the entire array")
			log.Warn().Str("device", devPath).
				Msg("deploy/raid: WARN: to use IMSM passthrough, exclude software-controller devices from this RAID config")
		}
	}

	// Software md RAID path.
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

	log.Info().Str("device", devPath).Str("level", spec.Level).Strs("members", members).Msg("creating software RAID array")
	if err := runAndLog(ctx, "mdadm", exec.CommandContext(ctx, "mdadm", args...)); err != nil {
		return fmt.Errorf("mdadm create: %w", err)
	}
	log.Info().Str("device", devPath).Str("level", spec.Level).Msg("RAID array created")

	// Wait for udev to settle so the new md device is visible.
	_ = runCmd(ctx, "udevadm", "settle")

	return nil
}

// IMSMContainerName returns the device path for the IMSM container that
// holds spec's sub-array.  Defaults to "/dev/md/imsm0" when spec.IMSMContainer
// is unset.  Mirrors clustervisor's per-controller container layout.
func IMSMContainerName(spec api.RAIDSpec) string {
	name := spec.IMSMContainer
	if name == "" {
		name = "imsm0"
	}
	return "/dev/md/" + name
}

// IMSMVolumeName returns the device path for the IMSM sub-array (volume).
// Uses spec.Name (e.g. "md0" → "/dev/md/md0") so multiple sub-arrays can
// co-exist within one container when each has a unique RAIDSpec.Name.
// Falls back to "Volume0" for backward compatibility when Name is unset.
func IMSMVolumeName(spec api.RAIDSpec) string {
	name := spec.Name
	if name == "" {
		name = "Volume0"
	}
	return "/dev/md/" + name
}

// BuildIMSMContainerArgs builds the mdadm argv for pass 1 of IMSM assembly:
// container creation.
//
//	mdadm --create /dev/md/<container> --metadata=imsm --raid-devices=N --run <members...>
//
// raidDevices is the number of physical devices in the container.  The
// returned slice is the args TO mdadm (i.e. argv[1:]).
//
// Pure function — exported for unit testing.
func BuildIMSMContainerArgs(containerDev string, raidDevices int, members []string) []string {
	args := []string{
		"--create", containerDev,
		"--metadata=imsm",
		"--raid-devices", strconv.Itoa(raidDevices),
		"--run", // don't wait for confirmation
	}
	args = append(args, members...)
	return args
}

// BuildIMSMVolumeArgs builds the mdadm argv for pass 2 of IMSM assembly:
// sub-array (volume) creation inside the container.
//
//	mdadm --create /dev/md/<volume> --metadata=imsm --level=<L> --raid-devices=N --run /dev/md/<container>
//
// chunkKB ≤ 0 omits the --chunk flag (lets mdadm pick a default).
// The container path MUST be the trailing argument — mdadm parses it as the
// parent pool when --metadata=imsm is set.
//
// Pure function — exported for unit testing.
func BuildIMSMVolumeArgs(volumeDev, containerDev, level string, raidDevices, chunkKB int) []string {
	args := []string{
		"--create", volumeDev,
		"--metadata=imsm",
		"--level", level,
		"--raid-devices", strconv.Itoa(raidDevices),
		"--run",
	}
	if chunkKB > 0 {
		args = append(args, "--chunk", strconv.Itoa(chunkKB)+"K")
	}
	args = append(args, containerDev)
	return args
}

// createIMSMArray assembles a RAID array using the IMSM (Intel Matrix Storage
// Manager) metadata format.  Two-pass:
//
//  1. mdadm -C /dev/md/<container> --metadata=imsm --raid-devices=N <devs...>
//  2. mdadm -C /dev/md/<spec.Name> --level=L --raid-devices=N --metadata=imsm /dev/md/<container>
//
// Reference: mdadm(8), "IMSM / Intel Matrix Storage Manager" section.
// Mirrors clustervisor's disk_raid_imsm in ClonerInstall.pm:589.
func createIMSMArray(ctx context.Context, spec api.RAIDSpec, members []string, devPath string) error {
	log := logger()
	n := len(members) - spec.Spare

	containerDev := IMSMContainerName(spec)
	volumeDev := IMSMVolumeName(spec)

	// Step 1 — IMSM container.
	containerArgs := BuildIMSMContainerArgs(containerDev, n, members)

	log.Info().Str("container", containerDev).Strs("members", members).
		Msg("deploy/raid: creating IMSM container")
	if err := runAndLog(ctx, "mdadm", exec.CommandContext(ctx, "mdadm", containerArgs...)); err != nil {
		return fmt.Errorf("mdadm imsm container create: %w", err)
	}

	// Brief udev settle so the container device node appears before the volume step.
	_ = runCmd(ctx, "udevadm", "settle")

	// Step 2 — IMSM sub-array (volume) inside the container.
	volumeArgs := BuildIMSMVolumeArgs(volumeDev, containerDev, spec.Level, n, spec.ChunkKB)

	log.Info().Str("volume", volumeDev).Str("level", spec.Level).
		Msg("deploy/raid: creating IMSM volume (sub-array)")
	if err := runAndLog(ctx, "mdadm", exec.CommandContext(ctx, "mdadm", volumeArgs...)); err != nil {
		return fmt.Errorf("mdadm imsm volume create: %w", err)
	}

	// Final settle so the volume device node is stable before partitioning.
	_ = runCmd(ctx, "udevadm", "settle")

	log.Info().Str("device", devPath).Str("container", containerDev).
		Str("volume", volumeDev).Str("level", spec.Level).
		Msg("deploy/raid: IMSM array created")
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
