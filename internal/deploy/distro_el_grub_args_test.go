package deploy

import (
	"strings"
	"testing"
)

// hasArg returns true if argv contains the literal `flag` token.
func hasArg(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}

// argValue returns the value attached to a `--key=value` argv token, or "" if
// the key is not present in any --key=value form. Whole-token --key matches
// (no `=`) return "" and require the caller to use hasArg instead.
func argValue(argv []string, key string) string {
	prefix := key + "="
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

// TestELGRUBBIOSArgs_NonRAID asserts the non-RAID BIOS/GPT path now passes
// `--skip-fs-probe` and an explicit `--modules=part_gpt biosdisk` list to
// grub2-install. Background: without --skip-fs-probe, grub-probe on a freshly
// partitioned disk can still see stale FS signatures from a previous deploy
// and fail with "multiple partition labels". The RAID-on-whole-disk branch
// already used both flags; the non-RAID path was missing them, which is
// what caused the v0.1.11 BIOS regression on vm201.
func TestELGRUBBIOSArgs_NonRAID(t *testing.T) {
	const (
		bootDir = "/mnt/target/boot"
		disk    = "/dev/sda"
	)
	argv := elGRUBBIOSArgs(bootDir, disk, false, false)

	// Required core flags.
	if !hasArg(argv, "--target=i386-pc") {
		t.Errorf("missing --target=i386-pc; argv=%v", argv)
	}
	if argValue(argv, "--boot-directory") != bootDir {
		t.Errorf("--boot-directory = %q, want %q; argv=%v",
			argValue(argv, "--boot-directory"), bootDir, argv)
	}
	if !hasArg(argv, "--recheck") {
		t.Errorf("missing --recheck; argv=%v", argv)
	}

	// The fix under test: non-RAID path must include --skip-fs-probe.
	if !hasArg(argv, "--skip-fs-probe") {
		t.Errorf("non-RAID path must pass --skip-fs-probe to avoid grub-probe stale-signature failures; argv=%v", argv)
	}

	// And the explicit module list.
	mods := argValue(argv, "--modules")
	if mods == "" {
		t.Errorf("non-RAID path must pass --modules=...; argv=%v", argv)
	}
	for _, want := range []string{"part_gpt", "biosdisk"} {
		if !strings.Contains(mods, want) {
			t.Errorf("--modules=%q missing %q (required for BIOS/GPT boot); argv=%v", mods, want, argv)
		}
	}

	// --force is RAID-only; must NOT appear in the non-RAID path.
	if hasArg(argv, "--force") {
		t.Errorf("non-RAID path must not pass --force; argv=%v", argv)
	}

	// The disk MUST be the last positional argument.
	if argv[len(argv)-1] != disk {
		t.Errorf("disk %q must be the last argv element; argv=%v", disk, argv)
	}
}

// TestELGRUBBIOSArgs_RAIDOnWholeDisk asserts the existing RAID-on-whole-disk
// argv shape is preserved end-to-end (regression guard while we extracted
// the helper). The RAID module list must include mdraid1x + diskfilter.
func TestELGRUBBIOSArgs_RAIDOnWholeDisk(t *testing.T) {
	const (
		bootDir = "/mnt/target/boot"
		disk    = "/dev/sda"
	)
	argv := elGRUBBIOSArgs(bootDir, disk, true /*isRAID*/, true /*isRAIDOnWholeDisk*/)

	if !hasArg(argv, "--force") {
		t.Errorf("RAID path must pass --force; argv=%v", argv)
	}
	if !hasArg(argv, "--skip-fs-probe") {
		t.Errorf("RAID-on-whole-disk path must pass --skip-fs-probe; argv=%v", argv)
	}

	mods := argValue(argv, "--modules")
	for _, want := range []string{"mdraid1x", "diskfilter", "part_gpt"} {
		if !strings.Contains(mods, want) {
			t.Errorf("RAID --modules=%q missing %q; argv=%v", mods, want, argv)
		}
	}

	// The non-RAID-only "biosdisk" module must NOT appear here — we want the
	// two paths to remain distinguishable in the argv contract so future
	// regressions can be attributed correctly.
	if strings.Contains(mods, "biosdisk") {
		t.Errorf("RAID-on-whole-disk path should not include biosdisk in --modules (RAID list owns its own boot drivers); argv=%v", argv)
	}

	if argv[len(argv)-1] != disk {
		t.Errorf("disk %q must be the last argv element; argv=%v", disk, argv)
	}
}

// TestELGRUBBIOSArgs_RAIDNonWholeDisk covers the md-on-partitions case:
// IsRAID=true but IsRAIDOnWholeDisk=false. Confirms that --force is set
// (any RAID layout) but the non-RAID module list is used (correct, because
// here the bootloader is written to raw member disks where biosdisk +
// part_gpt are the right modules and the array-level drivers come into
// play only after handoff to the kernel).
func TestELGRUBBIOSArgs_RAIDNonWholeDisk(t *testing.T) {
	const (
		bootDir = "/mnt/target/boot"
		disk    = "/dev/sda"
	)
	argv := elGRUBBIOSArgs(bootDir, disk, true /*isRAID*/, false /*isRAIDOnWholeDisk*/)

	if !hasArg(argv, "--force") {
		t.Errorf("RAID path must pass --force; argv=%v", argv)
	}
	if !hasArg(argv, "--skip-fs-probe") {
		t.Errorf("md-on-partitions BIOS path must pass --skip-fs-probe; argv=%v", argv)
	}
	mods := argValue(argv, "--modules")
	for _, want := range []string{"part_gpt", "biosdisk"} {
		if !strings.Contains(mods, want) {
			t.Errorf("md-on-partitions --modules=%q missing %q; argv=%v", mods, want, argv)
		}
	}
	if argv[len(argv)-1] != disk {
		t.Errorf("disk %q must be the last argv element; argv=%v", disk, argv)
	}
}
