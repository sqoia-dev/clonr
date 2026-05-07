package deploy

import (
	"strings"
	"testing"
)

// TestDiskWipeSequenceOrder asserts the load-bearing wipe-order contract:
// `wipefs -a <disk>` MUST run BEFORE `sgdisk --zap-all <disk>`.
//
// Background: sgdisk --zap-all only clears the GPT/MBR header. It leaves
// filesystem and RAID superblocks intact. On a redeploy of a previously
// imaged disk those residual signatures cause grub-probe (during
// grub2-install) to report "multiple partition labels" and fail the
// bootloader phase. The proactive wipefs step in front of sgdisk eliminates
// that class of failure. Reversing this order — or removing the wipefs step
// entirely — re-introduces the v0.1.11 BIOS bootloader regression on
// vm201/vm202.
func TestDiskWipeSequenceOrder(t *testing.T) {
	const target = "/dev/sda"

	got := diskWipeSequence(target)

	if len(got) != 2 {
		t.Fatalf("diskWipeSequence: expected 2 commands, got %d (%+v)", len(got), got)
	}

	// First command must be wipefs.
	if got[0].Name != "wipefs" {
		t.Errorf("diskWipeSequence[0].Name = %q, want %q", got[0].Name, "wipefs")
	}
	if len(got[0].Args) != 2 || got[0].Args[0] != "-a" || got[0].Args[1] != target {
		t.Errorf("diskWipeSequence[0].Args = %v, want [-a %s]", got[0].Args, target)
	}

	// Second command must be sgdisk --zap-all.
	if got[1].Name != "sgdisk" {
		t.Errorf("diskWipeSequence[1].Name = %q, want %q", got[1].Name, "sgdisk")
	}
	if len(got[1].Args) != 2 || got[1].Args[0] != "--zap-all" || got[1].Args[1] != target {
		t.Errorf("diskWipeSequence[1].Args = %v, want [--zap-all %s]", got[1].Args, target)
	}
}

// TestDiskWipeSequenceTargetPropagation verifies that the target disk is
// passed through to both commands as the final positional argument. This
// catches a class of refactor regression where one of the two commands
// silently runs on the wrong device (or no device).
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
// "wipefs" token appears strictly before the "sgdisk" token.
func TestDiskWipeSequenceContainsWipefsBeforeSgdisk(t *testing.T) {
	seq := diskWipeSequence("/dev/sda")
	var names []string
	for _, c := range seq {
		names = append(names, c.Name)
	}
	flat := strings.Join(names, " ")

	wipefsIdx := strings.Index(flat, "wipefs")
	sgdiskIdx := strings.Index(flat, "sgdisk")

	if wipefsIdx < 0 {
		t.Fatalf("diskWipeSequence missing wipefs: %q", flat)
	}
	if sgdiskIdx < 0 {
		t.Fatalf("diskWipeSequence missing sgdisk: %q", flat)
	}
	if wipefsIdx >= sgdiskIdx {
		t.Fatalf("wipefs must precede sgdisk in wipe sequence; got order: %q", flat)
	}
}
