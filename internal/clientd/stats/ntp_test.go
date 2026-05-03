package stats

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestParseChronycTracking_Synced(t *testing.T) {
	data, err := os.ReadFile("testdata/chronyc-tracking-synced")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	samples := parseChronycTracking(data)
	if len(samples) == 0 {
		t.Fatal("expected samples, got none")
	}

	// Build a sensor → value lookup.
	byName := make(map[string]float64)
	for _, s := range samples {
		byName[s.Sensor] = s.Value
	}

	// Stratum 3.
	if v, ok := byName["stratum"]; !ok || v != 3.0 {
		t.Errorf("stratum: want 3, got %v (ok=%v)", v, ok)
	}

	// System time: "0.000012345 seconds slow" → positive (slow = lagging).
	if v, ok := byName["system_time_offset_seconds"]; !ok || math.Abs(v-0.000012345) > 1e-12 {
		t.Errorf("system_time_offset_seconds: want ~0.000012345, got %v (ok=%v)", v, ok)
	}

	// Last offset: -0.000008234.
	if v, ok := byName["last_offset_seconds"]; !ok || math.Abs(v-(-0.000008234)) > 1e-12 {
		t.Errorf("last_offset_seconds: want -0.000008234, got %v (ok=%v)", v, ok)
	}

	// RMS offset: 0.000015678.
	if v, ok := byName["rms_offset_seconds"]; !ok || math.Abs(v-0.000015678) > 1e-12 {
		t.Errorf("rms_offset_seconds: want 0.000015678, got %v (ok=%v)", v, ok)
	}

	// Frequency: "-12.345 ppm slow" → -12.345 (we negate for fast, keep sign from value).
	// The fixture value is "-12.345 ppm slow" — the leading minus is on the magnitude.
	// After parsing fields[0] = "-12.345" and direction "slow" → no additional sign flip.
	if v, ok := byName["frequency_ppm"]; !ok || math.Abs(v-(-12.345)) > 1e-10 {
		t.Errorf("frequency_ppm: want -12.345, got %v (ok=%v)", v, ok)
	}

	// Residual freq: -0.012.
	if v, ok := byName["residual_freq_ppm"]; !ok || math.Abs(v-(-0.012)) > 1e-10 {
		t.Errorf("residual_freq_ppm: want -0.012, got %v (ok=%v)", v, ok)
	}

	// Skew: 0.034.
	if v, ok := byName["skew_ppm"]; !ok || math.Abs(v-0.034) > 1e-10 {
		t.Errorf("skew_ppm: want 0.034, got %v (ok=%v)", v, ok)
	}

	// Root delay: 0.023456789.
	if v, ok := byName["root_delay_seconds"]; !ok || math.Abs(v-0.023456789) > 1e-12 {
		t.Errorf("root_delay_seconds: want 0.023456789, got %v (ok=%v)", v, ok)
	}

	// Root dispersion: 0.000987654.
	if v, ok := byName["root_dispersion_seconds"]; !ok || math.Abs(v-0.000987654) > 1e-12 {
		t.Errorf("root_dispersion_seconds: want 0.000987654, got %v (ok=%v)", v, ok)
	}

	// No labels on any sample.
	for _, s := range samples {
		if len(s.Labels) != 0 {
			t.Errorf("sensor %q: unexpected labels %v", s.Sensor, s.Labels)
		}
	}
}

func TestParseChronycTracking_Unsynced(t *testing.T) {
	data, err := os.ReadFile("testdata/chronyc-tracking-unsynced")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	samples := parseChronycTracking(data)
	if len(samples) == 0 {
		t.Fatal("expected samples even when unsynced (stratum=0 is still a valid reading)")
	}

	byName := make(map[string]float64)
	for _, s := range samples {
		byName[s.Sensor] = s.Value
	}

	// Stratum 0 when unsynced.
	if v, ok := byName["stratum"]; !ok || v != 0.0 {
		t.Errorf("stratum: want 0, got %v (ok=%v)", v, ok)
	}
}

func TestNTPPlugin_AbsentBinary(t *testing.T) {
	if _, err := findBinary("chronyc"); err == nil {
		t.Skip("chronyc present in PATH; skipping absent-binary test")
	}

	p := NewNTPPlugin()
	samples := p.Collect(context.Background())
	if len(samples) != 0 {
		t.Errorf("expected 0 samples when binary absent, got %d", len(samples))
	}
}
