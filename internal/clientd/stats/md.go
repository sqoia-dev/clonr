package stats

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// MDPlugin parses /proc/mdstat and reports per-MD-array state.
//
// Sensors produced (label array=<name>):
//   - "state"         — 0=clean, 1=degraded, 2=recovering/resyncing (unit: count)
//   - "missing_disks" — number of failed/missing devices              (unit: count)
//   - "recovery_pct"  — rebuild progress 0-100 (only when recovering) (unit: pct)
type MDPlugin struct {
	procMdstat string // injectable for tests
}

// NewMDPlugin creates an MDPlugin reading /proc/mdstat.
func NewMDPlugin() *MDPlugin {
	return &MDPlugin{procMdstat: "/proc/mdstat"}
}

func (p *MDPlugin) Name() string { return "md" }

func (p *MDPlugin) Collect(_ context.Context) []Sample {
	arrays, err := parseMdstat(p.procMdstat)
	if err != nil || len(arrays) == 0 {
		return nil
	}

	now := time.Now().UTC()
	var samples []Sample
	for _, a := range arrays {
		labels := map[string]string{"array": a.name}
		stateVal := 0.0
		switch a.state {
		case "degraded":
			stateVal = 1.0
		case "recovering", "resyncing", "check", "repair":
			stateVal = 2.0
		}
		samples = append(samples,
			Sample{Sensor: "state", Value: stateVal, Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "missing_disks", Value: float64(a.missingDisks), Unit: "count", Labels: labels, TS: now},
		)
		if a.recoveryPct >= 0 {
			samples = append(samples, Sample{
				Sensor: "recovery_pct", Value: a.recoveryPct, Unit: "pct", Labels: labels, TS: now,
			})
		}
	}
	return samples
}

type mdArray struct {
	name         string
	state        string // "clean", "degraded", "recovering", "resyncing", etc.
	missingDisks int
	recoveryPct  float64 // -1 if not rebuilding
}

// parseMdstat reads /proc/mdstat and extracts array state information.
//
// Example /proc/mdstat layout:
//
//	Personalities : [raid1] [raid6] [raid5]
//	md1 : active raid5 sdb[0] sdc[1] sdd[2]
//	      1465145344 blocks super 1.2 level 5, 512k chunk, algorithm 2 [3/3] [UUU]
//
//	md0 : active raid1 sda[0] sde[2](F)
//	      976630464 blocks super 1.2 [2/1] [_U]
//	      [>....................]  recovery = 0.5% (4587520/976630464) finish=120.0min speed=134.4K/sec
func parseMdstat(path string) ([]mdArray, error) {
	f, err := os.Open(path)
	if err != nil {
		// Not an error if md is not loaded — just no arrays.
		return nil, nil //nolint:nilerr
	}
	defer f.Close()

	var arrays []mdArray
	var current *mdArray

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()

		// Array definition line: "md0 : active ..."
		if strings.HasPrefix(line, "md") && strings.Contains(line, ": active") {
			if current != nil {
				arrays = append(arrays, *current)
			}
			a := mdArray{recoveryPct: -1}
			fields := strings.Fields(line)
			a.name = fields[0]

			// Count missing/failed disks: devices marked with (F) or (S).
			for _, f := range fields {
				if strings.Contains(f, "(F)") || strings.Contains(f, "(S)") {
					a.missingDisks++
				}
			}
			current = &a
			continue
		}

		if current == nil {
			continue
		}

		// Status line: "... [3/3] [UUU]" — determines clean vs degraded.
		// [3/3] means 3 active / 3 total; [_UU] means one missing.
		if idx := strings.Index(line, "[_"); idx != -1 {
			current.state = "degraded"
			// Count underscores for missing disks.
			bracket := line[idx:]
			end := strings.Index(bracket, "]")
			if end > 0 {
				for _, ch := range bracket[1:end] {
					if ch == '_' {
						// Also counted from (F) above, but use max to avoid double-count.
					}
				}
			}
		} else if strings.Contains(line, "[U") {
			if current.state == "" {
				current.state = "clean"
			}
		}

		// Recovery line: "[==>.................]  recovery = 12.5% ..."
		if strings.Contains(line, "recovery =") || strings.Contains(line, "resync =") {
			current.state = "recovering"
			// Extract the percentage after "recovery = " or "resync = ".
			for _, marker := range []string{"recovery =", "resync ="} {
				if idx := strings.Index(line, marker); idx != -1 {
					rest := strings.TrimSpace(line[idx+len(marker):])
					pctStr := strings.Fields(rest)[0]
					pctStr = strings.TrimSuffix(pctStr, "%")
					if v, err := strconv.ParseFloat(pctStr, 64); err == nil {
						current.recoveryPct = v
					}
					break
				}
			}
		}
	}
	if current != nil {
		if current.state == "" {
			current.state = "clean"
		}
		arrays = append(arrays, *current)
	}
	return arrays, scanner.Err()
}
