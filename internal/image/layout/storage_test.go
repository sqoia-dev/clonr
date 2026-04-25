package layout

import (
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/hardware"
)

// ─── Hardware fixtures ────────────────────────────────────────────────────────

// storageHW builds a hardware.SystemInfo with the requested number of HDDs,
// SSDs, and NVMe drives.  All drives are given distinct names and identical
// sizes within each tier.
func storageHW(nHDD, nSSD, nNVMe int, hddSize, ssdSize, nvmeSize uint64) hardware.SystemInfo {
	var disks []hardware.Disk
	for i := 0; i < nHDD; i++ {
		disks = append(disks, hardware.Disk{
			Name:       nameDisk("sd", i),
			Size:       hddSize,
			Transport:  "sata",
			Rotational: true,
			Model:      "TOSHIBA MG08ACA16TE",
		})
	}
	offset := nHDD
	for i := 0; i < nSSD; i++ {
		disks = append(disks, hardware.Disk{
			Name:       nameDisk("sd", offset+i),
			Size:       ssdSize,
			Transport:  "sata",
			Rotational: false,
			Model:      "Samsung 870 EVO",
		})
	}
	for i := 0; i < nNVMe; i++ {
		disks = append(disks, hardware.Disk{
			Name:      nameDisk("nvme", i) + "n1",
			Size:      nvmeSize,
			Transport: "nvme",
			Model:     "Samsung PM9A3",
		})
	}
	return hardware.SystemInfo{
		Disks:  disks,
		Memory: hardware.MemoryInfo{TotalKB: 128 * 1024 * 1024},
	}
}

// nameDisk returns a device name: "sda", "sdb", … for "sd" prefix;
// "nvme0", "nvme1", … for "nvme" prefix.
func nameDisk(prefix string, idx int) string {
	if prefix == "nvme" {
		return prefix + string(rune('0'+idx))
	}
	// a, b, c, … aa, ab for >26 drives.
	if idx < 26 {
		return prefix + string(rune('a'+idx))
	}
	return prefix + string(rune('a'+idx/26-1)) + string(rune('a'+idx%26))
}

// ─── Basic sanity tests ───────────────────────────────────────────────────────

// TestRecommendStorage_NoDisk verifies an error is returned with no disks.
func TestRecommendStorage_NoDisk(t *testing.T) {
	_, err := RecommendStorage(hardware.SystemInfo{}, "bios")
	if err == nil {
		t.Fatal("expected error with no disks, got nil")
	}
}

// TestRecommendStorage_TooFewDrives verifies an error is returned with only 1 disk.
func TestRecommendStorage_TooFewDrives(t *testing.T) {
	hw := hardware.SystemInfo{
		Disks: []hardware.Disk{
			{Name: "sda", Size: 16 * tb, Transport: "sata", Rotational: true},
		},
	}
	_, err := RecommendStorage(hw, "bios")
	if err == nil {
		t.Fatal("expected error with only 1 disk, got nil")
	}
}

// TestRecommendStorage_OSDriveCount confirms exactly 2 drives are allocated to the OS.
func TestRecommendStorage_OSDriveCount(t *testing.T) {
	// 6 HDDs + 2 NVMe
	hw := storageHW(6, 0, 2, 16*tb, 0, 4*tb)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Stats.DrivesForOS != 2 {
		t.Errorf("want DrivesForOS=2, got %d", rec.Stats.DrivesForOS)
	}
	// 6 HDDs remain for data pool.
	if rec.Stats.DrivesForData != 6 {
		t.Errorf("want DrivesForData=6, got %d", rec.Stats.DrivesForData)
	}
}

// TestRecommendStorage_SmallestTwoDrivesChosenForOS confirms the OS RAID1
// uses the 2 smallest drives even when they are different types.
func TestRecommendStorage_SmallestTwoDrivesChosenForOS(t *testing.T) {
	// 4 × 16 TB HDDs, 2 × 480 GB SSDs — OS should land on the 2 SSDs.
	hw := storageHW(4, 2, 0, 16*tb, 480*uint64(gb), 0)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reasoning should mention the SSD names (they are sde, sdf in our fixture).
	joined := strings.Join(rec.Reasoning, " ")
	if !strings.Contains(joined, "sde") && !strings.Contains(joined, "sdf") {
		t.Errorf("expected OS to be on the 2 SSDs (sde/sdf), but reasoning says:\n%s", joined)
	}
}

// ─── Vdev topology tests ──────────────────────────────────────────────────────

func TestChooseVdevTopology(t *testing.T) {
	cases := []struct {
		n             int
		wantType      string
		wantMinVdevs  int
		wantMaxVdevs  int
		wantMinWidth  int
		wantMaxWidth  int
	}{
		{1, "stripe", 1, 1, 1, 1},
		{2, "stripe", 1, 1, 2, 2},
		{3, "stripe", 1, 1, 3, 3},
		{4, "raidz1", 1, 1, 4, 4},
		{5, "raidz1", 1, 1, 5, 5},
		{6, "raidz2", 1, 1, 6, 6},
		{10, "raidz2", 1, 1, 10, 10},
		{11, "raidz2", 2, 2, 5, 7},
		{20, "raidz2", 2, 2, 10, 10},
		{30, "raidz2", 3, 4, 7, 11},
		{60, "raidz2", 5, 7, 8, 13},
		{100, "raidz2", 10, 13, 7, 11},
	}

	for _, tc := range cases {
		vdevType, numVdevs, vdevWidth, _ := chooseVdevTopology(tc.n)
		if vdevType != tc.wantType {
			t.Errorf("n=%d: want type=%q, got %q", tc.n, tc.wantType, vdevType)
		}
		if numVdevs < tc.wantMinVdevs || numVdevs > tc.wantMaxVdevs {
			t.Errorf("n=%d: want numVdevs in [%d,%d], got %d", tc.n, tc.wantMinVdevs, tc.wantMaxVdevs, numVdevs)
		}
		if vdevWidth < tc.wantMinWidth || vdevWidth > tc.wantMaxWidth {
			t.Errorf("n=%d: want vdevWidth in [%d,%d], got %d", tc.n, tc.wantMinWidth, tc.wantMaxWidth, vdevWidth)
		}
		// Sanity: numVdevs * vdevWidth should never exceed tc.n + (numVdevs-1).
		// (The last vdev may have fewer drives when tc.n is not evenly divisible.)
		if numVdevs*vdevWidth > tc.n+(numVdevs-1) {
			t.Errorf("n=%d: numVdevs(%d)*vdevWidth(%d)=%d exceeds drive count",
				tc.n, numVdevs, vdevWidth, numVdevs*vdevWidth)
		}
	}
}

// TestRecommendStorage_Raidz1_FourDrives verifies that a 4-HDD storage node
// gets a single raidz1 vdev.
func TestRecommendStorage_Raidz1_FourDrives(t *testing.T) {
	hw := storageHW(6, 0, 0, 4*tb, 0, 0) // 6 HDDs; 2 go to OS, 4 remain
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.ZFSPools) == 0 {
		t.Fatal("expected at least one ZFS pool")
	}
	dataPool := rec.ZFSPools[0]
	if dataPool.VdevType != "raidz1" {
		t.Errorf("want raidz1 for 4 data drives, got %q", dataPool.VdevType)
	}
}

// TestRecommendStorage_Raidz2_EightDrives verifies raidz2 for 8 remaining drives.
func TestRecommendStorage_Raidz2_EightDrives(t *testing.T) {
	hw := storageHW(10, 0, 0, 8*tb, 0, 0) // 10 HDDs; 2 go to OS, 8 remain
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.ZFSPools[0].VdevType != "raidz2" {
		t.Errorf("want raidz2, got %q", rec.ZFSPools[0].VdevType)
	}
}

// TestRecommendStorage_MultiVdev_60Drives verifies that 60 HDDs (58 after OS)
// produce multiple raidz2 vdevs.
func TestRecommendStorage_MultiVdev_60Drives(t *testing.T) {
	hw := storageHW(60, 0, 0, 16*tb, 0, 0) // 60 HDDs; 58 for data
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Stats.VdevCount < 5 {
		t.Errorf("expected 5+ vdevs for 58 data drives, got %d", rec.Stats.VdevCount)
	}
	if rec.Stats.VdevCount > 7 {
		t.Errorf("expected <=7 vdevs for 58 data drives, got %d", rec.Stats.VdevCount)
	}
}

// ─── NVMe SLOG / L2ARC tests ──────────────────────────────────────────────────

// TestRecommendStorage_NVMe_SLOG_Mirror verifies that 2+ NVMe drives produce
// a mirrored SLOG pool plus an L2ARC pool.
func TestRecommendStorage_NVMe_SLOG_Mirror(t *testing.T) {
	// 10 HDDs + 2 NVMe; 2 smallest drives go to OS.
	// HDDs (16 TB) are larger than NVMes (4 TB), so OS goes on the 2 HDDs
	// with the smallest indices — but wait: we pick the 2 smallest overall.
	// NVMes (4 TB) < HDDs (16 TB), so OS drives are the 2 NVMes in this fixture.
	// After OS: 0 NVMes remain, so no SLOG. Let us use a different fixture.

	// 10 HDDs (1 TB each) + 4 NVMe (4 TB each): OS on 2 smallest HDDs.
	hw := storageHW(10, 0, 4, tb, 0, 4*tb)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var hasSLOG, hasL2ARC bool
	for _, p := range rec.ZFSPools {
		if p.Name == "slog" {
			hasSLOG = true
			if p.VdevType != "mirror" {
				t.Errorf("SLOG pool must be a mirror, got %q", p.VdevType)
			}
			if len(p.Members) != 2 {
				t.Errorf("SLOG mirror must have 2 members, got %d", len(p.Members))
			}
			// Members must be partition references (p1 suffix).
			for _, m := range p.Members {
				if !strings.HasSuffix(m, "p1") {
					t.Errorf("SLOG member %q must end with 'p1'", m)
				}
			}
		}
		if p.Name == "l2arc" {
			hasL2ARC = true
		}
	}
	if !hasSLOG {
		t.Error("expected a 'slog' ZFS pool when 2+ NVMe drives are available")
	}
	if !hasL2ARC {
		t.Error("expected an 'l2arc' ZFS pool when NVMe drives are available")
	}
}

// TestRecommendStorage_SingleNVMe_L2ARCOnly verifies that a single NVMe drive
// yields L2ARC only (no SLOG) and emits a warning.
func TestRecommendStorage_SingleNVMe_L2ARCOnly(t *testing.T) {
	// 10 HDDs (1 TB) + 1 NVMe (4 TB): OS on 2 smallest HDDs.
	hw := storageHW(10, 0, 1, tb, 0, 4*tb)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, p := range rec.ZFSPools {
		if p.Name == "slog" {
			t.Error("must NOT have a SLOG pool when only 1 NVMe is present")
		}
	}

	var hasL2ARC bool
	for _, p := range rec.ZFSPools {
		if p.Name == "l2arc" {
			hasL2ARC = true
		}
	}
	if !hasL2ARC {
		t.Error("expected l2arc pool for single NVMe drive")
	}

	// Warning about missing SLOG mirror must be present.
	warnFound := false
	for _, w := range rec.Warnings {
		if strings.Contains(strings.ToLower(w), "slog") {
			warnFound = true
		}
	}
	if !warnFound {
		t.Errorf("expected SLOG warning when only 1 NVMe is present, got warnings: %v", rec.Warnings)
	}
}

// TestRecommendStorage_AllSSD_NoHDD treats SSDs as data drives when no HDDs
// are present.
func TestRecommendStorage_AllSSD_NoHDD(t *testing.T) {
	// 8 SSDs (3.84 TB each), no HDDs, no NVMe.
	hw := storageHW(0, 8, 0, 0, 384*10*uint64(gb), 0)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Stats.DrivesForData != 6 { // 2 of 8 go to OS
		t.Errorf("want DrivesForData=6, got %d", rec.Stats.DrivesForData)
	}
	// 6 SSDs → raidz2 (single vdev)
	if len(rec.ZFSPools) == 0 || rec.ZFSPools[0].VdevType != "raidz2" {
		t.Errorf("expected raidz2 for 6-SSD data pool, got: %+v", rec.ZFSPools)
	}
}

// ─── Stats tests ──────────────────────────────────────────────────────────────

// TestRecommendStorage_Stats_ParityOverhead verifies that parity overhead is
// within a reasonable range for raidz2 configurations.
func TestRecommendStorage_Stats_ParityOverhead(t *testing.T) {
	// 12 HDDs (2 OS + 10 data → single raidz2/10-wide, parity = 2/10 = 20%).
	hw := storageHW(12, 0, 0, 16*tb, 0, 0)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// raidz2/10: 2 parity out of 10 → 20% overhead.
	if rec.Stats.ParityOverhead < 0.15 || rec.Stats.ParityOverhead > 0.25 {
		t.Errorf("expected parity overhead ~0.20 for raidz2/10-wide, got %.3f", rec.Stats.ParityOverhead)
	}
	if rec.Stats.UsableCapacityBytes >= rec.Stats.RawCapacityBytes {
		t.Errorf("usable (%d) must be less than raw (%d)", rec.Stats.UsableCapacityBytes, rec.Stats.RawCapacityBytes)
	}
}

// ─── Firmware detection tests ─────────────────────────────────────────────────

// TestRecommendStorage_FirmwareUEFI confirms ESP is present in OS layout for UEFI.
func TestRecommendStorage_FirmwareUEFI(t *testing.T) {
	hw := storageHW(6, 0, 0, 8*tb, 0, 0)
	rec, err := RecommendStorage(hw, "uefi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var hasESP bool
	for _, p := range rec.OSLayout.Partitions {
		if p.MountPoint == "/boot/efi" {
			hasESP = true
		}
	}
	if !hasESP {
		t.Errorf("expected /boot/efi ESP partition in UEFI storage OS layout")
	}
}

// TestRecommendStorage_FirmwareBIOS confirms biosboot partition is present for BIOS.
func TestRecommendStorage_FirmwareBIOS(t *testing.T) {
	hw := storageHW(6, 0, 0, 8*tb, 0, 0)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var hasBiosBoot bool
	for _, p := range rec.OSLayout.Partitions {
		if p.Filesystem == "biosboot" {
			hasBiosBoot = true
		}
		for _, f := range p.Flags {
			if f == "bios_grub" {
				hasBiosBoot = true
			}
		}
	}
	if !hasBiosBoot {
		t.Errorf("expected biosboot partition in BIOS storage OS layout")
	}
}

// ─── Reasoning output tests ───────────────────────────────────────────────────

// TestRecommendStorage_ReasoningNotEmpty checks that reasoning is non-empty.
func TestRecommendStorage_ReasoningNotEmpty(t *testing.T) {
	hw := storageHW(10, 0, 2, 8*tb, 0, 4*tb)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Reasoning) == 0 {
		t.Error("expected non-empty Reasoning slice")
	}
}

// TestRecommendStorage_ZFSPoolProperties checks that data pool has key properties set.
func TestRecommendStorage_ZFSPoolProperties(t *testing.T) {
	hw := storageHW(8, 0, 0, 8*tb, 0, 0)
	rec, err := RecommendStorage(hw, "bios")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.ZFSPools) == 0 {
		t.Fatal("expected at least one ZFS pool")
	}
	props := rec.ZFSPools[0].Properties
	if props["ashift"] != "12" {
		t.Errorf("expected ashift=12 in pool properties, got %q", props["ashift"])
	}
	if props["compression"] != "lz4" {
		t.Errorf("expected compression=lz4 in pool properties, got %q", props["compression"])
	}
}

// ─── Classify disks tests ─────────────────────────────────────────────────────

// TestClassifyDisks verifies that transport and rotational flag are used correctly.
func TestClassifyDisks(t *testing.T) {
	disks := []hardware.Disk{
		{Name: "sda", Transport: "sata", Rotational: true},
		{Name: "sdb", Transport: "sata", Rotational: false},
		{Name: "nvme0n1", Transport: "nvme", Rotational: false},
		{Name: "nvme1n1", Transport: "", Rotational: false}, // name-based NVMe detection
	}
	hdds, ssds, nvmes := classifyDisks(disks)
	if len(hdds) != 1 || hdds[0].Name != "sda" {
		t.Errorf("expected 1 HDD (sda), got %+v", hdds)
	}
	if len(ssds) != 1 || ssds[0].Name != "sdb" {
		t.Errorf("expected 1 SSD (sdb), got %+v", ssds)
	}
	if len(nvmes) != 2 {
		t.Errorf("expected 2 NVMe drives, got %d: %+v", len(nvmes), nvmes)
	}
}

// ─── Constants used in tests ──────────────────────────────────────────────────

const (
	tb = uint64(1) << 40 // 1 TiB in bytes (close enough for test sizing)
)
