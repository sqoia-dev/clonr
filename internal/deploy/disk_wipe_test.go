package deploy

import (
	"strings"
	"testing"
)

// TestDiskWipeSequenceOrder asserts the load-bearing wipe-order contract:
// dd → wipefs → sgdisk. dd MUST come first (Sprint 33 PRE-ZERO) so the first
// 10 MiB of the disk — boot-sector + GRUB stage 1 + MBR partition table — is
// zeroed before any signature scan sees it. wipefs MUST come before sgdisk so
// FS/RAID superblocks are cleared before the GPT/MBR header is dropped;
// otherwise grub-probe sees "multiple partition labels" on a redeploy and
// the bootloader phase fails (the v0.1.11 BIOS bootloader regression).
//
// Reordering this slice or removing any step re-introduces a known-broken
// state. Add new wipe steps; do not reorder.
func TestDiskWipeSequenceOrder(t *testing.T) {
	const target = "/dev/sda"

	got := diskWipeSequence(target)

	if len(got) != 3 {
		t.Fatalf("diskWipeSequence: expected 3 commands, got %d (%+v)", len(got), got)
	}

	// First command must be dd if=/dev/zero of=<target> bs=1M count=10.
	if got[0].Name != "dd" {
		t.Errorf("diskWipeSequence[0].Name = %q, want %q", got[0].Name, "dd")
	}
	wantDDArgs := []string{"if=/dev/zero", "of=" + target, "bs=1M", "count=10", "conv=fsync"}
	if !equalStrings(got[0].Args, wantDDArgs) {
		t.Errorf("diskWipeSequence[0].Args = %v, want %v", got[0].Args, wantDDArgs)
	}

	// Second command must be wipefs.
	if got[1].Name != "wipefs" {
		t.Errorf("diskWipeSequence[1].Name = %q, want %q", got[1].Name, "wipefs")
	}
	if len(got[1].Args) != 2 || got[1].Args[0] != "-a" || got[1].Args[1] != target {
		t.Errorf("diskWipeSequence[1].Args = %v, want [-a %s]", got[1].Args, target)
	}

	// Third command must be sgdisk --zap-all.
	if got[2].Name != "sgdisk" {
		t.Errorf("diskWipeSequence[2].Name = %q, want %q", got[2].Name, "sgdisk")
	}
	if len(got[2].Args) != 2 || got[2].Args[0] != "--zap-all" || got[2].Args[1] != target {
		t.Errorf("diskWipeSequence[2].Args = %v, want [--zap-all %s]", got[2].Args, target)
	}
}

// TestDiskWipeSequence_DDFirst is the focused Sprint 33 PRE-ZERO regression
// guard. It asserts that the very first command in the sequence is dd
// against /dev/zero with count=10 (10 MiB). If a future maintainer reorders
// the slice — say, to put dd after wipefs because "dd is slow on big disks"
// — they re-introduce the GRUB-stage-1-ghost class of failure: a previously
// imaged disk with stale stage 1 bytes in the first sector chains through
// the old bootloader before the freshly written one runs. Keep dd first.
func TestDiskWipeSequence_DDFirst(t *testing.T) {
	seq := diskWipeSequence("/dev/sda")
	if len(seq) < 3 {
		t.Fatalf("diskWipeSequence too short: %d commands", len(seq))
	}
	if seq[0].Name != "dd" {
		t.Fatalf("first command = %q; want dd", seq[0].Name)
	}
	if got := seq[0].Args[0]; got != "if=/dev/zero" {
		t.Errorf("dd[0].Args[0] = %q; want if=/dev/zero", got)
	}
	// Verify count=10 is present — defending against an accidental count=1
	// (single 1 MiB block) which would miss the upper sectors that hold
	// some legacy GRUB stage 1.5 / partition-table backups.
	var sawCount10 bool
	for _, a := range seq[0].Args {
		if a == "count=10" {
			sawCount10 = true
			break
		}
	}
	if !sawCount10 {
		t.Errorf("dd command missing count=10; got args %v", seq[0].Args)
	}
}

// TestDiskWipeSequenceTargetPropagation verifies that the target disk is
// passed through to all three commands. dd encodes it as of=<target>, the
// other two as the trailing positional arg. This catches a class of
// refactor regression where one of the commands silently runs on the wrong
// device (or no device).
func TestDiskWipeSequenceTargetPropagation(t *testing.T) {
	cases := []string{
		"/dev/sda",
		"/dev/sdb",
		"/dev/nvme0n1",
		"/dev/md0",
	}
	for _, target := range cases {
		seq := diskWipeSequence(target)
		for i, c := range seq {
			if len(c.Args) == 0 {
				t.Errorf("diskWipeSequence(%q)[%d]: empty args", target, i)
				continue
			}
			if c.Name == "dd" {
				// dd uses of=<target>; verify presence rather than
				// position because conv=fsync trails of=.
				wantOf := "of=" + target
				var saw bool
				for _, a := range c.Args {
					if a == wantOf {
						saw = true
						break
					}
				}
				if !saw {
					t.Errorf("diskWipeSequence(%q)[%d]: dd args missing %q (got %v)",
						target, i, wantOf, c.Args)
				}
				continue
			}
			last := c.Args[len(c.Args)-1]
			if last != target {
				t.Errorf("diskWipeSequence(%q)[%d].Args last element = %q, want %q",
					target, i, last, target)
			}
		}
	}
}

// TestDiskWipeSequenceContainsWipefsBeforeSgdisk is a defence-in-depth check
// against a future maintainer reordering the slice or splicing in extra
// commands. It builds a flat string view of the sequence and asserts the
// "wipefs" token appears strictly before the "sgdisk" token, and dd before
// both.
func TestDiskWipeSequenceContainsWipefsBeforeSgdisk(t *testing.T) {
	seq := diskWipeSequence("/dev/sda")
	var names []string
	for _, c := range seq {
		names = append(names, c.Name)
	}
	flat := strings.Join(names, " ")

	ddIdx := strings.Index(flat, "dd")
	wipefsIdx := strings.Index(flat, "wipefs")
	sgdiskIdx := strings.Index(flat, "sgdisk")

	if ddIdx < 0 {
		t.Fatalf("diskWipeSequence missing dd: %q", flat)
	}
	if wipefsIdx < 0 {
		t.Fatalf("diskWipeSequence missing wipefs: %q", flat)
	}
	if sgdiskIdx < 0 {
		t.Fatalf("diskWipeSequence missing sgdisk: %q", flat)
	}
	if ddIdx >= wipefsIdx {
		t.Fatalf("dd must precede wipefs in wipe sequence; got order: %q", flat)
	}
	if wipefsIdx >= sgdiskIdx {
		t.Fatalf("wipefs must precede sgdisk in wipe sequence; got order: %q", flat)
	}
}

// equalStrings is a tiny helper that avoids pulling in reflect.DeepEqual
// just for slice comparison in tests.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
