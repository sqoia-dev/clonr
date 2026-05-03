package stats

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/rs/zerolog/log"
)

// NVMePlugin reports per-device NVMe health metrics by shelling out to `nvme list -o json`.
//
// The plugin silently emits no samples when:
//   - The `nvme` binary is not installed.
//   - No NVMe devices are present.
//
// Sensors produced (label device=<dev>):
//   - "temp_celsius"     — composite temperature    (unit: celsius)
//   - "percentage_used"  — NVM subsystem life used  (unit: pct)
//   - "media_errors"     — media and data integrity errors (unit: count)
//   - "available_spare"  — available spare capacity (unit: pct)
type NVMePlugin struct{}

// NewNVMePlugin creates an NVMePlugin. It checks for the nvme binary at runtime.
func NewNVMePlugin() *NVMePlugin { return &NVMePlugin{} }

func (p *NVMePlugin) Name() string { return "nvme" }

// nvmeListOutput is the top-level structure of `nvme list -o json`.
type nvmeListOutput struct {
	Devices []nvmeDevice `json:"Devices"`
}

type nvmeDevice struct {
	DevicePath string `json:"DevicePath"`
	// Fields from `nvme smart-log -o json` are fetched per device.
}

// nvmeSmartLog is the structure of `nvme smart-log -o json <dev>`.
type nvmeSmartLog struct {
	Temperature    int `json:"temperature"` // in Kelvin
	PercentageUsed int `json:"percent_used"`
	MediaErrors    int `json:"media_errors"`
	AvailableSpare int `json:"avail_spare"`
}

func (p *NVMePlugin) Collect(ctx context.Context) []Sample {
	nvmePath, err := exec.LookPath("nvme")
	if err != nil {
		// Binary not present — stay silent.
		return nil
	}

	// Enumerate devices.
	listOut, err := runWithTimeout(ctx, nvmePath, "list", "-o", "json")
	if err != nil {
		log.Debug().Err(err).Msg("stats/nvme: nvme list failed (non-fatal)")
		return nil
	}

	var listing nvmeListOutput
	if err := json.Unmarshal(listOut, &listing); err != nil {
		log.Debug().Err(err).Msg("stats/nvme: failed to parse nvme list output")
		return nil
	}

	now := time.Now().UTC()
	var samples []Sample

	for _, dev := range listing.Devices {
		path := dev.DevicePath
		if path == "" {
			continue
		}

		smartOut, err := runWithTimeout(ctx, nvmePath, "smart-log", "-o", "json", path)
		if err != nil {
			log.Debug().Err(err).Str("device", path).Msg("stats/nvme: smart-log failed (non-fatal)")
			continue
		}

		var smart nvmeSmartLog
		if err := json.Unmarshal(smartOut, &smart); err != nil {
			log.Debug().Err(err).Str("device", path).Msg("stats/nvme: failed to parse smart-log")
			continue
		}

		labels := map[string]string{"device": path}
		// NVMe temperature is reported in Kelvin; convert to Celsius.
		tempC := float64(smart.Temperature) - 273.15
		samples = append(samples,
			Sample{Sensor: "temp_celsius", Value: tempC, Unit: "celsius", Labels: labels, TS: now},
			Sample{Sensor: "percentage_used", Value: float64(smart.PercentageUsed), Unit: "pct", Labels: labels, TS: now},
			Sample{Sensor: "media_errors", Value: float64(smart.MediaErrors), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "available_spare", Value: float64(smart.AvailableSpare), Unit: "pct", Labels: labels, TS: now},
		)
	}

	return samples
}
