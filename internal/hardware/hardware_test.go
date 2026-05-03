package hardware

import (
	"os"
	"strings"
	"testing"
)

// --- CPU tests ---

func TestParseCPUInfo_SingleSocket(t *testing.T) {
	f, err := os.Open("testdata/cpuinfo_single_socket.txt")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	cpus, err := parseCPUInfo(f)
	if err != nil {
		t.Fatalf("parseCPUInfo: %v", err)
	}

	if len(cpus) != 1 {
		t.Fatalf("expected 1 physical CPU, got %d", len(cpus))
	}

	cpu := cpus[0]
	if !strings.Contains(cpu.Model, "Xeon") {
		t.Errorf("unexpected model: %q", cpu.Model)
	}
	if cpu.Cores != 18 {
		t.Errorf("expected 18 cores, got %d", cpu.Cores)
	}
	if cpu.Threads != 36 {
		t.Errorf("expected 36 threads, got %d", cpu.Threads)
	}
	if cpu.MHz != 3000.0 {
		t.Errorf("expected 3000.0 MHz, got %f", cpu.MHz)
	}
	if !containsFlag(cpu.Flags, "avx512f") {
		t.Error("expected avx512f in flags")
	}
}

func TestParseCPUInfo_DualSocket(t *testing.T) {
	f, err := os.Open("testdata/cpuinfo_dual_socket.txt")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	cpus, err := parseCPUInfo(f)
	if err != nil {
		t.Fatalf("parseCPUInfo: %v", err)
	}

	if len(cpus) != 2 {
		t.Fatalf("expected 2 physical CPUs (dual socket), got %d", len(cpus))
	}
}

func TestParseCPUInfo_Empty(t *testing.T) {
	cpus, err := parseCPUInfo(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(cpus) != 0 {
		t.Errorf("expected 0 CPUs for empty input, got %d", len(cpus))
	}
}

func TestParseCPUInfo_NoPhysicalID(t *testing.T) {
	// Some VMs omit "physical id" — we should still return one CPU entry.
	input := `processor	: 0
model name	: QEMU Virtual CPU
cpu cores	: 4
siblings	: 4
cpu MHz		: 2000.000
flags		: fpu sse sse2

`
	cpus, err := parseCPUInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseCPUInfo: %v", err)
	}
	if len(cpus) != 1 {
		t.Fatalf("expected 1 CPU without physical id, got %d", len(cpus))
	}
	if cpus[0].Cores != 4 {
		t.Errorf("expected 4 cores, got %d", cpus[0].Cores)
	}
}

// --- Memory tests ---

func TestParseMemInfo(t *testing.T) {
	f, err := os.Open("testdata/meminfo.txt")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	mem, err := parseMemInfo(f)
	if err != nil {
		t.Fatalf("parseMemInfo: %v", err)
	}

	if mem.TotalKB != 131906712 {
		t.Errorf("MemTotal: expected 131906712, got %d", mem.TotalKB)
	}
	if mem.AvailableKB != 98324512 {
		t.Errorf("MemAvailable: expected 98324512, got %d", mem.AvailableKB)
	}
	if mem.SwapTotalKB != 8388604 {
		t.Errorf("SwapTotal: expected 8388604, got %d", mem.SwapTotalKB)
	}
}

func TestParseMemInfo_MissingFields(t *testing.T) {
	input := "MemTotal: 4096 kB\n"
	mem, err := parseMemInfo(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mem.TotalKB != 4096 {
		t.Errorf("expected 4096, got %d", mem.TotalKB)
	}
	if mem.AvailableKB != 0 {
		t.Errorf("expected 0 for missing MemAvailable, got %d", mem.AvailableKB)
	}
}

// --- Disk tests ---

func TestParseLsblkJSON(t *testing.T) {
	raw, err := os.ReadFile("testdata/lsblk.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	disks, err := parseLsblkJSON(raw)
	if err != nil {
		t.Fatalf("parseLsblkJSON: %v", err)
	}

	if len(disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(disks))
	}

	sda := disks[0]
	if sda.Name != "sda" {
		t.Errorf("expected sda, got %q", sda.Name)
	}
	if sda.Size != 480103981056 {
		t.Errorf("unexpected size: %d", sda.Size)
	}
	if sda.Transport != "sata" {
		t.Errorf("expected sata transport, got %q", sda.Transport)
	}
	if sda.Rotational {
		t.Error("expected sda to be non-rotational (SSD)")
	}
	if sda.PtType != "gpt" {
		t.Errorf("expected gpt, got %q", sda.PtType)
	}
	if len(sda.Partitions) != 3 {
		t.Fatalf("expected 3 partitions on sda, got %d", len(sda.Partitions))
	}

	efi := sda.Partitions[0]
	if efi.Name != "sda1" {
		t.Errorf("expected sda1, got %q", efi.Name)
	}
	if efi.FSType != "vfat" {
		t.Errorf("expected vfat, got %q", efi.FSType)
	}
	if efi.MountPoint != "/boot/efi" {
		t.Errorf("expected /boot/efi, got %q", efi.MountPoint)
	}

	sdb := disks[1]
	if !sdb.Rotational {
		t.Error("expected sdb to be rotational (HDD)")
	}
	if sdb.PhySector != 4096 {
		t.Errorf("expected 4096 physical sector, got %d", sdb.PhySector)
	}
}

func TestParseLsblkJSON_SkipsNonDisk(t *testing.T) {
	// lsblk output that contains a loop device — should be excluded.
	input := `{"blockdevices":[
		{"name":"loop0","size":102400,"type":"loop","model":"","serial":"","fstype":"squashfs","mountpoint":"/snap/core","tran":"","rota":false,"phy-sec":512,"log-sec":512,"pttype":"","ptuuid":"","partuuid":"","parttype":"","partlabel":"","children":null},
		{"name":"sda","size":500107862016,"type":"disk","model":"WD Blue","serial":"WD-001","fstype":null,"mountpoint":null,"tran":"sata","rota":true,"phy-sec":512,"log-sec":512,"pttype":"gpt","ptuuid":"aabbccdd","partuuid":null,"parttype":null,"partlabel":null,"children":null}
	]}`

	disks, err := parseLsblkJSON([]byte(input))
	if err != nil {
		t.Fatalf("parseLsblkJSON: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk (loop excluded), got %d", len(disks))
	}
	if disks[0].Name != "sda" {
		t.Errorf("expected sda, got %q", disks[0].Name)
	}
}

func TestParseLsblkJSON_StringSize(t *testing.T) {
	// Some lsblk versions quote the size as a string.
	input := `{"blockdevices":[
		{"name":"nvme0n1","size":"256060514304","type":"disk","model":"Samsung PM983","serial":"S3EVNX0M987654","fstype":null,"mountpoint":null,"tran":"nvme","rota":false,"phy-sec":512,"log-sec":512,"pttype":"gpt","ptuuid":"deadbeef","partuuid":null,"parttype":null,"partlabel":null,"children":null}
	]}`

	disks, err := parseLsblkJSON([]byte(input))
	if err != nil {
		t.Fatalf("parseLsblkJSON with string size: %v", err)
	}
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	if disks[0].Size != 256060514304 {
		t.Errorf("unexpected size: %d", disks[0].Size)
	}
}

// --- helpers ---

func containsFlag(flags []string, target string) bool {
	for _, f := range flags {
		if f == target {
			return true
		}
	}
	return false
}
