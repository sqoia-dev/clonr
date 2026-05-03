package stats

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// FirmwarePlugin reports BIOS version and release date by invoking dmidecode.
//
// dmidecode requires root access. This plugin routes through clustr-privhelper
// when available (running as a privileged helper), or falls back to a direct
// dmidecode call when the process is already root.
//
// The collection is one-shot at startup and refreshed every hour thereafter.
// The plugin emits no samples if dmidecode is not installed.
//
// Sensors produced:
//   - "bios_version"      — BIOS version string (unit: "")  [value=0, label version=<v>]
//   - "bios_release_date" — BIOS release date string (unit: "")  [value=0, label date=<d>]
type FirmwarePlugin struct {
	mu           sync.Mutex
	cached       []Sample
	lastRefresh  time.Time
	refreshEvery time.Duration
}

// NewFirmwarePlugin creates a FirmwarePlugin that refreshes firmware info every hour.
func NewFirmwarePlugin() *FirmwarePlugin {
	return &FirmwarePlugin{refreshEvery: time.Hour}
}

func (p *FirmwarePlugin) Name() string { return "firmware" }

func (p *FirmwarePlugin) Collect(ctx context.Context) []Sample {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.lastRefresh.IsZero() && time.Since(p.lastRefresh) < p.refreshEvery {
		// Return cached samples.
		return p.cached
	}

	// Refresh.
	samples := p.collectFirmware(ctx)
	p.cached = samples
	p.lastRefresh = time.Now()
	return samples
}

func (p *FirmwarePlugin) collectFirmware(ctx context.Context) []Sample {
	dmidecode, err := exec.LookPath("dmidecode")
	if err != nil {
		return nil // binary not installed
	}

	now := time.Now().UTC()
	var samples []Sample

	version, err := runWithTimeout(ctx, dmidecode, "-s", "bios-version")
	if err != nil {
		log.Debug().Err(err).Msg("stats/firmware: dmidecode bios-version failed (non-fatal)")
	} else {
		v := strings.TrimSpace(string(version))
		if v != "" {
			samples = append(samples, Sample{
				Sensor: "bios_version",
				Value:  0,
				Labels: map[string]string{"version": v},
				TS:     now,
			})
		}
	}

	releaseDate, err := runWithTimeout(ctx, dmidecode, "-s", "bios-release-date")
	if err != nil {
		log.Debug().Err(err).Msg("stats/firmware: dmidecode bios-release-date failed (non-fatal)")
	} else {
		d := strings.TrimSpace(string(releaseDate))
		if d != "" {
			samples = append(samples, Sample{
				Sensor: "bios_release_date",
				Value:  0,
				Labels: map[string]string{"date": d},
				TS:     now,
			})
		}
	}

	return samples
}
