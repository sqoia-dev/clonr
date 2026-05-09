package plugins

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

// IntelSSDPlugin reports SMART-style attributes for Intel enterprise SSDs
// via the Intel SSD Data Center Tool (`isdct`).  This is intentionally
// distinct from the generic SMART plugin — Intel DC SSDs expose vendor
// attributes (e.g. "host_writes_32MiB", "media_wear_indicator") that
// regular smartctl doesn't surface in a structured form.
//
// Detection: the plugin probes PATH for `isdct`, falling back to the
// historical capitalisation `IsdCt` and then to `intelmas` (the next-gen
// rebrand).  When none is present the plugin returns nil silently with no
// log spam — non-Intel-SSD hosts must stay quiet.
//
// All metrics register under ChartGroup("Intel SSD").
type IntelSSDPlugin struct {
	reg     *stats.MetricRegistry
	finder  func() string // returns "" when no binary on PATH
	mname   intelSSDMetricNames
	timeout time.Duration
}

type intelSSDMetricNames struct {
	driveCount        string
	mediaWearPct      string
	hostBytesWritten  string
	tempCelsius       string
	powerOnHours      string
	availableSparePct string
}

// NewIntelSSDPlugin constructs the plugin and registers its metrics.
// finderOverride is "" in production; tests pass a stub.
func NewIntelSSDPlugin(reg *stats.MetricRegistry, finderOverride func() string) *IntelSSDPlugin {
	finder := defaultIsdctFinder
	if finderOverride != nil {
		finder = finderOverride
	}
	p := &IntelSSDPlugin{reg: reg, finder: finder, timeout: 15 * time.Second}

	cg := stats.ChartGroup("Intel SSD")
	p.mname.driveCount = reg.MustRegister(stats.TypeInt, "intel_ssd_count",
		stats.Title("Intel SSDs detected"), stats.Unit("count"), cg).Name
	p.mname.mediaWearPct = reg.MustRegister(stats.TypeFloat, "intel_ssd_media_wear_pct",
		stats.Title("Intel SSD media wear"),
		stats.Unit("pct"), stats.Upper(100), cg).Name
	p.mname.hostBytesWritten = reg.MustRegister(stats.TypeInt, "intel_ssd_host_bytes_written",
		stats.Title("Intel SSD host bytes written"),
		stats.Unit("bytes"), cg).Name
	p.mname.tempCelsius = reg.MustRegister(stats.TypeFloat, "intel_ssd_temp_celsius",
		stats.Title("Intel SSD temperature"),
		stats.Unit("celsius"), cg).Name
	p.mname.powerOnHours = reg.MustRegister(stats.TypeInt, "intel_ssd_power_on_hours",
		stats.Title("Intel SSD power-on hours"),
		stats.Unit("count"), cg).Name
	p.mname.availableSparePct = reg.MustRegister(stats.TypeFloat, "intel_ssd_available_spare_pct",
		stats.Title("Intel SSD available spare"),
		stats.Unit("pct"), stats.Upper(100), cg).Name

	return p
}

// Name implements stats.Plugin.
func (p *IntelSSDPlugin) Name() string { return "intel_ssd" }

// Collect runs `isdct show -smart` once and parses the JSON output.
// Returns nil silently when the binary is absent.
func (p *IntelSSDPlugin) Collect(ctx context.Context) []stats.Sample {
	bin := p.finder()
	if bin == "" {
		return nil
	}

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	// `-output json` flag is the ergonomic switch for both isdct and intelmas.
	out, err := exec.CommandContext(cctx, bin, "show", "-smart", "-output", "json").Output()
	if err != nil {
		log.Debug().Err(err).Msg("stats/intel_ssd: isdct failed (non-fatal)")
		return nil
	}
	return parseIsdctSmartJSON(out, p.mname)
}

// defaultIsdctFinder probes PATH in preference order: isdct → IsdCt → intelmas.
func defaultIsdctFinder() string {
	for _, cand := range []string{"isdct", "IsdCt", "intelmas"} {
		if path, err := exec.LookPath(cand); err == nil {
			return path
		}
	}
	return ""
}

// isdctSmartResponse is the (lossy) shape we consume from `isdct show -smart`.
//
// isdct's JSON is a top-level array; each element is a map of attribute
// name → value, plus a "DevicePath" key.  We only consume a handful of
// well-known attributes — additions are forward-compatible.
type isdctSmartResponse []map[string]any

// parseIsdctSmartJSON parses the output of `isdct show -smart -output json`.
// Numeric values may be JSON numbers or strings like "100%" — we strip
// trailing units before parsing.
func parseIsdctSmartJSON(raw []byte, mn intelSSDMetricNames) []stats.Sample {
	var resp isdctSmartResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		// Some versions emit an object envelope; try that too.
		var env struct {
			Drives isdctSmartResponse `json:"Drives"`
		}
		if err2 := json.Unmarshal(raw, &env); err2 != nil {
			return nil
		}
		resp = env.Drives
	}
	if len(resp) == 0 {
		return nil
	}

	now := time.Now().UTC()
	var samples []stats.Sample
	mk := func(name string, v float64, unit, dev string) stats.Sample {
		s := stats.Sample{
			Sensor:     name,
			MetricName: name,
			Value:      v,
			Unit:       unit,
			TS:         now,
		}
		if dev != "" {
			s.Labels = map[string]string{"device": dev}
		}
		return s
	}

	samples = append(samples, mk(mn.driveCount, float64(len(resp)), "count", ""))

	for _, drive := range resp {
		dev, _ := drive["DevicePath"].(string)
		if dev == "" {
			dev, _ = drive["DeviceName"].(string)
		}

		if v, ok := numericField(drive, "MediaWearIndicator", "Media_Wearout_Indicator"); ok {
			// Intel reports 0–100; 100 = unworn.  We invert to "wear pct"
			// (0 = unworn, 100 = end-of-life) for ergonomics.
			samples = append(samples, mk(mn.mediaWearPct, 100-v, "pct", dev))
		}
		if v, ok := numericField(drive, "HostBytesWritten", "Host_Writes_32MiB"); ok {
			// "Host_Writes_32MiB" needs scaling; "HostBytesWritten" is raw.
			if _, raw := drive["HostBytesWritten"]; raw {
				samples = append(samples, mk(mn.hostBytesWritten, v, "bytes", dev))
			} else {
				// 32 MiB units → bytes.
				samples = append(samples, mk(mn.hostBytesWritten, v*32*1024*1024, "bytes", dev))
			}
		}
		if v, ok := numericField(drive, "Temperature", "Composite_Temperature_Celsius"); ok {
			samples = append(samples, mk(mn.tempCelsius, v, "celsius", dev))
		}
		if v, ok := numericField(drive, "PowerOnHours", "Power_On_Hours"); ok {
			samples = append(samples, mk(mn.powerOnHours, v, "count", dev))
		}
		if v, ok := numericField(drive, "AvailableSpare", "Available_Spare"); ok {
			samples = append(samples, mk(mn.availableSparePct, v, "pct", dev))
		}
	}
	return samples
}

// numericField pulls a numeric value out of a drive map.  Tries each key in
// order; returns (value, true) on the first successful parse.  Handles
// JSON numbers, integer JSON values, and strings like "85%" or "85 C".
func numericField(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case float64:
			return x, true
		case int:
			return float64(x), true
		case int64:
			return float64(x), true
		case string:
			s := strings.TrimSpace(x)
			// Strip trailing unit suffixes like "%", "C", " C".
			s = strings.TrimRight(s, " %CcFf")
			s = strings.TrimSpace(s)
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}
