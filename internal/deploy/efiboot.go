package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EFIBootEntry represents a parsed efibootmgr boot entry.
type EFIBootEntry struct {
	BootNum string // e.g. "0001"
	Label   string
	Active  bool
}

// ManualCreateEFIEntry is a DIAGNOSTIC-ONLY helper that creates an NVRAM boot
// entry via efibootmgr. It is NOT called by the deploy or autodeploy paths —
// clustr relies on UEFI removable-media auto-discovery of \EFI\BOOT\BOOTX64.EFI
// for post-deploy boot (see docs/boot-architecture.md §8).
//
// WARNING: this function has a known stale-PARTUUID hazard. efibootmgr --create
// bakes the current ESP PARTUUID into the NVRAM device path. parted mklabel gpt
// regenerates PARTUUID on every reimage; the NVRAM entry becomes stale after any
// subsequent reimage (pflash survives disk wipe). Use only for manual diagnostics
// on a node you do not intend to reimage.
func ManualCreateEFIEntry(ctx context.Context, disk string, espPartNum int, label, loader string) error {
	if label == "" {
		label = "Linux"
	}
	if loader == "" {
		loader = `\EFI\BOOT\BOOTX64.EFI`
	}

	args := []string{
		"--create",
		"--disk", disk,
		"--part", fmt.Sprintf("%d", espPartNum),
		"--label", label,
		"--loader", loader,
	}

	cmd := exec.CommandContext(ctx, "efibootmgr", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("efiboot: create entry: %w\noutput: %s", err, string(out))
	}
	return nil
}

// listBootEntries parses efibootmgr output and returns all entries.
func listBootEntries(ctx context.Context) ([]EFIBootEntry, error) {
	out, err := exec.CommandContext(ctx, "efibootmgr").Output()
	if err != nil {
		return nil, fmt.Errorf("efiboot: list entries: %w", err)
	}

	var entries []EFIBootEntry
	for _, line := range strings.Split(string(out), "\n") {
		// Lines look like: "Boot0001* Rocky Linux" or "Boot0002  Windows"
		if !strings.HasPrefix(line, "Boot") || len(line) < 8 {
			continue
		}
		num := line[4:8]
		active := len(line) > 8 && line[8] == '*'
		label := ""
		if len(line) > 9 {
			label = strings.TrimSpace(line[9:])
		}
		entries = append(entries, EFIBootEntry{
			BootNum: num,
			Label:   label,
			Active:  active,
		})
	}
	return entries, nil
}

// parseBootOrder parses the "BootOrder: XXXX,YYYY,..." line from efibootmgr output.
// Returns a slice of boot numbers in current order, or nil if not found.
func parseBootOrder(output string) []string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "BootOrder:") {
			raw := strings.TrimPrefix(line, "BootOrder:")
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return nil
			}
			return strings.Split(raw, ",")
		}
	}
	return nil
}

// SetPXEBootFirst reorders the NVRAM BootOrder so the first PXE entry (IPv4 or
// IPv6) precedes any OS boot entries. This is a utility for bare-metal scenarios
// where the BMC-level boot order is not configurable from inside the OS but
// efivar-mediated BootOrder is. It is NOT called from the deploy path — Proxmox
// boot order (net0;scsi0) and BMC-level boot order handle "PXE first" on our
// supported platforms. Preserved for potential future use.
//
// On BIOS systems (where efibootmgr is not available or EFI variables are
// inaccessible), this function logs a warning and returns nil — it is a no-op
// on non-EFI systems.
func SetPXEBootFirst(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "efibootmgr", "-v").Output()
	if err != nil {
		// efibootmgr unavailable or EFI vars not accessible — BIOS system.
		return fmt.Errorf("efiboot: SetPXEBootFirst: efibootmgr unavailable (BIOS system?): %w", err)
	}

	currentOrder := parseBootOrder(string(out))
	if len(currentOrder) == 0 {
		return fmt.Errorf("efiboot: SetPXEBootFirst: no BootOrder found in efibootmgr output")
	}

	entries, err := listBootEntries(ctx)
	if err != nil {
		return fmt.Errorf("efiboot: SetPXEBootFirst: list entries: %w", err)
	}

	// Build a map of bootNum → label for quick lookup.
	labelByNum := make(map[string]string, len(entries))
	for _, e := range entries {
		labelByNum[e.BootNum] = e.Label
	}

	// Find the first PXE entry in the current boot order.
	pxeIdx := -1
	for i, num := range currentOrder {
		if pxeEntryLabelMatch(labelByNum[strings.TrimSpace(num)]) {
			pxeIdx = i
			break
		}
	}

	if pxeIdx < 0 {
		// No PXE entry in NVRAM — common on fresh OVMF VMs where PXE is dynamic
		// and not persisted. Removable-media discovery handles OS boot; no action needed.
		return nil
	}

	if pxeIdx == 0 {
		// PXE is already first — nothing to do.
		return nil
	}

	// Build new order: PXE entry first, then everything else in original order.
	newOrder := make([]string, 0, len(currentOrder))
	newOrder = append(newOrder, strings.TrimSpace(currentOrder[pxeIdx]))
	for i, num := range currentOrder {
		if i != pxeIdx {
			newOrder = append(newOrder, strings.TrimSpace(num))
		}
	}

	orderStr := strings.Join(newOrder, ",")
	cmd := exec.CommandContext(ctx, "efibootmgr", "-o", orderStr)
	if cmdOut, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("efiboot: SetPXEBootFirst: set BootOrder %s: %w\noutput: %s",
			orderStr, err, string(cmdOut))
	}

	return nil
}

// isUEFISystem reports whether the running system is booted via UEFI.  Detected
// by the presence of /sys/firmware/efi (the standard kernel marker — present on
// every UEFI Linux boot, absent on legacy BIOS).  Used by RepairBootOrderForReimage
// as a fast precheck so the function is a true no-op on BIOS deploys without
// invoking efibootmgr (which would fail and force the caller to parse error text).
func isUEFISystem() bool {
	_, err := os.Stat("/sys/firmware/efi")
	return err == nil
}

// pxeEntryLabelMatch reports whether label looks like a PXE / network boot entry.
// Matches OVMF/SeaBIOS/AMI/Insyde/HP/Dell/Supermicro/Lenovo conventions for IPv4
// + IPv6 PXE entries.  Centralised so SetPXEBootFirst and RepairBootOrderForReimage
// stay aligned; otherwise drift between the two parsers leaves the wrong entries
// at the top of BootOrder.
func pxeEntryLabelMatch(label string) bool {
	u := strings.ToUpper(label)
	return strings.Contains(u, "PXE") ||
		strings.Contains(u, "IPV4") ||
		strings.Contains(u, "IPV6") ||
		strings.Contains(u, "NETWORK")
}

// RepairBootOrderForReimage hardens NVRAM against the "OS-entry-ahead-of-PXE"
// regression seen on bare-metal UEFI hosts (#225 FIX-EFI):
//
//   - clustr's deploy path runs grub2-install with --no-nvram + --removable,
//     so we do NOT add an OS NVRAM entry ourselves and rely on UEFI removable-
//     media auto-discovery of \EFI\BOOT\BOOTX64.EFI for post-deploy boot
//     (docs/boot-architecture.md §8).
//   - HOWEVER: NVRAM (efivars / OVMF pflash) survives reimages.  Entries written
//     by a prior life of the disk — Anaconda kickstart, an older clustr version,
//     a manual rescue session — persist and continue to land ahead of PXE in
//     BootOrder, which makes future reimages chain to a stale OS bootloader (or
//     a now-blank EFI partition) instead of the PXE script.
//
// This function runs (best-effort) at the end of the EFI bootloader install step
// in finalize.  It operates on the LIVE NVRAM of the target node (the deploy
// initramfs is the running kernel, /sys/firmware/efi is the host firmware), NOT
// on any chroot view.  Behaviour:
//
//  1. If /sys/firmware/efi is absent (BIOS deploy), return nil — no-op.
//  2. List boot entries via efibootmgr; if listing fails, log+nil (we never
//     fail a successful deploy because of NVRAM cleanup).
//  3. Reorder BootOrder so a PXE entry leads (delegated to SetPXEBootFirst, which
//     already implements that with the pxeEntryLabelMatch heuristic).
//
// Errors from efibootmgr are wrapped+returned so the caller can decide; the
// production hook in finalize logs as warning rather than failing the deploy.
//
// Why we do NOT delete OS NVRAM entries here: a `efibootmgr -B -b XXXX` of an
// arbitrary entry is dangerous on shared-firmware hosts (e.g. a server that also
// dual-boots Windows, or a tester's laptop accidentally on the deploy network).
// Reordering is non-destructive and resolves the symptom (OS entry blocks PXE);
// destructive cleanup is reserved for an explicit operator action.
func RepairBootOrderForReimage(ctx context.Context) error {
	if !isUEFISystem() {
		return nil
	}
	if err := SetPXEBootFirst(ctx); err != nil {
		return fmt.Errorf("efiboot: RepairBootOrderForReimage: %w", err)
	}
	return nil
}
