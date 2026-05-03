package clientd

// operator_disk_capture.go — node-side handler for disk_capture_request (#151).
//
// When the server sends a "disk_capture_request" the node runs lsblk -J, parses
// the device tree, optionally enriches RAID arrays via mdadm --detail, and sends
// back a "disk_capture_result" message carrying a JSON-serialised api.DiskLayout.
//
// Design constraints:
//   - lsblk is always required; missing → immediate error response.
//   - mdadm / lvs are optional enrichment; if missing or permission-denied we
//     emit a best-effort layout from lsblk alone.
//   - No root privilege is required for lsblk basic output.  mdadm --detail
//     sometimes needs root; if it fails we skip supplementary RAID metadata.
//   - We do NOT add new sudoers entries or polkit rules.  If the node's
//     clustr-privhelper ever grows a disk-capture verb we can route through it,
//     but in v1 we run lsblk directly and tolerate mdadm/lvs permission failures.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	diskCaptureTimeout = 30 * time.Second
	// lsblkOutputCap caps the lsblk JSON output at 4 MB — a node with hundreds of
	// block devices and partitions would still comfortably fit within this limit.
	lsblkOutputCap = 4 << 20
)

// lsblkColumns is the column list passed to lsblk -J -o.
// Kept as a constant so tests can verify the exact invocation.
const lsblkColumns = "NAME,TYPE,SIZE,FSTYPE,MOUNTPOINT,UUID,PARTUUID,LABEL,MODEL,SERIAL,PKNAME,RM,RO,TRAN,VENDOR"

// lsblkOutput is the top-level object returned by lsblk -J.
type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

// lsblkSize is a helper type that unmarshals lsblk's "size" field, which lsblk
// versions prior to ~2.37 emit as a JSON number and newer versions emit as a
// JSON string when -b (bytes) is requested. We handle both forms.
type lsblkSize int64

func (s *lsblkSize) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	// String form: "53687091200"
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		v := parseSize(str)
		*s = lsblkSize(v)
		return nil
	}
	// Null form
	if string(b) == "null" {
		*s = 0
		return nil
	}
	// Number form: 53687091200
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*s = lsblkSize(n)
	return nil
}

// lsblkDevice mirrors one entry from lsblk -J output.
// All optional string fields use *string so we can distinguish null from "".
type lsblkDevice struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"`
	Size       lsblkSize     `json:"size"` // handles both JSON string and number
	FSType     *string       `json:"fstype"`
	Mountpoint *string       `json:"mountpoint"`
	UUID       *string       `json:"uuid"`
	PartUUID   *string       `json:"partuuid"`
	Label      *string       `json:"label"`
	Model      *string       `json:"model"`
	Serial     *string       `json:"serial"`
	PKName     *string       `json:"pkname"`
	RM         bool          `json:"rm"`
	RO         bool          `json:"ro"`
	Tran       *string       `json:"tran"`
	Vendor     *string       `json:"vendor"`
	Children   []lsblkDevice `json:"children,omitempty"`
}

// derefStr returns the string value of a *string, or "" if nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// handleDiskCaptureRequest is the pure logic function. It captures the local
// disk layout and returns a DiskCaptureResultPayload ready to be sent over the
// WebSocket. Keeping it decoupled from the Client struct simplifies unit testing.
func handleDiskCaptureRequest(payload DiskCaptureRequestPayload) DiskCaptureResultPayload {
	ref := payload.RefMsgID

	layout, err := captureDiskLayout()
	if err != nil {
		log.Error().Err(err).Str("ref_msg_id", ref).
			Msg("clientd disk-capture: capture failed")
		return DiskCaptureResultPayload{
			RefMsgID: ref,
			Error:    err.Error(),
		}
	}

	layoutBytes, err := json.Marshal(layout)
	if err != nil {
		log.Error().Err(err).Str("ref_msg_id", ref).
			Msg("clientd disk-capture: failed to serialise layout")
		return DiskCaptureResultPayload{
			RefMsgID: ref,
			Error:    "failed to serialise layout: " + err.Error(),
		}
	}

	log.Info().
		Str("ref_msg_id", ref).
		Int("partitions", len(layout.Partitions)).
		Int("raid_arrays", len(layout.RAIDArrays)).
		Msg("clientd disk-capture: layout captured successfully")

	return DiskCaptureResultPayload{
		RefMsgID:   ref,
		LayoutJSON: string(layoutBytes),
	}
}

// captureDiskLayout runs lsblk, parses the device tree, enriches RAID arrays
// via mdadm, and returns a best-effort api.DiskLayout.
func captureDiskLayout() (api.DiskLayout, error) {
	// Verify lsblk is present — it is mandatory.
	lsblkPath, err := exec.LookPath("lsblk")
	if err != nil {
		return api.DiskLayout{}, fmt.Errorf("lsblk not available on node")
	}

	raw, err := runLsblk(lsblkPath)
	if err != nil {
		return api.DiskLayout{}, fmt.Errorf("lsblk failed: %w", err)
	}

	var out lsblkOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return api.DiskLayout{}, fmt.Errorf("failed to parse lsblk output: %w", err)
	}

	return buildDiskLayout(out)
}

// runLsblk executes lsblk -J with the fixed column list and returns the raw JSON.
func runLsblk(lsblkPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), diskCaptureTimeout)
	defer cancel()

	args := []string{"-J", "-b", "-o", lsblkColumns}
	cmd := exec.CommandContext(ctx, lsblkPath, args...) //#nosec G204 -- fixed path from LookPath, fixed args
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, remaining: lsblkOutputCap}
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// buildDiskLayout converts the parsed lsblk tree into an api.DiskLayout.
func buildDiskLayout(out lsblkOutput) (api.DiskLayout, error) {
	var layout api.DiskLayout

	// Collect md device names from lsblk for RAID array detection.
	// lsblk type values for md arrays: "raid0", "raid1", "raid5", "raid6", "raid10".
	// We normalise these to api.RAIDSpec.Level strings.
	mdDevices := map[string]string{} // name → lsblk type (e.g. "md0" → "raid1")

	// Collect member disk→md mappings to populate RAIDSpec.Members.
	// key: md device name, value: list of member device names.
	mdMembers := map[string][]string{}

	// Identify the disk containing the root mount point — used for TargetDevice.
	rootDisk := ""

	for _, dev := range out.BlockDevices {
		switch {
		case strings.HasPrefix(dev.Type, "raid"):
			// md array appears as a top-level device in lsblk output.
			mdDevices[dev.Name] = dev.Type
			// Members are the partition children that have fstype=linux_raid_member.
			// lsblk doesn't directly list md members in a flat view — we collect
			// member partitions by scanning all disks below.

		case dev.Type == "disk":
			// Scan partitions and gather RAID membership.
			for _, child := range dev.Children {
				if derefStr(child.FSType) == "linux_raid_member" {
					// This partition is a RAID member. We don't know the md name yet
					// from lsblk alone, but we record the partition→disk relationship.
					// mdadm enrichment (below) will fill in the array name.
					_ = child
				}
				// Detect root mountpoint.
				if derefStr(child.Mountpoint) == "/" {
					rootDisk = dev.Name
				}
			}
			// Root might be directly on the disk (no partitions).
			if derefStr(dev.Mountpoint) == "/" {
				rootDisk = dev.Name
			}
		}
	}

	// Enrichment pass: mdadm --detail for each detected md array.
	if mdadmPath, err := exec.LookPath("mdadm"); err == nil {
		for mdName := range mdDevices {
			members, err := mdadmGetMembers(mdadmPath, mdName)
			if err != nil {
				// Permission denied or mdadm unavailable for this array — skip.
				log.Debug().Err(err).Str("md", mdName).
					Msg("clientd disk-capture: mdadm detail skipped (non-fatal)")
				continue
			}
			mdMembers[mdName] = members
		}
	}

	// Build RAIDArrays from md devices.
	for mdName, lsblkType := range mdDevices {
		level := lsblkTypeToRAIDLevel(lsblkType)
		spec := api.RAIDSpec{
			Name:    mdName,
			Level:   level,
			Members: mdMembers[mdName],
		}
		layout.RAIDArrays = append(layout.RAIDArrays, spec)
	}

	// Build Partitions from all disk children (and md array children).
	for _, dev := range out.BlockDevices {
		if dev.Type == "disk" {
			for _, child := range dev.Children {
				if child.Type == "part" {
					spec := lsblkDeviceToPartitionSpec(child)
					layout.Partitions = append(layout.Partitions, spec)
				}
			}
		}
		// Partitions on top of md arrays.
		if strings.HasPrefix(dev.Type, "raid") {
			for _, child := range dev.Children {
				if child.Type == "part" {
					spec := lsblkDeviceToPartitionSpec(child)
					spec.Device = dev.Name // partition on md device
					layout.Partitions = append(layout.Partitions, spec)
				}
			}
		}
	}

	// Set TargetDevice from the disk that holds the root partition.
	layout.TargetDevice = rootDisk

	// Detect bootloader.
	layout.Bootloader = detectBootloader()

	return layout, nil
}

// lsblkDeviceToPartitionSpec converts a partition lsblkDevice to api.PartitionSpec.
func lsblkDeviceToPartitionSpec(dev lsblkDevice) api.PartitionSpec {
	sizeBytes := int64(dev.Size)
	fs := derefStr(dev.FSType)
	mp := derefStr(dev.Mountpoint)
	label := derefStr(dev.Label)

	// Map lsblk filesystem names to api.PartitionSpec Filesystem values.
	// "linux_raid_member" → skip FS (it's a RAID member, not a formatted partition).
	// "swap" → "swap"
	// everything else passes through as-is.
	if fs == "linux_raid_member" {
		fs = ""
	}

	var flags []string
	// Detect ESP (EFI System Partition) by filesystem type.
	if fs == "vfat" && (mp == "/boot/efi" || mp == "/efi") {
		flags = append(flags, "esp", "boot")
	}
	// Detect swap.
	if fs == "swap" {
		mp = "none"
	}

	return api.PartitionSpec{
		Label:      label,
		SizeBytes:  sizeBytes,
		Filesystem: fs,
		MountPoint: mp,
		Flags:      flags,
	}
}

// parseSize converts a decimal byte count string (as emitted by lsblk -b) to int64.
// Returns 0 on parse failure.
func parseSize(s string) int64 {
	if s == "" {
		return 0
	}
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	if err != nil {
		return 0
	}
	return n
}

// lsblkTypeToRAIDLevel maps lsblk device type strings to api.RAIDSpec level strings.
func lsblkTypeToRAIDLevel(t string) string {
	switch t {
	case "raid0":
		return "raid0"
	case "raid1":
		return "raid1"
	case "raid4":
		return "raid4"
	case "raid5":
		return "raid5"
	case "raid6":
		return "raid6"
	case "raid10":
		return "raid10"
	default:
		return t // pass through unknown types unchanged
	}
}

// mdadmGetMembers runs `mdadm --detail /dev/<name>` and parses the member device
// list from the output. Returns the member names (e.g. ["sda1", "sdb1"]).
// Returns an error if mdadm fails (e.g. permission denied) so the caller can skip.
func mdadmGetMembers(mdadmPath, mdName string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	devPath := "/dev/" + mdName
	cmd := exec.CommandContext(ctx, mdadmPath, "--detail", devPath) //#nosec G204 -- mdadmPath from LookPath, devPath is /dev/ + mdName from lsblk output
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Parse the "  /dev/sda1  ..." member lines from mdadm --detail output.
	// The format has lines like:
	//   Number   Major   Minor   RaidDevice State
	//      0       8        1        0      active sync   /dev/sda1
	var members []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Lines with member devices end with "/dev/..."
		if idx := strings.LastIndex(line, "/dev/"); idx >= 0 {
			devField := line[idx:]
			// Strip the /dev/ prefix to get kernel name (e.g. "sda1").
			name := strings.TrimPrefix(devField, "/dev/")
			name = strings.TrimSpace(name)
			// Skip the array device itself.
			if name == mdName {
				continue
			}
			if name != "" {
				members = append(members, name)
			}
		}
	}
	return members, nil
}

// detectBootloader inspects the running system to determine the bootloader type
// and target. It is best-effort — returns a zero-value Bootloader on any failure.
func detectBootloader() api.Bootloader {
	// systemd-boot: look for /sys/firmware/efi/efivars being present and
	// the bootloader entry in efivarfs.
	// Simplest heuristic: if /sys/firmware/efi exists → UEFI firmware.
	// Check for grub2 vs systemd-boot by looking for known binaries/dirs.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Detect UEFI vs BIOS via /sys/firmware/efi.
	checkEFI := exec.CommandContext(ctx, "test", "-d", "/sys/firmware/efi") //#nosec G204 -- fixed args
	isUEFI := checkEFI.Run() == nil

	// Detect grub2 by checking if grub2-install or grub2.cfg exists.
	grubPresent := false
	for _, p := range []string{"/boot/grub2/grub.cfg", "/boot/grub/grub.cfg"} {
		chk := exec.CommandContext(ctx, "test", "-f", p) //#nosec G204 -- fixed args, fixed paths
		if chk.Run() == nil {
			grubPresent = true
			break
		}
	}

	// Detect systemd-boot by checking /boot/efi/EFI/systemd/.
	sdbootPresent := false
	{
		chk := exec.CommandContext(ctx, "test", "-d", "/boot/efi/EFI/systemd") //#nosec G204 -- fixed args
		sdbootPresent = chk.Run() == nil
	}

	bl := api.Bootloader{}
	switch {
	case sdbootPresent:
		bl.Type = "systemd-boot"
		bl.Target = "x86_64-efi"
	case grubPresent && isUEFI:
		bl.Type = "grub2"
		bl.Target = "x86_64-efi"
	case grubPresent && !isUEFI:
		bl.Type = "grub2"
		bl.Target = "i386-pc"
	}
	return bl
}
