package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// EFIBootEntry represents a parsed efibootmgr boot entry.
type EFIBootEntry struct {
	BootNum string // e.g. "0001"
	Label   string
	Active  bool
}

// FixEFIBoot creates or repairs EFI boot entries for a freshly deployed system.
// It creates a new boot entry pointing to the ESP partition and sets it as the
// first boot option.
//
// Parameters:
//   - disk: the full device path of the target disk, e.g. /dev/nvme0n1
//   - espPartNum: the partition number of the ESP (usually 1), 1-indexed
//   - label: the boot menu label, e.g. "Rocky Linux"
//   - loader: the EFI loader path relative to the ESP, e.g. "\EFI\rocky\grubx64.efi"
func FixEFIBoot(ctx context.Context, disk string, espPartNum int, label, loader string) error {
	if label == "" {
		label = "Linux"
	}
	if loader == "" {
		loader = `\EFI\rocky\grubx64.efi`
	}

	// Remove stale entries with the same label to avoid duplicates.
	if err := removeStaleEntries(ctx, label); err != nil {
		// Non-fatal — proceed even if cleanup fails.
		_ = err
	}

	// Create new boot entry.
	// efibootmgr --create --disk /dev/nvme0n1 --part 1 --label "Linux" --loader '\EFI\...'
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

	// Set boot order so the new entry is first.
	newNum, err := parseNewBootNum(string(out))
	if err != nil {
		// Cannot determine the new boot number — set order based on existing list.
		return setBootOrderFirst(ctx)
	}

	return setBootEntry(ctx, newNum)
}

// removeStaleEntries deletes existing efibootmgr entries matching label.
func removeStaleEntries(ctx context.Context, label string) error {
	entries, err := listBootEntries(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Label, label) {
			cmd := exec.CommandContext(ctx, "efibootmgr", "--delete-bootnum", "--bootnum", e.BootNum)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("efiboot: remove %s: %w\noutput: %s", e.BootNum, err, string(out))
			}
		}
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

// parseNewBootNum extracts the new boot entry number from efibootmgr --create output.
// Output typically contains a line like: "Boot0001* label"
func parseNewBootNum(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Boot") && strings.Contains(line, "*") {
			if len(line) >= 8 {
				return line[4:8], nil
			}
		}
	}
	return "", fmt.Errorf("efiboot: cannot parse new boot number from output")
}

// setBootEntry sets the specified boot entry as first in the boot order and activates it.
func setBootEntry(ctx context.Context, bootNum string) error {
	// Activate the entry.
	activateCmd := exec.CommandContext(ctx, "efibootmgr", "--bootnum", bootNum, "--active")
	if out, err := activateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("efiboot: activate %s: %w\noutput: %s", bootNum, err, string(out))
	}

	// Set it as first in the boot order.
	orderCmd := exec.CommandContext(ctx, "efibootmgr", "--bootnext", bootNum)
	if out, err := orderCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("efiboot: set boot order %s: %w\noutput: %s", bootNum, err, string(out))
	}
	return nil
}

// setBootOrderFirst reads the current boot order and makes the first active entry
// the next boot target. Used as fallback when new entry number is unknown.
func setBootOrderFirst(ctx context.Context) error {
	entries, err := listBootEntries(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Active {
			return setBootEntry(ctx, e.BootNum)
		}
	}
	return fmt.Errorf("efiboot: no active boot entries found")
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
// IPv6) precedes any OS boot entries. This is called after finalize to ensure
// that OVMF/EDK2 and physical UEFI firmware come back to clonr's PXE server on
// the next reboot, allowing the server to confirm state before routing the node
// to disk via iPXE exit.
//
// On BIOS systems (where efibootmgr is not available or EFI variables are
// inaccessible), this function logs a warning and returns nil — it is a no-op
// on non-EFI systems.
//
// Logic:
//  1. Read current BootOrder from efibootmgr.
//  2. Find the first PXE entry (label contains "PXE" or "IPv4" or "IPv6").
//  3. Move the PXE entry to position 0 in BootOrder (before the OS entry).
//  4. Write the new BootOrder via efibootmgr -o.
//
// Both PXE and OS entries are kept — only the order changes. The OS entry
// remains second so disk boot works after the server routes via iPXE exit.
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
		label := strings.ToUpper(labelByNum[strings.TrimSpace(num)])
		if strings.Contains(label, "PXE") || strings.Contains(label, "IPV4") || strings.Contains(label, "IPV6") {
			pxeIdx = i
			break
		}
	}

	if pxeIdx < 0 {
		return fmt.Errorf("efiboot: SetPXEBootFirst: no PXE entry found in BootOrder %v", currentOrder)
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
