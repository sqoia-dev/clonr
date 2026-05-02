package stats

import (
	"testing"
)

func TestParseDiskstats(t *testing.T) {
	stats, err := parseDiskstats("testdata/proc_diskstats")
	if err != nil {
		t.Fatalf("parseDiskstats: %v", err)
	}

	// sda should be present
	sda, ok := stats["sda"]
	if !ok {
		t.Fatal("expected sda in diskstats")
	}
	if sda.readsCompleted != 100000 {
		t.Errorf("sda.readsCompleted: want 100000 got %d", sda.readsCompleted)
	}
	if sda.readSectors != 2000000 {
		t.Errorf("sda.readSectors: want 2000000 got %d", sda.readSectors)
	}

	// sda1 (partition) should be excluded
	if _, ok := stats["sda1"]; ok {
		t.Error("sda1 (partition) should not be in diskstats result")
	}

	// loop0 should be excluded
	if _, ok := stats["loop0"]; ok {
		t.Error("loop0 should not be in diskstats result")
	}

	// nvme0n1 (device, not partition) should be present
	nvme, ok := stats["nvme0n1"]
	if !ok {
		t.Fatal("expected nvme0n1 in diskstats")
	}
	if nvme.readsCompleted != 200000 {
		t.Errorf("nvme0n1.readsCompleted: want 200000 got %d", nvme.readsCompleted)
	}
}

func TestIsPartition(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"sda", false},
		{"sda1", true},
		{"sda10", true},
		{"nvme0n1", false},
		{"nvme0n1p1", true},
		{"nvme0n1p10", true},
		{"vda", false},
		{"vda1", true},
		{"loop0", false}, // loop devices don't have partition sub-names in typical usage
	}
	for _, tc := range cases {
		got := isPartition(tc.name)
		if got != tc.want {
			t.Errorf("isPartition(%q): want %v got %v", tc.name, tc.want, got)
		}
	}
}
