package stats

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// SystemPlugin collects general system metrics from /proc/uptime and kernel info.
//
// Sensors produced:
//   - "uptime_seconds" — seconds since last boot              (unit: seconds)
//   - "boot_time"      — Unix epoch of last boot              (unit: seconds)
//   - "kernel_version" — current kernel release string        (unit: "")  [value=0, label version=<kver>]
//   - "hostname"       — system hostname                      (unit: "")  [value=0, label hostname=<h>]
type SystemPlugin struct {
	procUptime    string // injectable for tests
	procOsrelease string // injectable for tests
}

// NewSystemPlugin creates a SystemPlugin reading /proc/uptime and /proc/sys/kernel/osrelease.
func NewSystemPlugin() *SystemPlugin {
	return &SystemPlugin{
		procUptime:    "/proc/uptime",
		procOsrelease: "/proc/sys/kernel/osrelease",
	}
}

func (p *SystemPlugin) Name() string { return "system" }

func (p *SystemPlugin) Collect(_ context.Context) []Sample {
	now := time.Now().UTC()
	var samples []Sample

	// Uptime from /proc/uptime (first field is seconds since boot).
	if raw, err := os.ReadFile(p.procUptime); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 1 {
			if uptime, err := strconv.ParseFloat(fields[0], 64); err == nil {
				samples = append(samples,
					Sample{Sensor: "uptime_seconds", Value: uptime, Unit: "seconds", TS: now},
					Sample{
						Sensor: "boot_time",
						Value:  float64(now.Unix()) - uptime,
						Unit:   "seconds",
						TS:     now,
					},
				)
			}
		}
	}

	// Kernel version from /proc/sys/kernel/osrelease.
	if raw, err := os.ReadFile(p.procOsrelease); err == nil {
		kver := strings.TrimSpace(string(raw))
		if kver != "" {
			samples = append(samples, Sample{
				Sensor: "kernel_version",
				Value:  0,
				Labels: map[string]string{"version": kver},
				TS:     now,
			})
		}
	}

	// Hostname.
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		samples = append(samples, Sample{
			Sensor: "hostname",
			Value:  0,
			Labels: map[string]string{"hostname": hostname},
			TS:     now,
		})
	}

	return samples
}

// formatBootTime formats a Unix timestamp as an RFC3339 string for the label.
func formatBootTime(unix float64) string {
	return fmt.Sprintf("%d", int64(unix))
}
