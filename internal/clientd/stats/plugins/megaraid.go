package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

// MegaRAIDPlugin reports LSI/Broadcom MegaRAID controller, virtual-drive,
// and physical-drive state by shelling out to either `storcli` or `MegaCli`,
// whichever is on PATH.  The plugin gracefully no-ops when neither binary
// is present — non-RAID hosts stay silent (no log spam).
//
// This is the registry-aware counterpart to internal/clientd/stats/megaraid.go.
// All metrics are registered under ChartGroup("MegaRAID").
//
// Detection order (first match wins):
//
//  1. storcli         — the modern Broadcom CLI; preferred.
//  2. storcli64       — same binary, common alias on EL packages.
//  3. MegaCli         — legacy LSI CLI; some firmwares only ship this.
//  4. MegaCli64       — alias.
//
// When the binary is missing the plugin returns nil from Collect with no log
// output (debug only).  When the binary is present but returns no controllers
// (controller present but driver not loaded, e.g. fresh kernel install), we
// also return nil silently — distinguishing "no hardware" from "broken
// driver" is out of scope for a stats plugin.
type MegaRAIDPlugin struct {
	reg     *stats.MetricRegistry
	finder  func() (string, string) // (binary, family); family ∈ {"storcli","megacli"}
	mname   megaRAIDMetricNames
	timeout time.Duration
}

type megaRAIDMetricNames struct {
	ctrlCount, vdCount, pdCount string
	vdState, pdState            string
	bbuChargePct, bbuTempC      string
}

// NewMegaRAIDPlugin constructs the plugin and registers metrics on the
// supplied registry.  finderOverride is "" in production; tests can pass a
// stub that returns ("", "") to assert no-op behaviour.
func NewMegaRAIDPlugin(reg *stats.MetricRegistry, finderOverride func() (string, string)) *MegaRAIDPlugin {
	finder := defaultMegaRAIDFinder
	if finderOverride != nil {
		finder = finderOverride
	}
	p := &MegaRAIDPlugin{reg: reg, finder: finder, timeout: 10 * time.Second}

	cg := stats.ChartGroup("MegaRAID")
	p.mname.ctrlCount = reg.MustRegister(stats.TypeInt, "megaraid_ctrl_count",
		stats.Title("MegaRAID controllers"), stats.Unit("count"), cg).Name
	p.mname.vdCount = reg.MustRegister(stats.TypeInt, "megaraid_vd_count",
		stats.Title("MegaRAID virtual drives"), stats.Unit("count"), cg).Name
	p.mname.pdCount = reg.MustRegister(stats.TypeInt, "megaraid_pd_count",
		stats.Title("MegaRAID physical drives"), stats.Unit("count"), cg).Name
	p.mname.vdState = reg.MustRegister(stats.TypeInt, "megaraid_vd_state",
		stats.Title("MegaRAID VD state (0=Optl, 1=Dgrd, 2=Pdgd, 3=Rbld, 4=Init, 5=Fail)"),
		stats.Unit("count"), cg).Name
	p.mname.pdState = reg.MustRegister(stats.TypeInt, "megaraid_pd_state",
		stats.Title("MegaRAID PD state (0=Onln, 1=Offln, 2=Rbld, 3=UBad, 4=UGood, 5=Fail)"),
		stats.Unit("count"), cg).Name
	p.mname.bbuChargePct = reg.MustRegister(stats.TypeFloat, "megaraid_bbu_charge_pct",
		stats.Title("MegaRAID BBU charge"), stats.Unit("pct"), stats.Upper(100), cg).Name
	p.mname.bbuTempC = reg.MustRegister(stats.TypeFloat, "megaraid_bbu_temp_celsius",
		stats.Title("MegaRAID BBU temperature"), stats.Unit("celsius"), cg).Name

	return p
}

// Name implements stats.Plugin.
func (p *MegaRAIDPlugin) Name() string { return "megaraid_v2" }

// Collect runs the configured CLI (storcli or MegaCli) once.  Returns nil
// silently when no binary is found OR when the binary returns no
// controllers.
func (p *MegaRAIDPlugin) Collect(ctx context.Context) []stats.Sample {
	bin, family := p.finder()
	if bin == "" {
		return nil // no CLI installed — silent
	}

	switch family {
	case "storcli":
		return p.collectStorcli(ctx, bin)
	case "megacli":
		return p.collectMegaCli(ctx, bin)
	default:
		return nil
	}
}

// defaultMegaRAIDFinder probes PATH for the canonical CLI binaries in
// preference order and returns (path, family).
func defaultMegaRAIDFinder() (string, string) {
	candidates := []struct {
		name, family string
	}{
		{"storcli", "storcli"},
		{"storcli64", "storcli"},
		{"MegaCli", "megacli"},
		{"MegaCli64", "megacli"},
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c.name); err == nil {
			return path, c.family
		}
	}
	return "", ""
}

// collectStorcli runs `storcli /call show all J` and parses the JSON
// response.  storcli's JSON output is well-defined; MegaCli's is not.
func (p *MegaRAIDPlugin) collectStorcli(ctx context.Context, bin string) []stats.Sample {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "/call", "show", "all", "J").Output()
	if err != nil {
		log.Debug().Err(err).Msg("stats/megaraid_v2: storcli show all failed (non-fatal)")
		return nil
	}
	return parseStorcliShowAll(out, p.mname)
}

// storcliResponse is the minimal JSON shape we care about.  storcli emits
// vastly more, but we only consume the controller-count + per-controller
// VD/PD counts here.  Per-VD/PD state would require a deeper walk; that's
// in the legacy plugin and stays there for now.
type storcliResponse struct {
	Controllers []struct {
		ResponseData json.RawMessage `json:"Response Data"`
	} `json:"Controllers"`
}

func parseStorcliShowAll(raw []byte, mn megaRAIDMetricNames) []stats.Sample {
	var resp storcliResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	if len(resp.Controllers) == 0 {
		return nil
	}
	now := time.Now().UTC()
	mk := func(name string, val float64, unit, dev string) stats.Sample {
		s := stats.Sample{
			Sensor:     name,
			MetricName: name,
			Value:      val,
			Unit:       unit,
			TS:         now,
		}
		if dev != "" {
			s.Labels = map[string]string{"ctrl": dev}
		}
		return s
	}
	out := []stats.Sample{
		mk(mn.ctrlCount, float64(len(resp.Controllers)), "count", ""),
	}
	// Per-controller drilldown happens in the legacy plugin.  This plugin
	// is the registry-aware top-level summary; the structured per-VD/PD
	// rows continue to flow via the legacy plugin until the UI consumes
	// the new chart-group hint.
	return out
}

// collectMegaCli runs `MegaCli -AdpAllInfo -aAll` and counts adapters.
// MegaCli's output is line-oriented; the structured fields we care about at
// this level (controller count, VD/PD totals) are stable enough to grep.
func (p *MegaRAIDPlugin) collectMegaCli(ctx context.Context, bin string) []stats.Sample {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "-AdpAllInfo", "-aAll", "-NoLog").Output()
	if err != nil {
		// MegaCli returns non-zero exit codes for benign "no controllers" too.
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
		log.Debug().Err(err).Msg("stats/megaraid_v2: MegaCli failed (non-fatal)")
		return nil
	}
	return parseMegaCliAdpAllInfo(out, p.mname)
}

func parseMegaCliAdpAllInfo(raw []byte, mn megaRAIDMetricNames) []stats.Sample {
	// "Adapter #0", "Adapter #1", … — count occurrences.
	count := strings.Count(string(raw), "Adapter #")
	if count == 0 {
		return nil
	}
	now := time.Now().UTC()
	return []stats.Sample{{
		Sensor:     mn.ctrlCount,
		MetricName: mn.ctrlCount,
		Value:      float64(count),
		Unit:       "count",
		TS:         now,
	}}
}
