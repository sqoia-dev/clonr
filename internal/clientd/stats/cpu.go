package stats

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// CPUPlugin collects per-core CPU utilisation from /proc/stat and load averages
// from /proc/loadavg.
//
// Sensors produced:
//   - "load1"          — 1-minute load average     (unit: count)
//   - "load5"          — 5-minute load average     (unit: count)
//   - "load15"         — 15-minute load average    (unit: count)
//   - "util_pct"       — aggregate CPU utilisation (unit: pct)
//   - "util_pct"       — per-core utilisation      (unit: pct, label cpu=cpu0…)
type CPUPlugin struct {
	// prevCPU stores the raw /proc/stat counters from the last Collect call.
	// First call always returns per-core util_pct 0 because we need two samples
	// to compute a delta; subsequent calls return accurate values.
	prevTotal map[string]cpuStat
	procStat  string // injectable for tests
	procLoad  string // injectable for tests
}

type cpuStat struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (s cpuStat) total() uint64 {
	return s.user + s.nice + s.system + s.idle + s.iowait + s.irq + s.softirq + s.steal
}
func (s cpuStat) busy() uint64 {
	return s.user + s.nice + s.system + s.irq + s.softirq + s.steal
}

// NewCPUPlugin creates a CPUPlugin that reads from /proc/stat and /proc/loadavg.
func NewCPUPlugin() *CPUPlugin {
	return &CPUPlugin{
		prevTotal: make(map[string]cpuStat),
		procStat:  "/proc/stat",
		procLoad:  "/proc/loadavg",
	}
}

func (p *CPUPlugin) Name() string { return "cpu" }

func (p *CPUPlugin) Collect(_ context.Context) []Sample {
	var samples []Sample
	now := time.Now().UTC()

	// Load averages from /proc/loadavg
	if raw, err := os.ReadFile(p.procLoad); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 3 {
			for i, sensor := range []string{"load1", "load5", "load15"} {
				if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
					samples = append(samples, Sample{Sensor: sensor, Value: v, Unit: "count", TS: now})
				}
			}
		}
	}

	// Per-core utilisation from /proc/stat
	current, err := parseProcStat(p.procStat)
	if err == nil {
		for name, cur := range current {
			if prev, ok := p.prevTotal[name]; ok {
				totalDelta := int64(cur.total()) - int64(prev.total())
				busyDelta := int64(cur.busy()) - int64(prev.busy())
				if totalDelta > 0 {
					pct := float64(busyDelta) / float64(totalDelta) * 100.0
					if pct < 0 {
						pct = 0
					}
					if pct > 100 {
						pct = 100
					}
					s := Sample{Sensor: "util_pct", Value: pct, Unit: "pct", TS: now}
					if name != "cpu" {
						s.Labels = map[string]string{"cpu": name}
					}
					samples = append(samples, s)
				}
			}
		}
		p.prevTotal = current
	}

	return samples
}

// parseProcStat reads /proc/stat (or the injected path) and returns a map of
// cpu_name → cpuStat for all "cpu" and "cpuN" lines.
func parseProcStat(path string) (map[string]cpuStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]cpuStat)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		nums := make([]uint64, 8)
		for i := 1; i < len(fields) && i <= 8; i++ {
			nums[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		result[name] = cpuStat{
			user:    nums[0],
			nice:    nums[1],
			system:  nums[2],
			idle:    nums[3],
			iowait:  nums[4],
			irq:     nums[5],
			softirq: nums[6],
			steal:   nums[7],
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cpu: scan /proc/stat: %w", err)
	}
	return result, nil
}
