// Package plugins contains stats plugins that use the typed MetricRegistry
// (Sprint 38 STAT-REGISTRY).  Each plugin in this package registers its
// metrics with a MetricRegistry at construction time and emits Samples with
// MetricName populated.
//
// Existing plugins in internal/clientd/stats/ (cpu, memory, infiniband.go via
// ibstat, megaraid.go via storcli, …) keep emitting by name without a
// foreign-key — the MetricRegistry is additive, not a forced rewrite.
package plugins

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/clientd/stats"
)

// IBSysfsRoot is the canonical sysfs root for InfiniBand devices.  Tests
// override it to point at a fixture tree.
const IBSysfsRoot = "/sys/class/infiniband"

// InfiniBandSysfsPlugin reads InfiniBand state directly from sysfs at
// /sys/class/infiniband/<dev>/ports/<n>/{state,rate,counters/*}.
//
// This is the registry-aware counterpart to the legacy ibstat-shelling
// plugin in internal/clientd/stats/infiniband.go.  Both plugins co-exist;
// this one is preferred when sysfs is available and is wired through the
// MetricRegistry so the UI gets unit/title/chart-group hints for free.
//
// The plugin emits no samples and no errors when sysfs has no IB devices —
// non-IB hosts stay silent.
type InfiniBandSysfsPlugin struct {
	root  string
	reg   *stats.MetricRegistry
	mname struct {
		state, rateGbps, linkLayer, rxBytes, txBytes, symbolErrors string
	}
}

// NewInfiniBandSysfsPlugin constructs the plugin and registers its metrics
// with the supplied MetricRegistry.  All metrics are registered under
// ChartGroup("InfiniBand") per Sprint 38 plan.
//
// rootOverride is "" in production; tests pass a fixture path.
func NewInfiniBandSysfsPlugin(reg *stats.MetricRegistry, rootOverride string) *InfiniBandSysfsPlugin {
	root := IBSysfsRoot
	if rootOverride != "" {
		root = rootOverride
	}
	p := &InfiniBandSysfsPlugin{root: root, reg: reg}

	// Register the metric *types* (not per-device — Device is set on the
	// per-port Sample at emit time).  The collision-on-(name, device) rule
	// means we register with Device("") here and tag ports via Sample
	// labels.  The UI groups by ChartGroup, not by device.
	p.mname.state = reg.MustRegister(stats.TypeInt, "ib_state",
		stats.Title("InfiniBand port state"),
		stats.Unit("count"),
		stats.ChartGroup("InfiniBand"),
	).Name
	p.mname.rateGbps = reg.MustRegister(stats.TypeFloat, "ib_rate_gbps",
		stats.Title("InfiniBand link rate"),
		stats.Unit("gbps"),
		stats.Upper(400),
		stats.ChartGroup("InfiniBand"),
	).Name
	p.mname.linkLayer = reg.MustRegister(stats.TypeInt, "ib_link_layer",
		stats.Title("InfiniBand link layer (1=IB, 2=Ethernet)"),
		stats.Unit("count"),
		stats.ChartGroup("InfiniBand"),
	).Name
	p.mname.rxBytes = reg.MustRegister(stats.TypeInt, "ib_port_rcv_data_bytes",
		stats.Title("InfiniBand RX bytes"),
		stats.Unit("bytes"),
		stats.ChartGroup("InfiniBand"),
	).Name
	p.mname.txBytes = reg.MustRegister(stats.TypeInt, "ib_port_xmit_data_bytes",
		stats.Title("InfiniBand TX bytes"),
		stats.Unit("bytes"),
		stats.ChartGroup("InfiniBand"),
	).Name
	p.mname.symbolErrors = reg.MustRegister(stats.TypeInt, "ib_symbol_errors",
		stats.Title("InfiniBand symbol error counter"),
		stats.Unit("count"),
		stats.ChartGroup("InfiniBand"),
	).Name

	return p
}

// Name implements stats.Plugin.
func (p *InfiniBandSysfsPlugin) Name() string { return "infiniband_sysfs" }

// Collect walks /sys/class/infiniband/*/ports/* once.  Returns nil silently
// if the root doesn't exist (no IB hardware) or has no readable devices.
func (p *InfiniBandSysfsPlugin) Collect(ctx context.Context) []stats.Sample {
	devs, err := os.ReadDir(p.root)
	if err != nil {
		// Non-fatal: stay silent on hosts without IB.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		log.Debug().Err(err).Str("root", p.root).Msg("stats/ib_sysfs: read root failed")
		return nil
	}

	now := time.Now().UTC()
	var samples []stats.Sample

	for _, dev := range devs {
		if !dev.IsDir() && dev.Type()&os.ModeSymlink == 0 {
			continue
		}
		devName := dev.Name()
		portsDir := filepath.Join(p.root, devName, "ports")
		ports, err := os.ReadDir(portsDir)
		if err != nil {
			continue
		}
		for _, port := range ports {
			portName := port.Name()
			label := devName + "/" + portName
			samples = append(samples, p.collectPort(filepath.Join(portsDir, portName), label, now)...)
		}
	}
	return samples
}

// collectPort reads a single port directory and returns its samples.
// Read errors on individual files yield a missing sample — never a panic.
func (p *InfiniBandSysfsPlugin) collectPort(portDir, label string, now time.Time) []stats.Sample {
	mk := func(metricName string, value float64, unit string) stats.Sample {
		return stats.Sample{
			Sensor:     metricName,
			MetricName: metricName,
			Value:      value,
			Unit:       unit,
			Labels:     map[string]string{"port": label},
			TS:         now,
		}
	}

	var out []stats.Sample

	if v, ok := readState(filepath.Join(portDir, "state")); ok {
		out = append(out, mk(p.mname.state, v, "count"))
	}
	if v, ok := readRateGbps(filepath.Join(portDir, "rate")); ok {
		out = append(out, mk(p.mname.rateGbps, v, "gbps"))
	}
	if v, ok := readLinkLayer(filepath.Join(portDir, "link_layer")); ok {
		out = append(out, mk(p.mname.linkLayer, v, "count"))
	}
	// Counters use kernel-internal "data words" of 4 bytes each — multiply
	// to get bytes, which is the unit we register.
	if v, ok := readUint64(filepath.Join(portDir, "counters", "port_rcv_data")); ok {
		out = append(out, mk(p.mname.rxBytes, float64(v)*4, "bytes"))
	}
	if v, ok := readUint64(filepath.Join(portDir, "counters", "port_xmit_data")); ok {
		out = append(out, mk(p.mname.txBytes, float64(v)*4, "bytes"))
	}
	if v, ok := readUint64(filepath.Join(portDir, "counters", "symbol_error")); ok {
		out = append(out, mk(p.mname.symbolErrors, float64(v), "count"))
	}
	return out
}

// readState parses "state" sysfs file.  Format: "<int>: <name>" e.g.
// "4: ACTIVE" or just "<int>".  Returns the integer.
//
// State mapping (per kernel rdma/ib_verbs.h IB_PORT_*):
//
//	1 = DOWN, 2 = INIT, 3 = ARMED, 4 = ACTIVE, 5 = ACTIVE_DEFER
func readState(path string) (float64, bool) {
	line, ok := readFirstLine(path)
	if !ok {
		return 0, false
	}
	// Take the leading int.
	for i, c := range line {
		if c < '0' || c > '9' {
			if i == 0 {
				return 0, false
			}
			line = line[:i]
			break
		}
	}
	v, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0, false
	}
	return float64(v), true
}

// readRateGbps parses "rate" sysfs file.  Format: "<gbps> Gb/sec (<width>X)"
// e.g. "100 Gb/sec (4X)".  Returns the leading float.
func readRateGbps(path string) (float64, bool) {
	line, ok := readFirstLine(path)
	if !ok {
		return 0, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readLinkLayer parses "link_layer".  Format: "InfiniBand" or "Ethernet".
// We map to ints so the metric stays numeric (1=IB, 2=Ethernet, 0=other).
func readLinkLayer(path string) (float64, bool) {
	line, ok := readFirstLine(path)
	if !ok {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "infiniband":
		return 1, true
	case "ethernet":
		return 2, true
	default:
		return 0, true
	}
}

// readUint64 reads a single uint64 from a sysfs file.
func readUint64(path string) (uint64, bool) {
	line, ok := readFirstLine(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readFirstLine reads the first newline-terminated line of a file.
func readFirstLine(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		return scanner.Text(), true
	}
	return "", false
}
