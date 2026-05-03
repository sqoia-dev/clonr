package stats

import (
	"context"
	"testing"
)

func TestCPUPlugin_LoadAverages(t *testing.T) {
	p := &CPUPlugin{
		prevTotal: make(map[string]cpuStat),
		procStat:  "testdata/proc_stat",
		procLoad:  "testdata/proc_loadavg",
	}

	samples := p.Collect(context.Background())

	sensorMap := make(map[string]float64)
	for _, s := range samples {
		if s.Labels == nil {
			sensorMap[s.Sensor] = s.Value
		}
	}

	if got := sensorMap["load1"]; got != 0.42 {
		t.Errorf("load1: want 0.42 got %v", got)
	}
	if got := sensorMap["load5"]; got != 0.38 {
		t.Errorf("load5: want 0.38 got %v", got)
	}
	if got := sensorMap["load15"]; got != 0.25 {
		t.Errorf("load15: want 0.25 got %v", got)
	}
}

func TestCPUPlugin_NoUtilOnFirstCall(t *testing.T) {
	// First call cannot produce util_pct because there is no previous sample.
	p := &CPUPlugin{
		prevTotal: make(map[string]cpuStat),
		procStat:  "testdata/proc_stat",
		procLoad:  "testdata/proc_loadavg",
	}

	samples := p.Collect(context.Background())
	for _, s := range samples {
		if s.Sensor == "util_pct" {
			t.Errorf("first call should produce no util_pct samples; got one for labels %v", s.Labels)
		}
	}
}

func TestCPUPlugin_UtilOnSecondCall(t *testing.T) {
	p := &CPUPlugin{
		prevTotal: make(map[string]cpuStat),
		procStat:  "testdata/proc_stat",
		procLoad:  "testdata/proc_loadavg",
	}

	p.Collect(context.Background()) // seed prevTotal

	// Simulate elapsed time by manually lowering the prev counters so that
	// the second collect (same fixture) produces a positive delta.
	// cpu aggregate: user=100000, idle=800000 → total=938500, busy=133500
	// Set prev to lower values so delta > 0.
	p.prevTotal["cpu"] = cpuStat{user: 90000, system: 20000, idle: 750000}
	p.prevTotal["cpu0"] = cpuStat{user: 45000, system: 10000, idle: 375000}
	p.prevTotal["cpu1"] = cpuStat{user: 45000, system: 10000, idle: 375000}

	samples := p.Collect(context.Background())

	foundUtil := false
	for _, s := range samples {
		if s.Sensor == "util_pct" {
			foundUtil = true
			if s.Value < 0 || s.Value > 100 {
				t.Errorf("util_pct out of range: %v", s.Value)
			}
		}
	}
	if !foundUtil {
		t.Error("expected util_pct samples on second call")
	}
}

func TestParseProcStat(t *testing.T) {
	stats, err := parseProcStat("testdata/proc_stat")
	if err != nil {
		t.Fatalf("parseProcStat: %v", err)
	}

	aggregate, ok := stats["cpu"]
	if !ok {
		t.Fatal("missing aggregate 'cpu' entry")
	}
	if aggregate.user != 100000 {
		t.Errorf("cpu.user: want 100000 got %d", aggregate.user)
	}

	cpu0, ok := stats["cpu0"]
	if !ok {
		t.Fatal("missing cpu0 entry")
	}
	if cpu0.idle != 400000 {
		t.Errorf("cpu0.idle: want 400000 got %d", cpu0.idle)
	}
}
