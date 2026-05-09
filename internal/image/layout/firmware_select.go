package layout

import (
	"strings"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// PickResult captures the outcome of a firmware-aware layout selection.
//
// Layout is the chosen StoredDiskLayout (zero-value when Picked == false).
// Source describes how it was matched, suitable for /effective-layout
// responses ("layout_catalog:firmware_match", "layout_catalog:firmware_predicate",
// or "layout_catalog:firmware_mismatch").
//
// Picked is false only when no candidates are provided — callers fall back
// to the inline / image-default path in that case.
type PickResult struct {
	Layout api.StoredDiskLayout
	Source string
	Picked bool
}

// PickLayoutForFirmware chooses the best disk layout from a candidate set
// for a node with the given detected firmware.  Closes #255 server-side.
//
// nodeFirmware is normalised to lowercase; values other than "bios" / "uefi"
// disable filtering and the function returns the first candidate as-is
// (used for legacy nodes whose firmware was never reported).
//
// Selection priority (highest → lowest):
//
//  1. Exact tag match: candidates with FirmwareKind == nodeFirmware AND
//     whose layout structure also matches (has-ESP for UEFI, has-biosboot
//     for BIOS).  This guards against a mis-tagged layout silently winning.
//
//  2. firmware-agnostic ("any") candidates whose structure is compatible
//     with the target firmware (predicate path).
//
//  3. Tag-match-only (structure mismatches): autocorrect path can salvage.
//
//  4. "any" tag with mismatched structure (catch-all).
//
//  5. Explicit opposite firmware (e.g. BIOS layout for UEFI node) —
//     deprioritised but not excluded outright since autocorrect can salvage
//     it; an operator who pinned a single BIOS layout to a UEFI group still
//     gets a deploy.
func PickLayoutForFirmware(candidates []api.StoredDiskLayout, nodeFirmware string) PickResult {
	if len(candidates) == 0 {
		return PickResult{}
	}

	target := strings.ToLower(strings.TrimSpace(nodeFirmware))
	if target != api.FirmwareKindBIOS && target != api.FirmwareKindUEFI {
		// Unknown firmware — first-candidate fallback (legacy behaviour).
		return PickResult{
			Layout: candidates[0],
			Source: "layout_catalog:firmware_unknown",
			Picked: true,
		}
	}

	// Sort candidates into priority buckets.  We don't mutate input.
	var (
		exactMatch        []api.StoredDiskLayout // tag matches AND structure compatible
		exactMatchTagOnly []api.StoredDiskLayout // tag matches but structure mismatches
		anyCompatible     []api.StoredDiskLayout // tag=any AND structure compatible
		anyTagged         []api.StoredDiskLayout // tag=any (any structure)
		oppositeTag       []api.StoredDiskLayout // explicit opposite firmware tag
	)

	for _, c := range candidates {
		kind := normalizeKind(c.FirmwareKind)
		structOK := IsLayoutCompatibleWithFirmware(c.Layout, target)
		switch {
		case kind == target && structOK:
			exactMatch = append(exactMatch, c)
		case kind == target:
			exactMatchTagOnly = append(exactMatchTagOnly, c)
		case kind == api.FirmwareKindAny && structOK:
			anyCompatible = append(anyCompatible, c)
		case kind == api.FirmwareKindAny:
			anyTagged = append(anyTagged, c)
		default:
			oppositeTag = append(oppositeTag, c)
		}
	}

	switch {
	case len(exactMatch) > 0:
		return PickResult{Layout: exactMatch[0], Source: "layout_catalog:firmware_match", Picked: true}
	case len(anyCompatible) > 0:
		return PickResult{Layout: anyCompatible[0], Source: "layout_catalog:firmware_predicate", Picked: true}
	case len(exactMatchTagOnly) > 0:
		return PickResult{Layout: exactMatchTagOnly[0], Source: "layout_catalog:firmware_tag", Picked: true}
	case len(anyTagged) > 0:
		return PickResult{Layout: anyTagged[0], Source: "layout_catalog:firmware_agnostic", Picked: true}
	case len(oppositeTag) > 0:
		return PickResult{Layout: oppositeTag[0], Source: "layout_catalog:firmware_mismatch", Picked: true}
	}
	// Unreachable: candidates was non-empty.
	return PickResult{Layout: candidates[0], Source: "layout_catalog:fallback", Picked: true}
}

// IsLayoutCompatibleWithFirmware reports whether a DiskLayout's partition
// shape is structurally compatible with the given target firmware.
//
//   - "uefi"  → must have at least one ESP-flagged or vfat partition with
//     mountpoint /boot/efi.  A layout with only a biosboot is BIOS-only.
//   - "bios"  → must NOT require an ESP, OR must have a biosboot partition.
//     A layout that has only an ESP and no biosboot is treated as UEFI-only.
//
// Unknown firmware values return true (don't block legacy paths).
func IsLayoutCompatibleWithFirmware(layout api.DiskLayout, firmware string) bool {
	target := strings.ToLower(strings.TrimSpace(firmware))
	hasESP := layoutHasESP(layout)
	hasBIOSBoot := layoutHasBIOSBoot(layout)

	switch target {
	case api.FirmwareKindUEFI:
		// UEFI needs an ESP.  A layout with neither ESP nor biosboot can
		// still be made UEFI by autocorrect, so return true.  A layout
		// with ONLY a biosboot is BIOS-only.
		if hasBIOSBoot && !hasESP {
			return false
		}
		return true
	case api.FirmwareKindBIOS:
		// BIOS needs MBR or biosboot, definitely no ESP.  A layout with
		// ONLY an ESP and no biosboot is UEFI-only.
		if hasESP && !hasBIOSBoot {
			return false
		}
		return true
	default:
		return true
	}
}

func layoutHasESP(l api.DiskLayout) bool {
	for _, p := range l.Partitions {
		if isESP(p) {
			return true
		}
	}
	return false
}

func layoutHasBIOSBoot(l api.DiskLayout) bool {
	for _, p := range l.Partitions {
		if isBIOSBoot(p) {
			return true
		}
	}
	return false
}

func normalizeKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case api.FirmwareKindBIOS:
		return api.FirmwareKindBIOS
	case api.FirmwareKindUEFI:
		return api.FirmwareKindUEFI
	default:
		return api.FirmwareKindAny
	}
}
