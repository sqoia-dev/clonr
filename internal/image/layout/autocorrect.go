package layout

import (
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	// biosbootSizeBytes is the 1 MiB BIOS boot (GPT BIOS grub) partition size.
	biosbootSizeBytes = 1 * 1024 * 1024
	// espSizeBytes is the 512 MiB EFI System Partition size.
	espSizeBytes = 512 * 1024 * 1024
)

// AutoCorrectForFirmware adjusts layout to match the node's detected firmware when the
// image's firmware type and the node's actual firmware type disagree. This prevents
// BIOS nodes from receiving UEFI layouts (and vice versa) when no explicit override
// is set.
//
// Priority:
//  1. If nodeDetectedFirmware is empty (unknown) — no correction, return layout as-is.
//  2. If imageFirmware matches nodeDetectedFirmware — no correction needed.
//  3. If mismatch detected — convert layout partitions and bootloader target.
//
// The correction is logged at Info level so operators can see it in the server log.
// nodeID and hostname are for log context only.
func AutoCorrectForFirmware(layout api.DiskLayout, imageFirmware, nodeDetectedFirmware, nodeID, hostname string) api.DiskLayout {
	if nodeDetectedFirmware == "" {
		// Unknown — can't auto-correct safely.
		return layout
	}

	imgFW := strings.ToLower(imageFirmware)
	nodeFW := strings.ToLower(nodeDetectedFirmware)

	if imgFW == nodeFW {
		// No mismatch — return as-is.
		return layout
	}

	switch {
	case nodeFW == "bios" && (imgFW == "uefi" || imgFW == ""):
		// Image is UEFI, node is BIOS. Convert to BIOS layout.
		log.Info().
			Str("node_id", nodeID).
			Str("hostname", hostname).
			Str("image_firmware", imgFW).
			Str("node_firmware", nodeFW).
			Msg("layout: auto-correcting UEFI image layout → BIOS (node reported legacy BIOS firmware)")
		return convertUEFIToBIOS(layout)

	case nodeFW == "uefi" && imgFW == "bios":
		// Image is BIOS, node is UEFI. Convert to UEFI layout.
		log.Info().
			Str("node_id", nodeID).
			Str("hostname", hostname).
			Str("image_firmware", imgFW).
			Str("node_firmware", nodeFW).
			Msg("layout: auto-correcting BIOS image layout → UEFI (node reported UEFI firmware)")
		return convertBIOSToUEFI(layout)
	}

	return layout
}

// convertUEFIToBIOS converts a UEFI disk layout to a BIOS-compatible one:
//   - Removes the ESP partition (/boot/efi, vfat, esp+boot flags).
//   - Prepends a biosboot partition (1 MiB, bios_grub flag) in its place.
//   - Changes the bootloader target from "x86_64-efi" to "i386-pc".
func convertUEFIToBIOS(in api.DiskLayout) api.DiskLayout {
	out := in
	out.Bootloader = api.Bootloader{Type: "grub2", Target: "i386-pc"}

	var newParts []api.PartitionSpec
	espReplaced := false
	for _, p := range in.Partitions {
		if !espReplaced && isESP(p) {
			// Replace ESP with biosboot.
			newParts = append(newParts, api.PartitionSpec{
				Device:     p.Device, // preserve device assignment (e.g. md0)
				Label:      "biosboot",
				SizeBytes:  biosbootSizeBytes,
				Filesystem: "biosboot",
				MountPoint: "",
				Flags:      []string{"bios_grub"},
			})
			espReplaced = true
			continue
		}
		newParts = append(newParts, p)
	}

	// If no ESP was found (unusual but defensive), prepend a biosboot.
	if !espReplaced {
		newParts = append([]api.PartitionSpec{{
			Label:      "biosboot",
			SizeBytes:  biosbootSizeBytes,
			Filesystem: "biosboot",
			MountPoint: "",
			Flags:      []string{"bios_grub"},
		}}, newParts...)
	}

	out.Partitions = newParts
	return out
}

// convertBIOSToUEFI converts a BIOS disk layout to a UEFI-compatible one:
//   - Removes the biosboot partition (bios_grub flag).
//   - Prepends an ESP (512 MiB, vfat, /boot/efi, esp+boot flags) in its place.
//   - Changes the bootloader target from "i386-pc" to "x86_64-efi".
func convertBIOSToUEFI(in api.DiskLayout) api.DiskLayout {
	out := in
	out.Bootloader = api.Bootloader{Type: "grub2", Target: "x86_64-efi"}

	var newParts []api.PartitionSpec
	biosReplaced := false
	for _, p := range in.Partitions {
		if !biosReplaced && isBIOSBoot(p) {
			// Replace biosboot with ESP.
			newParts = append(newParts, api.PartitionSpec{
				Device:     p.Device,
				Label:      "esp",
				SizeBytes:  espSizeBytes,
				Filesystem: "vfat",
				MountPoint: "/boot/efi",
				Flags:      []string{"esp", "boot"},
			})
			biosReplaced = true
			continue
		}
		newParts = append(newParts, p)
	}

	// If no biosboot was found, prepend an ESP.
	if !biosReplaced {
		newParts = append([]api.PartitionSpec{{
			Label:      "esp",
			SizeBytes:  espSizeBytes,
			Filesystem: "vfat",
			MountPoint: "/boot/efi",
			Flags:      []string{"esp", "boot"},
		}}, newParts...)
	}

	out.Partitions = newParts
	return out
}

// isESP returns true if the partition looks like an EFI System Partition:
// vfat filesystem or has "esp" in its flags.
func isESP(p api.PartitionSpec) bool {
	if strings.EqualFold(p.Filesystem, "vfat") {
		return true
	}
	for _, f := range p.Flags {
		if strings.EqualFold(f, "esp") {
			return true
		}
	}
	return false
}

// isBIOSBoot returns true if the partition is a BIOS boot (GPT BIOS grub) partition:
// biosboot filesystem or has "bios_grub" in its flags.
func isBIOSBoot(p api.PartitionSpec) bool {
	if strings.EqualFold(p.Filesystem, "biosboot") {
		return true
	}
	for _, f := range p.Flags {
		if strings.EqualFold(f, "bios_grub") {
			return true
		}
	}
	return false
}
