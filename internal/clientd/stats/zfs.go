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

// ZFSPlugin reports per-pool state by shelling out to `zpool status -p`.
// The `-p` flag requests parseable (non-abbreviated) numbers.
//
// The plugin silently emits no samples when:
//   - The `zpool` binary is not installed.
//   - zpool returns a non-zero exit code (ZFS not loaded, no pools).
//
// Sensors produced per pool (label pool=<name>):
//   - "state"         — pool state encoded as integer (unit: count); see zpoolStateVal
//   - "errors_read"   — read error count across all vdevs  (unit: count)
//   - "errors_write"  — write error count across all vdevs (unit: count)
//   - "errors_cksum"  — checksum error count across all vdevs (unit: count)
//   - "scrub_pct"     — scrub progress 0-100 (unit: pct) — only emitted if scrub in progress
//   - "capacity_pct"  — pool capacity used percent (unit: pct)
//
// Pool state mapping (stable — do not reorder without updating the API):
//
//	0 = ONLINE
//	1 = DEGRADED
//	2 = FAULTED
//	3 = OFFLINE
//	4 = UNAVAIL
//	5 = REMOVED
type ZFSPlugin struct{}

// NewZFSPlugin creates a ZFSPlugin.
func NewZFSPlugin() *ZFSPlugin { return &ZFSPlugin{} }

func (p *ZFSPlugin) Name() string { return "zfs" }

// zpoolStateVal converts a zpool state string to a stable numeric value.
// Unmapped strings return -1 (unknown).
var zpoolStateMap = map[string]float64{
	"ONLINE":   0,
	"DEGRADED": 1,
	"FAULTED":  2,
	"OFFLINE":  3,
	"UNAVAIL":  4,
	"REMOVED":  5,
}

func zpoolStateVal(s string) float64 {
	if v, ok := zpoolStateMap[strings.TrimSpace(s)]; ok {
		return v
	}
	return -1
}

func (p *ZFSPlugin) Collect(ctx context.Context) []Sample {
	zpoolPath, err := exec.LookPath("zpool")
	if err != nil {
		// Binary not present — stay silent.
		return nil
	}

	out, err := runWithTimeout(ctx, zpoolPath, "status", "-p")
	if err != nil {
		log.Debug().Err(err).Msg("stats/zfs: zpool status -p failed (non-fatal)")
		return nil
	}

	pools := parseZpoolStatus(out)
	if len(pools) == 0 {
		return nil
	}

	// Collect capacity from `zpool list -p -H` for each pool.
	capacities := collectZpoolCapacity(ctx, zpoolPath)

	now := time.Now().UTC()
	var samples []Sample

	for _, pool := range pools {
		labels := map[string]string{"pool": pool.name}

		samples = append(samples,
			Sample{Sensor: "state", Value: zpoolStateVal(pool.state), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "errors_read", Value: float64(pool.errRead), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "errors_write", Value: float64(pool.errWrite), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "errors_cksum", Value: float64(pool.errCksum), Unit: "count", Labels: labels, TS: now},
		)

		if pool.scrubPct >= 0 {
			samples = append(samples, Sample{
				Sensor: "scrub_pct", Value: pool.scrubPct, Unit: "pct", Labels: labels, TS: now,
			})
		}

		if cap, ok := capacities[pool.name]; ok {
			samples = append(samples, Sample{
				Sensor: "capacity_pct", Value: cap, Unit: "pct", Labels: labels, TS: now,
			})
		}
	}

	return samples
}

// collectZpoolCapacity runs `zpool list -p -H` and returns a map of pool name → capacity pct.
// On error it returns an empty map; callers must tolerate missing capacity.
func collectZpoolCapacity(ctx context.Context, zpoolPath string) map[string]float64 {
	out, err := runWithTimeout(ctx, zpoolPath, "list", "-p", "-H")
	if err != nil {
		return nil
	}
	return parseZpoolList(out)
}

type zpoolInfo struct {
	name     string
	state    string
	errRead  int64
	errWrite int64
	errCksum int64
	scrubPct float64 // -1 if no scrub in progress
}

// parseZpoolStatus parses the human-readable output of `zpool status -p`.
//
// Example output:
//
//	  pool: tank
//	 state: DEGRADED
//	  scan: scrub in progress since Thu May 1 11:00:00 2026
//	        2.00G scanned out of 10.0G at 512M/s, 0h0m24s to go
//	        256M repaired, 20.00% done
//	config:
//
//		NAME        STATE     READ WRITE CKSUM
//		tank        DEGRADED     0     0     0
//		  mirror-0  DEGRADED     0     0     0
//		    sdb     ONLINE       0     0     0
//		    sdc     FAULTED      1     0     4
func parseZpoolStatus(data []byte) []zpoolInfo {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var pools []zpoolInfo
	var current *zpoolInfo
	inConfig := false

	flush := func() {
		if current != nil {
			if current.state == "" {
				current.state = "ONLINE"
			}
			pools = append(pools, *current)
			current = nil
		}
		inConfig = false
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// New pool block.
		if strings.HasPrefix(trimmed, "pool:") {
			flush()
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			current = &zpoolInfo{name: name, scrubPct: -1}
			continue
		}

		if current == nil {
			continue
		}

		// Pool state line.
		if strings.HasPrefix(trimmed, "state:") {
			current.state = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
			continue
		}

		// Scrub progress line: "X.XX% done" in the scan section.
		if strings.Contains(trimmed, "% done") {
			fields := strings.Fields(trimmed)
			for _, f := range fields {
				if strings.HasSuffix(f, "%") {
					pctStr := strings.TrimSuffix(f, "%")
					if v, err := strconv.ParseFloat(pctStr, 64); err == nil {
						current.scrubPct = v
					}
					break
				}
			}
			continue
		}

		// Config section starts.
		if strings.HasPrefix(trimmed, "config:") {
			inConfig = true
			continue
		}

		if !inConfig {
			continue
		}

		// Config table header — skip.
		if strings.HasPrefix(trimmed, "NAME") {
			continue
		}

		// Pool name row is the first entry in the config table — its fields are the aggregate errors.
		// Format: "<name>  <STATE>  <READ> <WRITE> <CKSUM>"
		// We recognise the pool row by checking if the first field matches the pool name.
		fields := strings.Fields(trimmed)
		if len(fields) >= 5 && fields[0] == current.name {
			current.errRead = parseErrField(fields[2])
			current.errWrite = parseErrField(fields[3])
			current.errCksum = parseErrField(fields[4])
		}
	}
	flush()
	return pools
}

// parseErrField converts a zpool error field to int64.
// zpool uses "0" for no errors. With -p it stays numeric, but without may show
// large numbers. We parse directly; on failure return 0.
func parseErrField(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// parseZpoolList parses `zpool list -p -H` output.
// Tab-separated fields: name, size, alloc, free, ckpoint, expandsz, frag, cap, dedup, health, altroot.
// Field index 7 (cap) is the capacity percentage (integer, no suffix with -p).
func parseZpoolList(data []byte) map[string]float64 {
	result := make(map[string]float64)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		// With -H -p the output is tab-separated with no header.
		// Minimum fields we need: name(0) and cap(7).
		if len(fields) < 8 {
			continue
		}
		name := fields[0]
		capStr := fields[7]
		if v, err := strconv.ParseFloat(capStr, 64); err == nil {
			result[name] = v
		}
	}
	return result
}
