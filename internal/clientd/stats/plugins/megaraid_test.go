package plugins

import (
	"context"
	"testing"

	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

func TestMegaRAID_NoBinaryNoOp(t *testing.T) {
	reg := stats.NewMetricRegistry()
	p := NewMegaRAIDPlugin(reg, func() (string, string) { return "", "" })

	samples := p.Collect(context.Background())
	if samples != nil {
		t.Errorf("expected nil samples when no CLI present, got %+v", samples)
	}
}

func TestMegaRAID_StorcliFixtureParse(t *testing.T) {
	// Simulated `storcli /call show all J` response for a single controller.
	raw := []byte(`{
	  "Controllers": [
	    { "Response Data": {} }
	  ]
	}`)
	mn := megaRAIDMetricNames{ctrlCount: "megaraid_ctrl_count"}
	samples := parseStorcliShowAll(raw, mn)
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	if samples[0].MetricName != "megaraid_ctrl_count" {
		t.Errorf("MetricName = %q, want megaraid_ctrl_count", samples[0].MetricName)
	}
	if samples[0].Value != 1 {
		t.Errorf("Value = %v, want 1 controller", samples[0].Value)
	}
}

func TestMegaRAID_StorcliEmptyControllers(t *testing.T) {
	raw := []byte(`{ "Controllers": [] }`)
	mn := megaRAIDMetricNames{ctrlCount: "megaraid_ctrl_count"}
	samples := parseStorcliShowAll(raw, mn)
	if len(samples) != 0 {
		t.Errorf("empty controller list should yield 0 samples, got %d", len(samples))
	}
}

func TestMegaRAID_MegaCliFixtureParse(t *testing.T) {
	raw := []byte(`
Adapter #0
Adapter Type            : MegaRAID
...
Adapter #1
Adapter Type            : MegaRAID
`)
	mn := megaRAIDMetricNames{ctrlCount: "megaraid_ctrl_count"}
	samples := parseMegaCliAdpAllInfo(raw, mn)
	if len(samples) != 1 {
		t.Fatalf("want 1 sample, got %d", len(samples))
	}
	if samples[0].Value != 2 {
		t.Errorf("Value = %v, want 2 adapters", samples[0].Value)
	}
}

func TestMegaRAID_MegaCliNoAdapters(t *testing.T) {
	raw := []byte(`Exit Code: 0x00\n`)
	mn := megaRAIDMetricNames{ctrlCount: "megaraid_ctrl_count"}
	samples := parseMegaCliAdpAllInfo(raw, mn)
	if len(samples) != 0 {
		t.Errorf("no-adapter MegaCli output should yield 0 samples, got %d", len(samples))
	}
}

func TestMegaRAID_RegistersAllMetrics(t *testing.T) {
	reg := stats.NewMetricRegistry()
	_ = NewMegaRAIDPlugin(reg, nil)
	want := []string{
		"megaraid_ctrl_count",
		"megaraid_vd_count",
		"megaraid_pd_count",
		"megaraid_vd_state",
		"megaraid_pd_state",
		"megaraid_bbu_charge_pct",
		"megaraid_bbu_temp_celsius",
	}
	for _, name := range want {
		if _, ok := reg.Get(name, ""); !ok {
			t.Errorf("expected metric %q registered", name)
		}
	}
	// Verify chart-group is set on at least one entry.
	d, _ := reg.Get("megaraid_ctrl_count", "")
	if d.ChartGroup != "MegaRAID" {
		t.Errorf("ChartGroup = %q, want MegaRAID", d.ChartGroup)
	}
}
