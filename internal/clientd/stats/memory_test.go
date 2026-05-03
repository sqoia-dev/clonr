package stats

import (
	"context"
	"testing"
)

func TestMemoryPlugin_Collect(t *testing.T) {
	p := &MemoryPlugin{procMeminfo: "testdata/proc_meminfo"}
	samples := p.Collect(context.Background())

	if len(samples) == 0 {
		t.Fatal("expected non-empty samples")
	}

	smap := make(map[string]float64)
	for _, s := range samples {
		smap[s.Sensor] = s.Value
	}

	// MemTotal: 16384000 kB → 16777216000 bytes
	wantTotal := float64(16384000 * 1024)
	if smap["total"] != wantTotal {
		t.Errorf("total: want %v got %v", wantTotal, smap["total"])
	}

	// MemFree: 4096000 kB → 4194304000 bytes
	wantFree := float64(4096000 * 1024)
	if smap["free"] != wantFree {
		t.Errorf("free: want %v got %v", wantFree, smap["free"])
	}

	// used_pct must be in (0, 100)
	usedPct := smap["used_pct"]
	if usedPct <= 0 || usedPct >= 100 {
		t.Errorf("used_pct out of range: %v", usedPct)
	}
}

func TestParseProcMeminfo(t *testing.T) {
	fields, err := parseProcMeminfo("testdata/proc_meminfo")
	if err != nil {
		t.Fatalf("parseProcMeminfo: %v", err)
	}

	if fields["MemTotal"] != 16384000 {
		t.Errorf("MemTotal: want 16384000 got %d", fields["MemTotal"])
	}
	if fields["Buffers"] != 512000 {
		t.Errorf("Buffers: want 512000 got %d", fields["Buffers"])
	}
}
