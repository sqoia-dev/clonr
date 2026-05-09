package plugins

import (
	"context"
	"testing"

	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

func TestIntelSSD_NoBinaryNoOp(t *testing.T) {
	reg := stats.NewMetricRegistry()
	p := NewIntelSSDPlugin(reg, func() string { return "" })

	samples := p.Collect(context.Background())
	if samples != nil {
		t.Errorf("expected nil samples when isdct missing, got %+v", samples)
	}
}

func TestIntelSSD_ParseSmartArrayJSON(t *testing.T) {
	raw := []byte(`[
	  {
	    "DevicePath": "/dev/nvme0n1",
	    "MediaWearIndicator": 95,
	    "HostBytesWritten": 12345678,
	    "Temperature": "42 C",
	    "PowerOnHours": 1200,
	    "AvailableSpare": "100%"
	  }
	]`)
	mn := intelSSDMetricNames{
		driveCount:        "intel_ssd_count",
		mediaWearPct:      "intel_ssd_media_wear_pct",
		hostBytesWritten:  "intel_ssd_host_bytes_written",
		tempCelsius:       "intel_ssd_temp_celsius",
		powerOnHours:      "intel_ssd_power_on_hours",
		availableSparePct: "intel_ssd_available_spare_pct",
	}
	samples := parseIsdctSmartJSON(raw, mn)
	if len(samples) != 6 {
		t.Fatalf("want 6 samples, got %d: %+v", len(samples), samples)
	}

	byName := map[string]stats.Sample{}
	for _, s := range samples {
		byName[s.MetricName] = s
		if s.MetricName == "" {
			t.Errorf("sample %q missing MetricName", s.Sensor)
		}
	}
	if byName["intel_ssd_count"].Value != 1 {
		t.Errorf("count = %v, want 1", byName["intel_ssd_count"].Value)
	}
	// MediaWearIndicator=95 → wear_pct = 100-95 = 5
	if byName["intel_ssd_media_wear_pct"].Value != 5 {
		t.Errorf("media_wear_pct = %v, want 5 (inverted from 95)",
			byName["intel_ssd_media_wear_pct"].Value)
	}
	if byName["intel_ssd_temp_celsius"].Value != 42 {
		t.Errorf("temp = %v, want 42", byName["intel_ssd_temp_celsius"].Value)
	}
	if byName["intel_ssd_available_spare_pct"].Value != 100 {
		t.Errorf("available_spare = %v, want 100",
			byName["intel_ssd_available_spare_pct"].Value)
	}
	if byName["intel_ssd_temp_celsius"].Labels["device"] != "/dev/nvme0n1" {
		t.Errorf("device label = %q, want /dev/nvme0n1",
			byName["intel_ssd_temp_celsius"].Labels["device"])
	}
}

func TestIntelSSD_ParseSmartObjectEnvelope(t *testing.T) {
	// Some isdct versions wrap the array in {"Drives": [...]}.
	raw := []byte(`{
	  "Drives": [
	    { "DevicePath": "/dev/nvme0n1", "Temperature": 35 }
	  ]
	}`)
	mn := intelSSDMetricNames{
		driveCount:  "intel_ssd_count",
		tempCelsius: "intel_ssd_temp_celsius",
	}
	samples := parseIsdctSmartJSON(raw, mn)
	if len(samples) != 2 {
		t.Fatalf("want 2 samples (count + temp), got %d", len(samples))
	}
}

func TestIntelSSD_ParseSmartEmpty(t *testing.T) {
	samples := parseIsdctSmartJSON([]byte(`[]`), intelSSDMetricNames{})
	if len(samples) != 0 {
		t.Errorf("empty drive array: want 0 samples, got %d", len(samples))
	}
}

func TestIntelSSD_ParseSmartGarbage(t *testing.T) {
	samples := parseIsdctSmartJSON([]byte(`not json`), intelSSDMetricNames{})
	if len(samples) != 0 {
		t.Errorf("garbage input: want 0 samples, got %d", len(samples))
	}
}

func TestIntelSSD_NumericFieldUnitStripping(t *testing.T) {
	m := map[string]any{
		"AvailableSpare": "85%",
		"Temperature":    "37 C",
		"Other":          float64(1.5),
	}
	if v, ok := numericField(m, "AvailableSpare"); !ok || v != 85 {
		t.Errorf("AvailableSpare: got (%v, %v), want (85, true)", v, ok)
	}
	if v, ok := numericField(m, "Temperature"); !ok || v != 37 {
		t.Errorf("Temperature: got (%v, %v), want (37, true)", v, ok)
	}
	if v, ok := numericField(m, "Other"); !ok || v != 1.5 {
		t.Errorf("Other: got (%v, %v), want (1.5, true)", v, ok)
	}
	if _, ok := numericField(m, "Missing"); ok {
		t.Errorf("Missing key should return ok=false")
	}
}

func TestIntelSSD_RegistersAllMetrics(t *testing.T) {
	reg := stats.NewMetricRegistry()
	_ = NewIntelSSDPlugin(reg, func() string { return "" })
	want := []string{
		"intel_ssd_count",
		"intel_ssd_media_wear_pct",
		"intel_ssd_host_bytes_written",
		"intel_ssd_temp_celsius",
		"intel_ssd_power_on_hours",
		"intel_ssd_available_spare_pct",
	}
	for _, name := range want {
		d, ok := reg.Get(name, "")
		if !ok {
			t.Errorf("missing metric %q", name)
		}
		if d.ChartGroup != "Intel SSD" {
			t.Errorf("metric %q ChartGroup = %q, want Intel SSD",
				name, d.ChartGroup)
		}
	}
}
