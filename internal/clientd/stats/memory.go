package stats

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// MemoryPlugin collects memory statistics from /proc/meminfo.
//
// Sensors produced (all in bytes unless noted):
//   - "total"     — MemTotal    (unit: bytes)
//   - "used"      — used bytes  (unit: bytes) — total − free − buffers − cached
//   - "free"      — MemFree     (unit: bytes)
//   - "cached"    — Cached      (unit: bytes)
//   - "buffers"   — Buffers     (unit: bytes)
//   - "used_pct"  — used / total × 100 (unit: pct)
type MemoryPlugin struct {
	procMeminfo string // injectable for tests
}

// NewMemoryPlugin creates a MemoryPlugin that reads /proc/meminfo.
func NewMemoryPlugin() *MemoryPlugin {
	return &MemoryPlugin{procMeminfo: "/proc/meminfo"}
}

func (p *MemoryPlugin) Name() string { return "memory" }

func (p *MemoryPlugin) Collect(_ context.Context) []Sample {
	fields, err := parseProcMeminfo(p.procMeminfo)
	if err != nil {
		return nil
	}

	now := time.Now().UTC()

	kbToBytes := func(kb uint64) float64 { return float64(kb) * 1024 }

	total := fields["MemTotal"]
	free := fields["MemFree"]
	buffers := fields["Buffers"]
	cached := fields["Cached"]
	sReclaimable := fields["SReclaimable"]

	// "used" excludes reclaimable memory (matches `free -b` "used" column).
	// used = total - free - buffers - cached - SReclaimable
	usedKB := int64(total) - int64(free) - int64(buffers) - int64(cached) - int64(sReclaimable)
	if usedKB < 0 {
		usedKB = 0
	}

	var samples []Sample
	samples = append(samples,
		Sample{Sensor: "total", Value: kbToBytes(total), Unit: "bytes", TS: now},
		Sample{Sensor: "used", Value: kbToBytes(uint64(usedKB)), Unit: "bytes", TS: now},
		Sample{Sensor: "free", Value: kbToBytes(free), Unit: "bytes", TS: now},
		Sample{Sensor: "cached", Value: kbToBytes(cached), Unit: "bytes", TS: now},
		Sample{Sensor: "buffers", Value: kbToBytes(buffers), Unit: "bytes", TS: now},
	)

	if total > 0 {
		usedPct := float64(usedKB) / float64(total) * 100.0
		samples = append(samples, Sample{Sensor: "used_pct", Value: usedPct, Unit: "pct", TS: now})
	}

	return samples
}

// parseProcMeminfo reads /proc/meminfo and returns a map of field name → kB value.
func parseProcMeminfo(path string) (map[string]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "FieldName:   12345 kB"
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		rest := strings.TrimSpace(line[colonIdx+1:])
		// Strip " kB" suffix
		rest = strings.TrimSuffix(rest, " kB")
		rest = strings.TrimSpace(rest)
		if v, err := strconv.ParseUint(rest, 10, 64); err == nil {
			result[key] = v
		}
	}
	return result, scanner.Err()
}
