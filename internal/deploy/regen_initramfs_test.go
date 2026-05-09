package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDracutCmdForKernel_PortabilityFlags is the load-bearing Sprint 33
// DRACUT-REGEN regression guard. It builds the dracut argv for a known
// kernel version and asserts every portability flag is present.
//
// If a future maintainer drops one of these flags, a captured-on-virtio
// image will silently fail to boot on a host with a different storage
// controller — and the failure mode (initramfs hangs at "Probing EDD" or
// "Cannot find /dev/mapper/...") is exactly the bug class this sprint
// closes. Keep the assertions strict.
func TestDracutCmdForKernel_PortabilityFlags(t *testing.T) {
	const (
		mountRoot = "/tmp/fake-rootfs-for-test"
		kver      = "5.14.0-503.40.1.el9_5.x86_64"
	)
	cmd := dracutCmdForKernel(context.Background(), mountRoot, kver)

	if cmd == nil {
		t.Fatal("dracutCmdForKernel returned nil")
	}
	if got, want := filepath.Base(cmd.Path), "chroot"; got != want {
		t.Errorf("cmd.Path basename = %q, want %q", got, want)
	}

	// cmd.Args[0] is the program name ("chroot"), [1] is mountRoot, [2] is
	// "dracut", and the rest is the dracut argv.
	if len(cmd.Args) < 4 {
		t.Fatalf("cmd.Args too short: %v", cmd.Args)
	}
	if cmd.Args[1] != mountRoot {
		t.Errorf("cmd.Args[1] = %q, want %q", cmd.Args[1], mountRoot)
	}
	if cmd.Args[2] != "dracut" {
		t.Errorf("cmd.Args[2] = %q, want %q", cmd.Args[2], "dracut")
	}

	// Flatten the dracut argv portion into a string set for membership tests.
	dracutArgs := cmd.Args[3:]
	flat := strings.Join(dracutArgs, " ")

	// Every portability flag must be present. The order between flags is
	// not load-bearing (dracut is order-insensitive for these), but every
	// flag must appear. Adjacency for "--force-add mdraid" / "--force-add
	// lvm" IS load-bearing — dracut's CLI requires the value to follow
	// the flag — so we assert the flag-value pair as a substring.
	required := []string{
		"-fv",
		"-N",
		"--lvmconf",
		"--force-add mdraid",
		"--force-add lvm",
	}
	for _, want := range required {
		if !strings.Contains(flat, want) {
			t.Errorf("dracut argv missing %q\n  got: %q", want, flat)
		}
	}

	// The output path and trailing kver must be present and in that
	// order: dracut's positional args are <output> <kver>. Without the
	// trailing kver, dracut introspects the running (deploy-host) kernel
	// instead of the target's.
	wantOutput := "/boot/initramfs-" + kver + ".img"
	wantTail := []string{wantOutput, kver}
	if len(dracutArgs) < 2 {
		t.Fatalf("dracut argv too short for positional args: %v", dracutArgs)
	}
	gotTail := dracutArgs[len(dracutArgs)-2:]
	for i, want := range wantTail {
		if gotTail[i] != want {
			t.Errorf("dracut argv positional[%d] = %q, want %q (full argv: %v)",
				i, gotTail[i], want, dracutArgs)
		}
	}
}

// TestListKernelVersionsForDracut_DiscoveryAndFiltering verifies that the
// kernel enumeration walks <mountRoot>/boot/, picks up every vmlinuz-*
// file, returns the version stripped of the prefix, and filters out
// rescue kernels. Rescue kernels are skipped because the BLS rescue entry
// is deleted later in applyBootConfig — building an initramfs for them
// is wasted time.
func TestListKernelVersionsForDracut_DiscoveryAndFiltering(t *testing.T) {
	root := t.TempDir()
	bootDir := filepath.Join(root, "boot")
	if err := os.MkdirAll(bootDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	plant := []string{
		"vmlinuz-5.14.0-503.40.1.el9_5.x86_64",
		"vmlinuz-5.14.0-427.13.1.el9_4.x86_64",
		"vmlinuz-0-rescue-deadbeef",                  // must be filtered
		"vmlinuz",                                    // bare prefix → skipped (kver empty)
		"initramfs-5.14.0-503.40.1.el9_5.x86_64.img", // wrong prefix → skipped
		"config-5.14.0-503.40.1.el9_5.x86_64",        // wrong prefix → skipped
	}
	for _, name := range plant {
		if err := os.WriteFile(filepath.Join(bootDir, name), nil, 0o644); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	got, err := listKernelVersionsForDracut(root)
	if err != nil {
		t.Fatalf("listKernelVersionsForDracut: %v", err)
	}

	want := []string{
		"5.14.0-427.13.1.el9_4.x86_64", // sorted before 503
		"5.14.0-503.40.1.el9_5.x86_64",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d kernels (%v); want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kernel[%d] = %q; want %q", i, got[i], want[i])
		}
	}
}

// TestListKernelVersionsForDracut_EmptyBoot verifies the function returns
// nil with no error when /boot has no vmlinuz files. runDracutInChroot
// treats this as a non-fatal warning and skips dracut regeneration; the
// test pins that contract.
func TestListKernelVersionsForDracut_EmptyBoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "boot"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := listKernelVersionsForDracut(root)
	if err != nil {
		t.Fatalf("listKernelVersionsForDracut: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d kernels (%v); want 0", len(got), got)
	}
}

// TestListKernelVersionsForDracut_MissingBoot verifies the function returns
// an error (not a panic) when /boot does not exist on the rootfs. This
// can happen if the image is corrupt or a partial extract; the caller
// logs the error and proceeds without dracut.
func TestListKernelVersionsForDracut_MissingBoot(t *testing.T) {
	root := t.TempDir() // no /boot subdir

	_, err := listKernelVersionsForDracut(root)
	if err == nil {
		t.Fatal("expected error for missing /boot, got nil")
	}
}

// TestDracutCmdForKernel_NoRegenerateAll defends against a regression
// where someone "helpfully" reintroduces --regenerate-all to the argv.
// regenerate-all defeats the per-kver progress reporting AND, because
// dracut then ignores positional args, would silently mask a
// dracutCmdForKernel call that passed the wrong kver. Keep them separate.
func TestDracutCmdForKernel_NoRegenerateAll(t *testing.T) {
	cmd := dracutCmdForKernel(context.Background(), "/x", "1.0")
	for _, a := range cmd.Args {
		if a == "--regenerate-all" {
			t.Fatalf("dracut argv contains --regenerate-all (Sprint 33 uses per-kver invocation): %v", cmd.Args)
		}
	}
}
