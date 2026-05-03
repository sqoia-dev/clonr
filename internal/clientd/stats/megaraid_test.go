package stats

import (
	"context"
	"os"
	"testing"
)

func TestParseMegaRAID_1CtrlDegraded(t *testing.T) {
	data, err := os.ReadFile("testdata/storcli-1ctrl-degraded.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	samples := parseMegaRAID(data)
	if len(samples) == 0 {
		t.Fatal("expected samples, got none")
	}

	// Build a lookup: sensor+ctrl+vd+pd → value.
	type key struct {
		sensor string
		ctrl   string
		vd     string
		pd     string
	}
	byKey := make(map[key]float64)
	for _, s := range samples {
		byKey[key{s.Sensor, s.Labels["ctrl"], s.Labels["vd"], s.Labels["pd"]}] = s.Value
	}

	// Controller 0 should have vd_count = 2.
	if v, ok := byKey[key{"vd_count", "0", "", ""}]; !ok || v != 2.0 {
		t.Errorf("ctrl0 vd_count: want 2, got %v (ok=%v)", v, ok)
	}

	// VD 0/0 is Optl → state 0.
	if v, ok := byKey[key{"vd_state", "0", "0/0", ""}]; !ok || v != 0.0 {
		t.Errorf("VD 0/0 vd_state: want 0 (Optl), got %v (ok=%v)", v, ok)
	}
	// VD 1/0 is Dgrd → state 1.
	if v, ok := byKey[key{"vd_state", "0", "1/0", ""}]; !ok || v != 1.0 {
		t.Errorf("VD 1/0 vd_state: want 1 (Dgrd), got %v (ok=%v)", v, ok)
	}

	// PD 8:0 is Onln → state 0.
	if v, ok := byKey[key{"pd_state", "0", "", "8:0"}]; !ok || v != 0.0 {
		t.Errorf("PD 8:0 pd_state: want 0 (Onln), got %v (ok=%v)", v, ok)
	}
	// PD 8:3 is Rbld → state 2.
	if v, ok := byKey[key{"pd_state", "0", "", "8:3"}]; !ok || v != 2.0 {
		t.Errorf("PD 8:3 pd_state: want 2 (Rbld), got %v (ok=%v)", v, ok)
	}

	// BBU charge = 100.
	if v, ok := byKey[key{"bbu_charge_pct", "0", "", ""}]; !ok || v != 100.0 {
		t.Errorf("ctrl0 bbu_charge_pct: want 100, got %v (ok=%v)", v, ok)
	}
	// BBU temp = 31.
	if v, ok := byKey[key{"bbu_temp_celsius", "0", "", ""}]; !ok || v != 31.0 {
		t.Errorf("ctrl0 bbu_temp_celsius: want 31, got %v (ok=%v)", v, ok)
	}
}

func TestMegaRAIDPlugin_AbsentBinary(t *testing.T) {
	if _, err := findBinary("storcli"); err == nil {
		t.Skip("storcli present in PATH; skipping absent-binary test")
	}

	p := NewMegaRAIDPlugin()
	samples := p.Collect(context.Background())
	if len(samples) != 0 {
		t.Errorf("expected 0 samples when binary absent, got %d", len(samples))
	}
}

func TestVDStateVal(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"Optl", 0},
		{"Dgrd", 1},
		{"Pdgd", 2},
		{"Rbld", 3},
		{"Init", 4},
		{"Fail", 5},
		{"Msng", 6},
		{"Unknown", -1},
		{"", -1},
	}
	for _, c := range cases {
		if got := vdStateVal(c.in); got != c.want {
			t.Errorf("vdStateVal(%q): want %v got %v", c.in, c.want, got)
		}
	}
}

func TestPDStateVal(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"Onln", 0},
		{"Offln", 1},
		{"Rbld", 2},
		{"UBad", 3},
		{"UGood", 4},
		{"Reblg", 5},
		{"Fail", 6},
		{"Unknown", -1},
		{"", -1},
	}
	for _, c := range cases {
		if got := pdStateVal(c.in); got != c.want {
			t.Errorf("pdStateVal(%q): want %v got %v", c.in, c.want, got)
		}
	}
}
