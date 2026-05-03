package clientd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Unit tests: lsblk JSON parse path ───────────────────────────────────────

// TestBuildDiskLayout_Simple parses the simple fixture (one disk, two partitions)
// and asserts the resulting api.DiskLayout has the expected partitions.
func TestBuildDiskLayout_Simple(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "lsblk-simple.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var out lsblkOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	layout, err := buildDiskLayout(out)
	if err != nil {
		t.Fatalf("buildDiskLayout: %v", err)
	}

	// Should have no RAID arrays.
	if len(layout.RAIDArrays) != 0 {
		t.Errorf("RAIDArrays = %d, want 0", len(layout.RAIDArrays))
	}

	// Should have 2 partitions: EFI + root.
	if len(layout.Partitions) != 2 {
		t.Fatalf("Partitions = %d, want 2", len(layout.Partitions))
	}

	// EFI partition: vfat, /boot/efi
	efi := findPartitionByMount(layout.Partitions, "/boot/efi")
	if efi == nil {
		t.Fatal("expected a partition mounted at /boot/efi")
	}
	if efi.Filesystem != "vfat" {
		t.Errorf("EFI partition filesystem = %q, want vfat", efi.Filesystem)
	}
	if !containsFlag(efi.Flags, "esp") {
		t.Errorf("EFI partition flags %v should include 'esp'", efi.Flags)
	}
	if efi.SizeBytes != 1073741824 {
		t.Errorf("EFI partition size = %d, want 1073741824", efi.SizeBytes)
	}

	// Root partition: xfs, /
	root := findPartitionByMount(layout.Partitions, "/")
	if root == nil {
		t.Fatal("expected a partition mounted at /")
	}
	if root.Filesystem != "xfs" {
		t.Errorf("root partition filesystem = %q, want xfs", root.Filesystem)
	}

	// TargetDevice should be "sda" (disk containing root partition).
	if layout.TargetDevice != "sda" {
		t.Errorf("TargetDevice = %q, want sda", layout.TargetDevice)
	}
}

// TestBuildDiskLayout_RAID parses the RAID fixture (two physical disks + md0
// RAID-1 array) and asserts the resulting api.DiskLayout has one RAIDArray
// with level "raid1".
func TestBuildDiskLayout_RAID(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "lsblk-raid.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var out lsblkOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	layout, err := buildDiskLayout(out)
	if err != nil {
		t.Fatalf("buildDiskLayout: %v", err)
	}

	// Should have exactly one RAID array.
	if len(layout.RAIDArrays) != 1 {
		t.Fatalf("RAIDArrays = %d, want 1", len(layout.RAIDArrays))
	}

	arr := layout.RAIDArrays[0]
	if arr.Name != "md0" {
		t.Errorf("RAIDArray Name = %q, want md0", arr.Name)
	}
	if arr.Level != "raid1" {
		t.Errorf("RAIDArray Level = %q, want raid1", arr.Level)
	}

	// Partition members (linux_raid_member type) should not have their filesystem
	// set to "linux_raid_member" — it must be cleared to "".
	for _, p := range layout.Partitions {
		if p.Filesystem == "linux_raid_member" {
			t.Errorf("partition %q has fstype linux_raid_member — should be cleared", p.Label)
		}
	}
}

// TestParseSize covers the lsblk -b size string parser.
func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"53687091200", 53687091200},
		{"1073741824", 1073741824},
		{"0", 0},
		{"", 0},
		{"   1024   ", 1024},
	}
	for _, tc := range cases {
		got := parseSize(tc.in)
		if got != tc.want {
			t.Errorf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestLsblkTypeToRAIDLevel verifies the mapping of lsblk type strings.
func TestLsblkTypeToRAIDLevel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"raid0", "raid0"},
		{"raid1", "raid1"},
		{"raid5", "raid5"},
		{"raid6", "raid6"},
		{"raid10", "raid10"},
		{"unknown-type", "unknown-type"}, // pass-through
	}
	for _, tc := range cases {
		got := lsblkTypeToRAIDLevel(tc.in)
		if got != tc.want {
			t.Errorf("lsblkTypeToRAIDLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestHandleDiskCaptureRequest_LsblkMissing verifies that when lsblk is not on
// PATH the handler returns an error result with a descriptive message.
func TestHandleDiskCaptureRequest_LsblkMissing(t *testing.T) {
	// Override PATH to an empty directory so lsblk cannot be found.
	t.Setenv("PATH", t.TempDir())

	result := handleDiskCaptureRequest(DiskCaptureRequestPayload{RefMsgID: "test-ref-1"})

	if result.Error == "" {
		t.Fatal("expected an error when lsblk is missing, got empty error string")
	}
	if result.LayoutJSON != "" {
		t.Errorf("expected empty LayoutJSON on error, got %q", result.LayoutJSON)
	}
	if result.RefMsgID != "test-ref-1" {
		t.Errorf("RefMsgID = %q, want test-ref-1", result.RefMsgID)
	}
}

// TestHandleDiskCaptureRequest_RefMsgIDEcho verifies that RefMsgID is echoed
// back in the result even on failure paths.
func TestHandleDiskCaptureRequest_RefMsgIDEcho(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	result := handleDiskCaptureRequest(DiskCaptureRequestPayload{RefMsgID: "echo-test-42"})
	if result.RefMsgID != "echo-test-42" {
		t.Errorf("RefMsgID = %q, want echo-test-42", result.RefMsgID)
	}
}

// TestDiskCaptureResultPayload_JSONRoundtrip verifies that DiskCaptureResultPayload
// round-trips through JSON without data loss.
func TestDiskCaptureResultPayload_JSONRoundtrip(t *testing.T) {
	layout := api.DiskLayout{
		TargetDevice: "sda",
		Partitions: []api.PartitionSpec{
			{Label: "EFI", SizeBytes: 1073741824, Filesystem: "vfat", MountPoint: "/boot/efi", Flags: []string{"esp", "boot"}},
			{Label: "", SizeBytes: 52613349376, Filesystem: "xfs", MountPoint: "/", Flags: nil},
		},
		Bootloader: api.Bootloader{Type: "grub2", Target: "x86_64-efi"},
	}

	layoutBytes, err := json.Marshal(layout)
	if err != nil {
		t.Fatalf("marshal layout: %v", err)
	}

	payload := DiskCaptureResultPayload{
		RefMsgID:   "roundtrip-test",
		LayoutJSON: string(layoutBytes),
	}

	// Round-trip through JSON (simulates wire encoding).
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	var decoded DiskCaptureResultPayload
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if decoded.RefMsgID != payload.RefMsgID {
		t.Errorf("RefMsgID = %q, want %q", decoded.RefMsgID, payload.RefMsgID)
	}
	if decoded.Error != "" {
		t.Errorf("unexpected Error = %q", decoded.Error)
	}

	// Unmarshal the inner LayoutJSON back to api.DiskLayout.
	var decoded2 api.DiskLayout
	if err := json.Unmarshal([]byte(decoded.LayoutJSON), &decoded2); err != nil {
		t.Fatalf("unmarshal LayoutJSON: %v", err)
	}
	if decoded2.TargetDevice != "sda" {
		t.Errorf("TargetDevice = %q, want sda", decoded2.TargetDevice)
	}
	if len(decoded2.Partitions) != 2 {
		t.Errorf("Partitions = %d, want 2", len(decoded2.Partitions))
	}
}

// ─── Integration test: dispatch round-trip via fake send channel ──────────────

// TestDispatch_DiskCaptureRequest_Integration fires a disk_capture_request at
// the Client's handleDiskCaptureRequest method (which dispatches in a goroutine)
// and asserts that within 5 s a disk_capture_result arrives on the send channel.
//
// Because lsblk may or may not be present on the CI runner, we only assert on
// the message type and RefMsgID echo — not the layout content.
func TestDispatch_DiskCaptureRequest_Integration(t *testing.T) {
	c := &Client{
		send: make(chan []byte, 8),
	}

	const testRefMsgID = "integ-test-ref-999"
	requestPayload, err := json.Marshal(DiskCaptureRequestPayload{RefMsgID: testRefMsgID})
	if err != nil {
		t.Fatalf("marshal request payload: %v", err)
	}

	msg := ServerMessage{
		Type:    "disk_capture_request",
		MsgID:   testRefMsgID,
		Payload: json.RawMessage(requestPayload),
	}

	// Dispatch — handler runs in goroutine.
	go c.handleDiskCaptureRequest(msg)

	select {
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disk_capture_result on send channel (5s)")

	case data := <-c.send:
		var resultMsg ClientMessage
		if err := json.Unmarshal(data, &resultMsg); err != nil {
			t.Fatalf("unmarshal result ClientMessage: %v", err)
		}
		if resultMsg.Type != "disk_capture_result" {
			t.Errorf("ClientMessage.Type = %q, want disk_capture_result", resultMsg.Type)
		}

		var result DiskCaptureResultPayload
		if err := json.Unmarshal(resultMsg.Payload, &result); err != nil {
			t.Fatalf("unmarshal DiskCaptureResultPayload: %v", err)
		}
		if result.RefMsgID != testRefMsgID {
			t.Errorf("RefMsgID = %q, want %q", result.RefMsgID, testRefMsgID)
		}

		// At least one of LayoutJSON or Error must be non-empty.
		if result.LayoutJSON == "" && result.Error == "" {
			t.Error("expected either LayoutJSON or Error to be non-empty in result")
		}

		t.Logf("disk_capture_result received: error=%q layout_json_len=%d",
			result.Error, len(result.LayoutJSON))
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func findPartitionByMount(parts []api.PartitionSpec, mountpoint string) *api.PartitionSpec {
	for i := range parts {
		if parts[i].MountPoint == mountpoint {
			return &parts[i]
		}
	}
	return nil
}

func containsFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}
