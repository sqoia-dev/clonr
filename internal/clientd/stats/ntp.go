package stats

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// NTPPlugin reports NTP synchronisation state by shelling out to `chronyc tracking`.
//
// The plugin silently emits no samples when:
//   - The `chronyc` binary is not installed.
//   - chronyc returns a non-zero exit code (chronyd not running).
//
// Sensors produced (no labels — single NTP state per node):
//   - "stratum"                  — reference stratum            (unit: count)
//   - "system_time_offset_seconds" — system clock offset        (unit: seconds)
//   - "last_offset_seconds"      — last measured clock offset   (unit: seconds)
//   - "rms_offset_seconds"       — RMS of recent clock offsets  (unit: seconds)
//   - "frequency_ppm"            — frequency error              (unit: ppm)
//   - "residual_freq_ppm"        — residual frequency error     (unit: ppm)
//   - "skew_ppm"                 — estimated error bounds       (unit: ppm)
//   - "root_delay_seconds"       — total network path delay     (unit: seconds)
//   - "root_dispersion_seconds"  — total dispersion to root     (unit: seconds)
type NTPPlugin struct{}

// NewNTPPlugin creates an NTPPlugin.
func NewNTPPlugin() *NTPPlugin { return &NTPPlugin{} }

func (p *NTPPlugin) Name() string { return "ntp" }

func (p *NTPPlugin) Collect(ctx context.Context) []Sample {
	chronyPath, err := exec.LookPath("chronyc")
	if err != nil {
		// Binary not present — stay silent.
		return nil
	}

	out, err := runWithTimeout(ctx, chronyPath, "tracking")
	if err != nil {
		log.Debug().Err(err).Msg("stats/ntp: chronyc tracking failed (non-fatal)")
		return nil
	}

	return parseChronycTracking(out)
}

// parseChronycTracking parses standard `chronyc tracking` text output and
// returns a flat set of samples with no labels.
//
// Example output:
//
//	Reference ID    : C0A80101 (ntp1.example.com)
//	Stratum         : 3
//	Ref time (UTC)  : Thu May  1 12:00:00 2026
//	System time     : 0.000012345 seconds slow of NTP time
//	Last offset     : -0.000008234 seconds
//	RMS offset      : 0.000015678 seconds
//	Frequency       : -12.345 ppm slow
//	Residual freq   : -0.012 ppm
//	Skew            : 0.034 ppm
//	Root delay      : 0.023456789 seconds
//	Root dispersion : 0.000987654 seconds
//	Update interval : 64.0 seconds
//	Leap status     : Normal
func parseChronycTracking(data []byte) []Sample {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	now := time.Now().UTC()
	var samples []Sample

	for scanner.Scan() {
		line := scanner.Text()
		kv := splitKV(line)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		switch key {
		case "Stratum":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				samples = append(samples, Sample{Sensor: "stratum", Value: v, Unit: "count", TS: now})
			}

		case "System time":
			// "0.000012345 seconds slow of NTP time" or "0.000012345 seconds fast of NTP time"
			if v, ok := parseSecondsOffset(val); ok {
				samples = append(samples, Sample{Sensor: "system_time_offset_seconds", Value: v, Unit: "seconds", TS: now})
			}

		case "Last offset":
			// "-0.000008234 seconds"
			if v, ok := parseSecondsValue(val); ok {
				samples = append(samples, Sample{Sensor: "last_offset_seconds", Value: v, Unit: "seconds", TS: now})
			}

		case "RMS offset":
			// "0.000015678 seconds"
			if v, ok := parseSecondsValue(val); ok {
				samples = append(samples, Sample{Sensor: "rms_offset_seconds", Value: v, Unit: "seconds", TS: now})
			}

		case "Frequency":
			// "-12.345 ppm slow" or "12.345 ppm fast"
			if v, ok := parsePPMOffset(val); ok {
				samples = append(samples, Sample{Sensor: "frequency_ppm", Value: v, Unit: "ppm", TS: now})
			}

		case "Residual freq":
			// "-0.012 ppm"
			if v, ok := parsePPMValue(val); ok {
				samples = append(samples, Sample{Sensor: "residual_freq_ppm", Value: v, Unit: "ppm", TS: now})
			}

		case "Skew":
			// "0.034 ppm"
			if v, ok := parsePPMValue(val); ok {
				samples = append(samples, Sample{Sensor: "skew_ppm", Value: v, Unit: "ppm", TS: now})
			}

		case "Root delay":
			// "0.023456789 seconds"
			if v, ok := parseSecondsValue(val); ok {
				samples = append(samples, Sample{Sensor: "root_delay_seconds", Value: v, Unit: "seconds", TS: now})
			}

		case "Root dispersion":
			// "0.000987654 seconds"
			if v, ok := parseSecondsValue(val); ok {
				samples = append(samples, Sample{Sensor: "root_dispersion_seconds", Value: v, Unit: "seconds", TS: now})
			}
		}
	}

	return samples
}

// splitKV splits "Key : Value" on the first colon. Returns nil if no colon found.
func splitKV(line string) []string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return nil
	}
	return []string{line[:idx], line[idx+1:]}
}

// parseSecondsValue parses "0.000012345 seconds" → float.
func parseSecondsValue(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseSecondsOffset parses chronyc system time: "0.000012345 seconds slow of NTP time".
// Negates the value when "fast" so the sign convention matches last_offset.
func parseSecondsOffset(s string) (float64, bool) {
	fields := strings.Fields(s)
	// Minimum: "<value> seconds <direction> ..."
	if len(fields) < 3 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	// "slow" = clock is behind NTP → negative offset from NTP perspective (system reads low).
	// We keep the magnitude and flip if "fast".
	if strings.EqualFold(fields[2], "fast") {
		v = -v
	}
	return v, true
}

// parsePPMValue parses "-0.012 ppm" → float.
func parsePPMValue(s string) (float64, bool) {
	fields := strings.Fields(s)
	if len(fields) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parsePPMOffset parses "-12.345 ppm slow" or "12.345 ppm fast".
// Returns a signed value: positive = slow (lagging), negative = fast (leading).
func parsePPMOffset(s string) (float64, bool) {
	fields := strings.Fields(s)
	// Minimum: "<value> ppm <direction>"
	if len(fields) < 3 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	if strings.EqualFold(fields[2], "fast") {
		v = -v
	}
	return v, true
}
