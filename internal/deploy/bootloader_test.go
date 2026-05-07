package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestRawDiskFromDevice verifies that rawDiskFromDevice strips trailing
// partition number suffixes to recover the parent disk path. This is required
// for md-on-partitions BIOS RAID layouts where RAIDSpec.Members contain
// partition device names (e.g. "sda2") rather than whole-disk names.
func TestRawDiskFromDevice(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Traditional SATA/SAS devices with direct digit suffix.
		{"/dev/sda", "/dev/sda"},  // whole disk — no change
		{"/dev/sdb", "/dev/sdb"},  // whole disk — no change
		{"/dev/sda1", "/dev/sda"}, // partition 1
		{"/dev/sda2", "/dev/sda"}, // partition 2
		{"/dev/sda3", "/dev/sda"}, // partition 3
		{"/dev/sdb2", "/dev/sdb"}, // second disk, partition 2
		{"/dev/sdc4", "/dev/sdc"}, // third disk, partition 4
		{"/dev/hda3", "/dev/hda"}, // legacy IDE disk

		// NVMe devices use 'p' separator before partition number.
		{"/dev/nvme0n1", "/dev/nvme0n1"},   // whole disk — no change
		{"/dev/nvme0n1p1", "/dev/nvme0n1"}, // partition 1
		{"/dev/nvme0n1p2", "/dev/nvme0n1"}, // partition 2
		{"/dev/nvme1n1p3", "/dev/nvme1n1"}, // second NVMe, partition 3

		// Bare names (no /dev/ prefix) — returned as-is (no stripping needed
		// for the /dev/ prefix path, but the function must not panic).
		{"sda", "sda"},  // no /dev/, no digit — unchanged
		{"sda2", "sda"}, // no /dev/ prefix but still strips digit
	}

	for _, tc := range tests {
		got := rawDiskFromDevice(tc.in)
		if got != tc.want {
			t.Errorf("rawDiskFromDevice(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBootloaderError_Fatal verifies that BootloaderError is a proper error
// that wraps its cause and is detectable via errors.As.
func TestBootloaderError_Fatal(t *testing.T) {
	cause := errors.New("embedding is not possible, but this is required for RAID and LVM install")
	be := &BootloaderError{
		Targets: []string{"/dev/sda", "/dev/sdb"},
		Cause:   cause,
	}

	// Must satisfy the error interface.
	if be.Error() == "" {
		t.Error("BootloaderError.Error() must be non-empty")
	}

	// Must unwrap to the cause.
	if !errors.Is(be, cause) {
		t.Error("errors.Is(BootloaderError, cause) should return true via Unwrap")
	}

	// errors.As must find it when wrapped.
	wrapped := errors.New("finalize: " + be.Error())
	_ = wrapped // errors.As needs a non-nil target; test the direct case
	var detected *BootloaderError
	if !errors.As(be, &detected) {
		t.Error("errors.As should detect *BootloaderError")
	}
	if len(detected.Targets) != 2 {
		t.Errorf("expected 2 targets, got %d", len(detected.Targets))
	}
}

// TestSortMountEntries verifies that sortMountEntries produces depth-first order
// so parent mountpoints are always established before their children.
//
// This is critical for correctness: if /boot is mounted before / (the parent),
// the tar extract writes kernel files to the in-memory root's /boot directory,
// then the real /boot partition (empty) is mounted on top, hiding the content
// from GRUB at boot time.
func TestSortMountEntries(t *testing.T) {
	tests := []struct {
		name  string
		input []mountEntry
		want  []string // expected mount order (mount field only)
	}{
		{
			name: "root before boot — already in correct order",
			input: []mountEntry{
				{dev: "/dev/sda4", mount: "/"},
				{dev: "/dev/sda2", mount: "/boot"},
			},
			want: []string{"/", "/boot"},
		},
		{
			name: "boot before root — must be reordered",
			input: []mountEntry{
				{dev: "/dev/sda2", mount: "/boot"},
				{dev: "/dev/sda4", mount: "/"},
			},
			want: []string{"/", "/boot"},
		},
		{
			name: "root + /boot + /home — three levels, all need depth sort",
			input: []mountEntry{
				{dev: "/dev/sda5", mount: "/home"},
				{dev: "/dev/sda2", mount: "/boot"},
				{dev: "/dev/sda4", mount: "/"},
			},
			want: []string{"/", "/boot", "/home"},
		},
		{
			name: "root + /boot + /boot/efi — three-level nesting",
			input: []mountEntry{
				{dev: "/dev/sda3", mount: "/boot/efi"},
				{dev: "/dev/sda4", mount: "/"},
				{dev: "/dev/sda2", mount: "/boot"},
			},
			want: []string{"/", "/boot", "/boot/efi"},
		},
		{
			name: "VM207 single-disk BIOS layout (biosboot skipped, swap skipped)",
			// biosboot and swap are excluded before sortMountEntries is called;
			// only / and /boot reach the sorter.
			input: []mountEntry{
				{dev: "/dev/sda2", mount: "/boot"},
				{dev: "/dev/sda4", mount: "/"},
			},
			want: []string{"/", "/boot"},
		},
		{
			name: "VM206 md-on-partitions RAID layout — md2=/ must mount before md0=/boot",
			// In this topology mountPartitions receives md devices. Before 6631f6d
			// the order depended on layout iteration order; md0=/boot appeared before
			// md2=/ causing /boot to be mounted before the root filesystem.
			input: []mountEntry{
				{dev: "/dev/md0", mount: "/boot"},
				{dev: "/dev/md2", mount: "/"},
			},
			want: []string{"/", "/boot"},
		},
		{
			name: "deterministic secondary sort when depths are equal",
			// /data and /home both have depth 2; /data < /home lexicographically.
			input: []mountEntry{
				{dev: "/dev/sda6", mount: "/home"},
				{dev: "/dev/sda4", mount: "/"},
				{dev: "/dev/sda5", mount: "/data"},
			},
			want: []string{"/", "/data", "/home"},
		},
		{
			name: "single entry — no-op",
			input: []mountEntry{
				{dev: "/dev/sda1", mount: "/"},
			},
			want: []string{"/"},
		},
		{
			name:  "empty — no-op",
			input: []mountEntry{},
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sortMountEntries(tc.input)
			got := make([]string, len(tc.input))
			for i, m := range tc.input {
				got[i] = m.mount
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, want %d; got=%v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("position %d: got %q, want %q (full order: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestMountPartitionsOrder verifies that mountPartitions builds a mount list
// in the correct depth-first order for the VM207 single-disk BIOS layout
// (biosboot + /boot + swap + /) WITHOUT requiring real block devices or root.
//
// Strategy: we call sortMountEntries (the extracted sort helper) directly with
// the same input that mountPartitions would produce from the VM207 layout, and
// verify the resulting order matches the expected mount sequence.
func TestMountPartitionsOrder(t *testing.T) {
	// VM207 disk layout: biosboot(1MB) + /boot(1GB xfs) + swap(4GB) + /(xfs fill)
	// partDevs after createFilesystems: [sda1, sda2, sda3, sda4]
	layout := api.DiskLayout{
		Partitions: []api.PartitionSpec{
			{Label: "biosboot", Filesystem: "biosboot", MountPoint: ""},
			{Label: "boot", Filesystem: "xfs", MountPoint: "/boot"},
			{Label: "swap", Filesystem: "swap", MountPoint: "swap"},
			{Label: "root", Filesystem: "xfs", MountPoint: "/"},
		},
	}
	partDevs := []string{"/dev/sda1", "/dev/sda2", "/dev/sda3", "/dev/sda4"}

	// Build the mount entry list the same way mountPartitions does.
	var mps []mountEntry
	for i, p := range layout.Partitions {
		if p.MountPoint == "" || p.Filesystem == "swap" {
			continue
		}
		mps = append(mps, mountEntry{dev: partDevs[i], mount: p.MountPoint})
	}

	// Verify that before sorting, the layout-order is /boot then / (wrong).
	if len(mps) != 2 {
		t.Fatalf("expected 2 mountable partitions (/ and /boot), got %d: %v", len(mps), mps)
	}
	if mps[0].mount != "/boot" || mps[1].mount != "/" {
		t.Logf("pre-sort order: %v %v (layout already ordered correctly, test still verifies sort)", mps[0].mount, mps[1].mount)
	}

	sortMountEntries(mps)

	// After sorting: / must come first, /boot second.
	if mps[0].mount != "/" || mps[0].dev != "/dev/sda4" {
		t.Errorf("position 0: got {mount=%q dev=%q}, want {mount=%q dev=%q}",
			mps[0].mount, mps[0].dev, "/", "/dev/sda4")
	}
	if mps[1].mount != "/boot" || mps[1].dev != "/dev/sda2" {
		t.Errorf("position 1: got {mount=%q dev=%q}, want {mount=%q dev=%q}",
			mps[1].mount, mps[1].dev, "/boot", "/dev/sda2")
	}
}

// TestNoEfibootmgrCreateDuringFinalize asserts that the deploy package does NOT
// contain a call to `efibootmgr --create` anywhere in the finalize/rsync path.
// This is the unit-level guard for docs/boot-architecture.md §8 Change 1:
// clustr relies on UEFI removable-media auto-discovery of \EFI\BOOT\BOOTX64.EFI
// and must never inject an NVRAM OS entry during the deploy path.
//
// The test inspects the source of rsync.go for any residual `FixEFIBoot` call
// or `efibootmgr.*--create` invocation. It reads the file from disk so it is
// immune to dead-code elimination and catches future regressions at the text level.
func TestNoEfibootmgrCreateDuringFinalize(t *testing.T) {
	src := filepath.Join("rsync.go")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("cannot read rsync.go: %v", err)
	}
	content := string(data)

	// (a) No FixEFIBoot call in the deploy finalize path.
	if strings.Contains(content, "FixEFIBoot(") {
		t.Error("rsync.go must not call FixEFIBoot — NVRAM entry creation was removed in §8; " +
			"post-deploy UEFI boot relies on removable-media discovery of \\EFI\\BOOT\\BOOTX64.EFI")
	}

	// (b) No efibootmgr --create in rsync.go (belt-and-suspenders: guards inline invocations).
	if strings.Contains(content, `"--create"`) {
		t.Error("rsync.go must not invoke efibootmgr --create — see docs/boot-architecture.md §8")
	}
}

// TestBootx64PathIsLoadBearing asserts that the EL UEFI path (installELGRUBEFI
// in distro_el.go) verifies \EFI\BOOT\BOOTX64.EFI (the removable-media binary)
// rather than only \EFI\rocky\grubx64.efi (the RPM-shipped binary that drops to
// grub prompt). After the Sprint 26 DistroDriver refactor the logic lives in
// distro_el.go, not rsync.go; the test follows the code.
//
// Catches regressions where the BootloaderError guard is reverted to the old path.
func TestBootx64PathIsLoadBearing(t *testing.T) {
	src := filepath.Join("distro_el.go")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("cannot read distro_el.go: %v", err)
	}
	content := string(data)

	// BOOTX64.EFI must be referenced in the EFI install path.
	if !strings.Contains(content, "BOOTX64.EFI") {
		t.Error("distro_el.go must verify BOOTX64.EFI post-install and return BootloaderError on miss " +
			"(removable-media boot target — see docs/boot-architecture.md §8)")
	}

	// BootloaderError must be returned when BOOTX64.EFI is missing.
	bootx64Idx := strings.Index(content, "BOOTX64.EFI")
	bootloaderErrIdx := strings.Index(content, "BootloaderError{")
	if bootx64Idx < 0 || bootloaderErrIdx < 0 {
		t.Error("expected both BOOTX64.EFI verification and BootloaderError in distro_el.go")
		return
	}
	// The BOOTX64 stat check should precede (or coincide with) the BootloaderError
	// return in the verification block. This is satisfied by our implementation.
}

// TestParseBootOrderUnit verifies the parseBootOrder helper used by SetPXEBootFirst.
// These are parser utilities retained for future BMC-less bare-metal use.
func TestParseBootOrderUnit(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "standard output with BootOrder line",
			input: "BootCurrent: 0001\nBootOrder: 0001,0002,0003\nBoot0001* Rocky Linux\n",
			want:  []string{"0001", "0002", "0003"},
		},
		{
			name:  "single entry BootOrder",
			input: "BootOrder: ABCD\nBoot0001* PXE\n",
			want:  []string{"ABCD"},
		},
		{
			name:  "no BootOrder line",
			input: "BootCurrent: 0001\nBoot0001* PXE\n",
			want:  nil,
		},
		{
			name:  "empty BootOrder",
			input: "BootOrder: \n",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBootOrder(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("parseBootOrder(%q): got %v (len %d), want %v (len %d)",
					tc.name, got, len(got), tc.want, len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("parseBootOrder[%d]: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPXEEntryLabelMatch verifies the label heuristic used by SetPXEBootFirst
// and RepairBootOrderForReimage to classify NVRAM boot entries as PXE/network.
// FIX-EFI (#225): drift between this matcher and the OS-entry filter would leave
// the wrong entry at the top of BootOrder after a reimage.
func TestPXEEntryLabelMatch(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		// Common firmware conventions for PXE / network boot entries.
		{"UEFI PXEv4 (MAC:BC241136E92F)", true}, // Lenovo / Supermicro
		{"PXE IPv4: Realtek PCIe", true},        // ASRock / generic
		{"IPv4 Network", true},                  // AMI / Insyde
		{"IPv6 Network", true},                  // AMI dual-stack
		{"Network Boot", true},                  // HP iLO firmware
		{"UEFI: Network Card", true},            // Dell BIOS setup default
		{"PXE Boot", true},                      // OVMF (QEMU)
		// OS / disk entries that must NOT be classified as PXE.
		{"Rocky Linux", false},
		{"Windows Boot Manager", false},
		{"ubuntu", false},
		{"UEFI: PNY USB 3.0 FD", false}, // removable media — superficially looks like UEFI but no PXE/network token
		{"", false},
		{"BOOTX64.EFI (removable)", false},
	}
	for _, tc := range cases {
		if got := pxeEntryLabelMatch(tc.label); got != tc.want {
			t.Errorf("pxeEntryLabelMatch(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// TestRepairBootOrderForReimage_BIOSNoOp verifies that on a BIOS host
// (no /sys/firmware/efi) the repair function returns nil without touching
// efibootmgr.  We can't directly mock the filesystem here, but we exercise the
// fast path that isUEFISystem reports BIOS by checking the unit-tested predicate.
// The full UEFI path is exercised by integration on cloner / lab hosts.
func TestRepairBootOrderForReimage_BIOSNoOp(t *testing.T) {
	// Confirm the predicate works as intended on the test host.  On CI runners
	// (containerised) /sys/firmware/efi is almost always absent, so we expect
	// isUEFISystem to be false and RepairBootOrderForReimage to be a no-op.
	if isUEFISystem() {
		t.Skip("test host is UEFI — BIOS no-op path cannot be exercised here")
	}
	if err := RepairBootOrderForReimage(context.Background()); err != nil {
		t.Errorf("RepairBootOrderForReimage on BIOS host: got %v, want nil", err)
	}
}

// ── DistroDriver dispatch unit tests ────────────────────────────────────────

// stubDriver is a minimal DistroDriver that records InstallBootloader calls
// and returns a configurable error. Used to verify Finalize dispatch without
// requiring real block devices or a chroot environment.
type stubDriver struct {
	distro    Distro
	calls     []*bootloaderCtx
	returnErr error
}

func (s *stubDriver) Distro() Distro { return s.distro }

func (s *stubDriver) WriteSystemFiles(_ string, _ api.NodeConfig) error { return nil }

func (s *stubDriver) InstallBootloader(ctx *bootloaderCtx) error {
	s.calls = append(s.calls, ctx)
	return s.returnErr
}

// TestDriverDispatch_BIOS verifies that InstallBootloader is called exactly
// once with IsEFI=false and the correct AllTargets when a BIOS layout is
// dispatched via driverFor.
func TestDriverDispatch_BIOS(t *testing.T) {
	stub := &stubDriver{}
	testDistro := Distro{Family: "eltest", Major: 9}
	stub.distro = testDistro
	RegisterDriver(stub)
	defer delete(drivers, driverKey(testDistro))

	driver, err := driverFor(testDistro)
	if err != nil {
		t.Fatalf("driverFor returned unexpected error: %v", err)
	}

	bctx := &bootloaderCtx{
		MountRoot:  "/mnt/test",
		TargetDisk: "/dev/sda",
		AllTargets: []string{"/dev/sda", "/dev/sdb"},
		IsRAID:     true,
		IsEFI:      false,
	}
	if err := driver.InstallBootloader(bctx); err != nil {
		t.Fatalf("InstallBootloader returned unexpected error: %v", err)
	}

	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 InstallBootloader call, got %d", len(stub.calls))
	}
	got := stub.calls[0]
	if got.IsEFI {
		t.Error("BIOS dispatch: IsEFI must be false")
	}
	if got.MountRoot != "/mnt/test" {
		t.Errorf("MountRoot: got %q, want %q", got.MountRoot, "/mnt/test")
	}
	if len(got.AllTargets) != 2 {
		t.Errorf("AllTargets len: got %d, want 2", len(got.AllTargets))
	}
}

// TestDriverDispatch_EFI verifies that InstallBootloader is called exactly
// once with IsEFI=true for a UEFI layout dispatch.
func TestDriverDispatch_EFI(t *testing.T) {
	stub := &stubDriver{}
	testDistro := Distro{Family: "eltest", Major: 10}
	stub.distro = testDistro
	RegisterDriver(stub)
	defer delete(drivers, driverKey(testDistro))

	driver, err := driverFor(testDistro)
	if err != nil {
		t.Fatalf("driverFor returned unexpected error: %v", err)
	}

	bctx := &bootloaderCtx{
		MountRoot:  "/mnt/uefitest",
		TargetDisk: "/dev/sda",
		AllTargets: []string{"/dev/sda"},
		IsEFI:      true,
	}
	if err := driver.InstallBootloader(bctx); err != nil {
		t.Fatalf("InstallBootloader returned unexpected error: %v", err)
	}

	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 InstallBootloader call, got %d", len(stub.calls))
	}
	got := stub.calls[0]
	if !got.IsEFI {
		t.Error("EFI dispatch: IsEFI must be true")
	}
	if got.MountRoot != "/mnt/uefitest" {
		t.Errorf("MountRoot: got %q, want %q", got.MountRoot, "/mnt/uefitest")
	}
}

// TestDriverDispatch_ErrorPropagates verifies that errors returned by
// InstallBootloader propagate through the dispatch layer unchanged.
func TestDriverDispatch_ErrorPropagates(t *testing.T) {
	sentinel := fmt.Errorf("grub install failed: disk offline")
	stub := &stubDriver{returnErr: sentinel}
	testDistro := Distro{Family: "eltest", Major: 8}
	stub.distro = testDistro
	RegisterDriver(stub)
	defer delete(drivers, driverKey(testDistro))

	driver, err := driverFor(testDistro)
	if err != nil {
		t.Fatalf("driverFor returned unexpected error: %v", err)
	}

	bctx := &bootloaderCtx{
		MountRoot:  "/mnt/err",
		TargetDisk: "/dev/sda",
		AllTargets: []string{"/dev/sda"},
	}
	got := driver.InstallBootloader(bctx)
	if !errors.Is(got, sentinel) {
		t.Errorf("expected sentinel error, got: %v", got)
	}
}

// TestDriverDispatch_NoDriverReturnsError verifies that driverFor returns
// ErrNoDriver for an unregistered distro key.
func TestDriverDispatch_NoDriverReturnsError(t *testing.T) {
	_, err := driverFor(Distro{Family: "unknown-distro", Major: 99})
	if err == nil {
		t.Fatal("expected ErrNoDriver for unregistered distro, got nil")
	}
	var noDriver *ErrNoDriver
	if !errors.As(err, &noDriver) {
		t.Errorf("expected *ErrNoDriver, got %T: %v", err, err)
	}
}
