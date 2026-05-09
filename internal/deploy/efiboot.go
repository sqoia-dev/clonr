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
// regression seen on bare-metal UEFI hosts (#225 FIX-EFI).
//
// DEPRECATED (Sprint 34 BOOT-POLICY): preserved as a thin shim over
// ApplyBootOrderPolicy("auto") so existing callers and tests continue to
// compile.  New call sites in finalize go through
// ApplyBootOrderPolicy(ctx, node.BootOrderPolicy) directly so the operator's
// per-node intent is respected.
func RepairBootOrderForReimage(ctx context.Context) error {
	return ApplyBootOrderPolicy(ctx, "auto")
}

// ApplyBootOrderPolicy is the Sprint 34 replacement for the reactive NVRAM
// repair.  It operates on the LIVE NVRAM of the deploy-target node and
// reorders BootOrder according to the operator's per-node policy:
//
//	"" / "auto" / "network"  — PXE / network entries lead BootOrder; OS
//	                           entries follow.  This is the v0.1.22 reactive
//	                           behaviour preserved verbatim — applied via
//	                           SetPXEBootFirst.
//
//	"os"                     — OS entries lead BootOrder; the first PXE entry
//	                           (if any) is moved to second position.  Used for
//	                           login / storage / service nodes that the
//	                           operator wants to cold-boot from disk by
//	                           default; PXE remains available as a fallback.
//
// The function is a TRUE no-op on:
//
//   - BIOS hosts (no /sys/firmware/efi).  efibootmgr is meaningless without
//     UEFI variables; we exit at the predicate.
//   - UEFI hosts where the requested policy is already satisfied.  We avoid an
//     unnecessary `efibootmgr -o` write that would dirty NVRAM on every deploy.
//
// Errors are wrapped+returned so the deploy caller can decide whether to fail
// the deploy or just log and continue.  finalize.go logs as a warning — the
// node will boot via removable-media auto-discovery regardless of NVRAM
// order, and an at-most-one extra power-cycle is the worst-case downside.
//
// Implementation note: the executable invoked is always `efibootmgr`; the
// argv we generate is exactly:
//
//	efibootmgr -o <BOOT0001>,<BOOT0002>,...
//
// Tests verify this argv directly via the bootOrderArgs helper so the
// contract is testable without a real UEFI host.
func ApplyBootOrderPolicy(ctx context.Context, policy string) error {
	if !isUEFISystem() {
		return nil
	}
	switch policy {
	case "", "auto", "network":
		// Existing behaviour — PXE first, OS second.  Already idempotent.
		if err := SetPXEBootFirst(ctx); err != nil {
			return fmt.Errorf("efiboot: ApplyBootOrderPolicy(network): %w", err)
		}
		return nil
	case "os":
		return setOSBootFirst(ctx)
	default:
		return fmt.Errorf("efiboot: unknown boot-order policy %q (want auto/network/os)", policy)
	}
}

// setOSBootFirst reorders NVRAM BootOrder so an OS entry leads, with the
// first PXE / network entry moved to second position.  Used by the "os"
// policy.  Behaviour mirrors SetPXEBootFirst except for the leadership rule.
//
// "OS entry" is defined as any entry that does NOT match the
// pxeEntryLabelMatch heuristic — symmetry with the network-first case keeps
// the two paths in sync as new firmware label conventions are added.
func setOSBootFirst(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "efibootmgr", "-v").Output()
	if err != nil {
		return fmt.Errorf("efiboot: setOSBootFirst: efibootmgr unavailable: %w", err)
	}
	currentOrder := parseBootOrder(string(out))
	if len(currentOrder) == 0 {
		return fmt.Errorf("efiboot: setOSBootFirst: no BootOrder found")
	}
	entries, err := listBootEntries(ctx)
	if err != nil {
		return fmt.Errorf("efiboot: setOSBootFirst: list entries: %w", err)
	}

	labelByNum := make(map[string]string, len(entries))
	for _, e := range entries {
		labelByNum[e.BootNum] = e.Label
	}

	osIdx := -1
	for i, num := range currentOrder {
		if !pxeEntryLabelMatch(labelByNum[strings.TrimSpace(num)]) {
			osIdx = i
			break
		}
	}
	if osIdx < 0 {
		// No OS entry in NVRAM — nothing to promote.  Common on a fresh OVMF
		// VM where only PXE entries exist; the policy is simply inapplicable
		// until an OS is installed.
		return nil
	}
	if osIdx == 0 {
		return nil
	}

	newOrder := make([]string, 0, len(currentOrder))
	newOrder = append(newOrder, strings.TrimSpace(currentOrder[osIdx]))
	for i, num := range currentOrder {
		if i != osIdx {
			newOrder = append(newOrder, strings.TrimSpace(num))
		}
	}

	args := bootOrderArgs(newOrder)
	cmd := exec.CommandContext(ctx, "efibootmgr", args...)
	if cmdOut, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("efiboot: setOSBootFirst: efibootmgr %v: %w\noutput: %s",
			args, err, string(cmdOut))
	}
	return nil
}

// bootOrderArgs returns the argv slice handed to efibootmgr to set BootOrder
// to the supplied sequence.  Centralised so the unit test for
// ApplyBootOrderPolicy can pin the contract without spawning a real
// efibootmgr process.
func bootOrderArgs(order []string) []string {
	return []string{"-o", strings.Join(order, ",")}
}
