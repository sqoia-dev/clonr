package layout

import (
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

func uefiLayout() api.DiskLayout {
	return api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{Label: "esp", SizeBytes: 512 * 1024 * 1024, Filesystem: "vfat", MountPoint: "/boot/efi", Flags: []string{"esp", "boot"}},
			{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
		},
		Bootloader: api.Bootloader{Type: "grub2", Target: "x86_64-efi"},
	}
}

func biosLayout() api.DiskLayout {
	return api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{Label: "biosboot", SizeBytes: 1 * 1024 * 1024, Filesystem: "biosboot", Flags: []string{"bios_grub"}},
			{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
		},
		Bootloader: api.Bootloader{Type: "grub2", Target: "i386-pc"},
	}
}

func ambiguousLayout() api.DiskLayout {
	return api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{Label: "root", SizeBytes: 0, Filesystem: "xfs", MountPoint: "/"},
		},
		Bootloader: api.Bootloader{Type: "grub2", Target: "x86_64-efi"},
	}
}

func stored(id, name, kind string, layout api.DiskLayout) api.StoredDiskLayout {
	now := time.Now().UTC()
	return api.StoredDiskLayout{
		ID:           id,
		Name:         name,
		FirmwareKind: kind,
		Layout:       layout,
		CreatedAt:    now,
		UpdatedAt:    now,
		CapturedAt:   now,
	}
}

// TestPickLayoutForFirmware_UEFINodePicksUEFILayout is the headline test from
// the Sprint 35 plan — pass (UEFI node, [BIOS-only, UEFI-compatible]),
// expect the UEFI layout to win.
func TestPickLayoutForFirmware_UEFINodePicksUEFILayout(t *testing.T) {
	bios := stored("bios-1", "BIOS-only", api.FirmwareKindBIOS, biosLayout())
	uefi := stored("uefi-1", "UEFI-compatible", api.FirmwareKindUEFI, uefiLayout())

	res := PickLayoutForFirmware([]api.StoredDiskLayout{bios, uefi}, api.FirmwareKindUEFI)

	if !res.Picked {
		t.Fatalf("want Picked=true; got false")
	}
	if res.Layout.ID != "uefi-1" {
		t.Errorf("want UEFI layout; got %q (%s)", res.Layout.ID, res.Layout.Name)
	}
	if res.Source != "layout_catalog:firmware_match" {
		t.Errorf("want source=firmware_match; got %q", res.Source)
	}
}

// TestPickLayoutForFirmware_BIOSNodePicksBIOSLayout — symmetric case.
func TestPickLayoutForFirmware_BIOSNodePicksBIOSLayout(t *testing.T) {
	bios := stored("bios-1", "BIOS", api.FirmwareKindBIOS, biosLayout())
	uefi := stored("uefi-1", "UEFI", api.FirmwareKindUEFI, uefiLayout())

	res := PickLayoutForFirmware([]api.StoredDiskLayout{uefi, bios}, api.FirmwareKindBIOS)

	if !res.Picked {
		t.Fatalf("want Picked=true; got false")
	}
	if res.Layout.ID != "bios-1" {
		t.Errorf("want BIOS layout; got %q", res.Layout.ID)
	}
}

// TestPickLayoutForFirmware_AnyKindPredicatePath verifies that an
// untagged ("any") layout that structurally has an ESP wins over a
// BIOS-tagged layout for a UEFI node.
func TestPickLayoutForFirmware_AnyKindPredicatePath(t *testing.T) {
	bios := stored("bios-1", "BIOS", api.FirmwareKindBIOS, biosLayout())
	anyUEFI := stored("any-1", "Untagged but has ESP", api.FirmwareKindAny, uefiLayout())

	res := PickLayoutForFirmware([]api.StoredDiskLayout{bios, anyUEFI}, api.FirmwareKindUEFI)

	if !res.Picked {
		t.Fatalf("want Picked=true")
	}
	if res.Layout.ID != "any-1" {
		t.Errorf("want any-tagged UEFI-compat layout; got %q", res.Layout.ID)
	}
	if res.Source != "layout_catalog:firmware_predicate" {
		t.Errorf("want source=firmware_predicate; got %q", res.Source)
	}
}

// TestPickLayoutForFirmware_NoMatchFallsBackToOpposite ensures an operator
// who only has a BIOS layout pinned to a UEFI node still gets a deploy
// (autocorrect path can salvage it) rather than a "no layout" 500.
func TestPickLayoutForFirmware_NoMatchFallsBackToOpposite(t *testing.T) {
	bios := stored("bios-only", "BIOS-only", api.FirmwareKindBIOS, biosLayout())
	res := PickLayoutForFirmware([]api.StoredDiskLayout{bios}, api.FirmwareKindUEFI)
	if !res.Picked {
		t.Fatalf("want Picked=true; got false")
	}
	if res.Layout.ID != "bios-only" {
		t.Errorf("expected BIOS fallback; got %q", res.Layout.ID)
	}
	if res.Source != "layout_catalog:firmware_mismatch" {
		t.Errorf("want source=firmware_mismatch; got %q", res.Source)
	}
}

// TestPickLayoutForFirmware_UnknownFirmwareReturnsFirst preserves legacy
// behaviour for nodes that have not yet reported their firmware.
func TestPickLayoutForFirmware_UnknownFirmwareReturnsFirst(t *testing.T) {
	bios := stored("bios-1", "BIOS", api.FirmwareKindBIOS, biosLayout())
	uefi := stored("uefi-1", "UEFI", api.FirmwareKindUEFI, uefiLayout())

	res := PickLayoutForFirmware([]api.StoredDiskLayout{bios, uefi}, "")
	if !res.Picked {
		t.Fatalf("want Picked=true")
	}
	if res.Layout.ID != "bios-1" {
		t.Errorf("first-fit path should return bios-1; got %q", res.Layout.ID)
	}
}

// TestPickLayoutForFirmware_EmptyCandidates confirms we don't lie about the
// outcome.
func TestPickLayoutForFirmware_EmptyCandidates(t *testing.T) {
	res := PickLayoutForFirmware(nil, api.FirmwareKindUEFI)
	if res.Picked {
		t.Errorf("empty input must not produce a Picked result")
	}
}

// TestPickLayoutForFirmware_AmbiguousAnyOverTagOnly — an "any"-tagged
// layout (catch-all) is preferred over a same-tag layout whose structure
// would force the autocorrect path.
func TestPickLayoutForFirmware_AmbiguousAnyOverTagOnly(t *testing.T) {
	// UEFI-tagged but structurally BIOS (mis-tagged catalog row).
	mistagged := stored("mistag", "UEFI but biosboot", api.FirmwareKindUEFI, biosLayout())
	// any-tagged AND structurally UEFI-OK.
	good := stored("good", "any and ESP", api.FirmwareKindAny, uefiLayout())

	res := PickLayoutForFirmware([]api.StoredDiskLayout{mistagged, good}, api.FirmwareKindUEFI)
	if !res.Picked {
		t.Fatalf("want Picked=true")
	}
	if res.Layout.ID != "good" {
		t.Errorf("predicate path should beat tag-only mistag; got %q", res.Layout.ID)
	}
}

// ─── IsLayoutCompatibleWithFirmware predicate tests ──────────────────────────

func TestIsLayoutCompatibleWithFirmware_UEFI(t *testing.T) {
	if !IsLayoutCompatibleWithFirmware(uefiLayout(), api.FirmwareKindUEFI) {
		t.Error("UEFI layout must be UEFI-compatible")
	}
	if IsLayoutCompatibleWithFirmware(biosLayout(), api.FirmwareKindUEFI) {
		t.Error("BIOS-only layout (biosboot, no ESP) must not be UEFI-compatible")
	}
	if !IsLayoutCompatibleWithFirmware(ambiguousLayout(), api.FirmwareKindUEFI) {
		t.Error("layout without biosboot can be reshaped to UEFI by autocorrect — must be compatible")
	}
}

func TestIsLayoutCompatibleWithFirmware_BIOS(t *testing.T) {
	if !IsLayoutCompatibleWithFirmware(biosLayout(), api.FirmwareKindBIOS) {
		t.Error("BIOS layout must be BIOS-compatible")
	}
	if IsLayoutCompatibleWithFirmware(uefiLayout(), api.FirmwareKindBIOS) {
		t.Error("UEFI-only layout (ESP, no biosboot) must not be BIOS-compatible")
	}
	if !IsLayoutCompatibleWithFirmware(ambiguousLayout(), api.FirmwareKindBIOS) {
		t.Error("layout without ESP can be reshaped to BIOS — must be compatible")
	}
}

func TestIsLayoutCompatibleWithFirmware_UnknownFirmwareIsAlwaysTrue(t *testing.T) {
	if !IsLayoutCompatibleWithFirmware(uefiLayout(), "freedos") {
		t.Error("unknown firmware should not block legacy paths")
	}
}
