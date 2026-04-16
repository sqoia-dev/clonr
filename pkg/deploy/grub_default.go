package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// grubEnvSize is the exact byte size of a GRUB environment block file.
// The format is: a fixed header line, one or more key=value lines, then '#'
// padding characters to fill the block to exactly 1024 bytes.
const grubEnvSize = 1024

// grubEnvHeader is the mandatory first line of every grubenv file.
const grubEnvHeader = "# GRUB Environment Block\n"

// pinGrubDefaultBLSEntry inspects the BLS entries under
// <mountRoot>/boot/loader/entries/, selects the best production kernel entry
// (highest version, non-rescue), and writes that entry's stem (filename without
// .conf) as saved_entry into <mountRoot>/boot/grub2/grubenv.
//
// When grub.cfg contains `load_env` and `set default="${saved_entry}"`, GRUB
// will boot the pinned production kernel instead of whatever ends up at index 0
// after BLS entries are sorted (which is often the rescue entry on RAID1 nodes
// because dracut names it with a "0-rescue-" prefix that sorts lexicographically
// before production kernel entries).
//
// grub2-editenv is NOT used because it is not staged in the deploy initramfs;
// we write the grubenv block directly in the documented 1024-byte padded format.
//
// Selection policy when multiple non-rescue entries exist: the entry whose stem
// sorts highest under a numeric-aware kernel version comparison is chosen.
// In practice after a fresh deploy there is exactly one production kernel.
func pinGrubDefaultBLSEntry(mountRoot string) (string, error) {
	entriesDir := filepath.Join(mountRoot, "boot", "loader", "entries")
	matches, err := filepath.Glob(filepath.Join(entriesDir, "*.conf"))
	if err != nil {
		return "", fmt.Errorf("glob BLS entries: %w", err)
	}

	// Separate production entries from rescue entries.
	var prodEntries []string
	for _, path := range matches {
		base := filepath.Base(path)
		// Dracut names rescue entries with a "0-rescue-" token embedded in the
		// stem, e.g. "<machineID>-0-rescue-<token>.conf". Skip any entry whose
		// base filename contains "rescue".
		if strings.Contains(base, "rescue") {
			continue
		}
		stem := strings.TrimSuffix(base, ".conf")
		prodEntries = append(prodEntries, stem)
	}

	if len(prodEntries) == 0 {
		return "", fmt.Errorf("no non-rescue BLS entry found under %s", entriesDir)
	}

	// When multiple production entries exist, pick the one with the highest
	// kernel version. BLS filenames produced by clonr and dracut follow the
	// pattern "<machineID>-<kver>.conf" where kver is the full uname -r string
	// (e.g. "5.14.0-427.13.1.el9_4.x86_64"). We extract the kver suffix
	// (everything after the first '-') and compare numerically so that
	// "5.14.0-427" > "5.14.0-362" even though '4' < '3' lexicographically when
	// the strings are compared as a whole.
	sort.Slice(prodEntries, func(i, j int) bool {
		return kernelVersionGreater(kverFromStem(prodEntries[i]), kverFromStem(prodEntries[j]))
	})
	chosen := prodEntries[0]

	// Write grubenv.
	grubenvPath := filepath.Join(mountRoot, "boot", "grub2", "grubenv")
	if err := os.MkdirAll(filepath.Dir(grubenvPath), 0o755); err != nil {
		return "", fmt.Errorf("mkdir grub2 dir: %w", err)
	}
	if err := writeGrubenv(grubenvPath, chosen); err != nil {
		return "", fmt.Errorf("write grubenv: %w", err)
	}
	return chosen, nil
}

// writeGrubenv writes a GRUB environment block to path with saved_entry set to
// savedEntry. The file is exactly grubEnvSize (1024) bytes as required by GRUB.
func writeGrubenv(path, savedEntry string) error {
	content := grubEnvHeader + "saved_entry=" + savedEntry + "\n"
	if len(content) > grubEnvSize {
		return fmt.Errorf("grubenv content exceeds %d bytes (got %d)", grubEnvSize, len(content))
	}
	// Pad with '#' characters to reach exactly 1024 bytes.
	padding := strings.Repeat("#", grubEnvSize-len(content))
	block := content + padding
	return os.WriteFile(path, []byte(block), 0o644)
}

// kverFromStem extracts the kernel version component from a BLS entry stem.
// Stems look like "<machineID>-<kver>" where machineID is a 32-char hex string.
// We strip the first 33 characters (32 hex + 1 dash) to get the kver.
// If the stem is shorter than expected we return it as-is for comparison.
func kverFromStem(stem string) string {
	// machineID is always 32 hex chars; the separator is '-'.
	const machineIDLen = 32
	if len(stem) > machineIDLen+1 {
		return stem[machineIDLen+1:]
	}
	return stem
}

// kernelVersionGreater reports whether kver a is greater than kver b using a
// numeric-aware component comparison. Kernel versions look like:
//
//	5.14.0-427.13.1.el9_4.x86_64
//
// We split on '.' and '-' and compare each numeric component. Non-numeric
// components fall back to lexicographic comparison. If all shared components
// are equal, the longer version string is considered greater.
func kernelVersionGreater(a, b string) bool {
	partsA := splitKernelVersion(a)
	partsB := splitKernelVersion(b)

	minLen := len(partsA)
	if len(partsB) < minLen {
		minLen = len(partsB)
	}

	for i := 0; i < minLen; i++ {
		na, aIsNum := parseIntPart(partsA[i])
		nb, bIsNum := parseIntPart(partsB[i])
		if aIsNum && bIsNum {
			if na != nb {
				return na > nb
			}
		} else {
			if partsA[i] != partsB[i] {
				return partsA[i] > partsB[i]
			}
		}
	}
	return len(partsA) > len(partsB)
}

// splitKernelVersion splits a kernel version string on '.' and '-' delimiters.
func splitKernelVersion(kver string) []string {
	// Replace '-' with '.' first so we can split on a single delimiter.
	normalized := strings.ReplaceAll(kver, "-", ".")
	return strings.Split(normalized, ".")
}

// parseIntPart attempts to parse s as a non-negative integer.
func parseIntPart(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
