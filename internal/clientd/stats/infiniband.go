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

// InfiniBandPlugin reports per-port IB state by parsing `ibstat` output.
//
// The plugin silently emits no samples when:
//   - The `ibstat` binary is not installed.
//   - No IB hardware is present (ibstat returns an error or empty output).
//
// Sensors produced (label port=<CA>/<port>):
//   - "state"       — 0=Down, 1=Init, 2=Armed, 3=Active  (unit: count)
//   - "rate_gbps"   — link rate in Gbps                   (unit: gbps)
//   - "link_width"  — link width (1=1x, 4=4x, 8=8x, 12=12x) (unit: count)
//   - "symbol_errors"    — symbol error counter (unit: count)
//   - "link_error_recovery" — link error recovery counter (unit: count)
type InfiniBandPlugin struct{}

// NewInfiniBandPlugin creates an InfiniBandPlugin.
func NewInfiniBandPlugin() *InfiniBandPlugin { return &InfiniBandPlugin{} }

func (p *InfiniBandPlugin) Name() string { return "infiniband" }

func (p *InfiniBandPlugin) Collect(ctx context.Context) []Sample {
	ibstatPath, err := exec.LookPath("ibstat")
	if err != nil {
		return nil // binary not installed — stay silent
	}

	out, err := runWithTimeout(ctx, ibstatPath)
	if err != nil {
		log.Debug().Err(err).Msg("stats/infiniband: ibstat failed (non-fatal)")
		return nil
	}

	return parseIbstat(out)
}

// ibPort represents one parsed CA+port entry from ibstat output.
type ibPort struct {
	ca         string
	portNum    string
	state      string // "Active", "Down", "Initializing", "Armed"
	rateGbps   float64
	linkWidth  int
	symErrors  uint64
	linkErrRec uint64
}

// ibStateVal converts an IB port state string to a numeric value.
func ibStateVal(state string) float64 {
	switch strings.ToLower(state) {
	case "down":
		return 0
	case "initializing", "init":
		return 1
	case "armed":
		return 2
	case "active":
		return 3
	default:
		return 0
	}
}

// parseIbstat parses the text output of `ibstat`.
//
// Example ibstat output structure:
//
//	CA 'mlx5_0'
//	        CA type: MT4117
//	        ...
//	        Port 1:
//	                State: Active
//	                Physical state: LinkUp
//	                Rate: 100
//	                Width: 4X
//	                ...
//	                Symbol errors: 0
//	                Link error recovery: 0
func parseIbstat(output []byte) []Sample {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	now := time.Now().UTC()

	var samples []Sample
	var currentCA string
	var currentPort *ibPort

	flush := func() {
		if currentPort == nil {
			return
		}
		portKey := currentCA + "/" + currentPort.portNum
		labels := map[string]string{"port": portKey}
		samples = append(samples,
			Sample{Sensor: "state", Value: ibStateVal(currentPort.state), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "rate_gbps", Value: currentPort.rateGbps, Unit: "gbps", Labels: labels, TS: now},
			Sample{Sensor: "link_width", Value: float64(currentPort.linkWidth), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "symbol_errors", Value: float64(currentPort.symErrors), Unit: "count", Labels: labels, TS: now},
			Sample{Sensor: "link_error_recovery", Value: float64(currentPort.linkErrRec), Unit: "count", Labels: labels, TS: now},
		)
		currentPort = nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// CA line: "CA 'mlx5_0'"
		if strings.HasPrefix(trimmed, "CA '") {
			flush()
			ca := strings.TrimPrefix(trimmed, "CA '")
			ca = strings.TrimSuffix(ca, "'")
			currentCA = ca
			continue
		}

		// Port line: "Port 1:"
		if strings.HasPrefix(trimmed, "Port ") && strings.HasSuffix(trimmed, ":") {
			flush()
			portNum := strings.TrimSuffix(strings.TrimPrefix(trimmed, "Port "), ":")
			currentPort = &ibPort{ca: currentCA, portNum: portNum, linkWidth: 1}
			continue
		}

		if currentPort == nil {
			continue
		}

		if kv := parseKV(trimmed); len(kv) == 2 {
			key, val := kv[0], kv[1]
			switch key {
			case "State":
				currentPort.state = val
			case "Rate":
				// Rate is in Gbps, e.g. "100" or "56"
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					currentPort.rateGbps = v
				}
			case "Width":
				// Width: "4X", "1X", "8X", "12X"
				w := strings.TrimSuffix(val, "X")
				if v, err := strconv.Atoi(w); err == nil {
					currentPort.linkWidth = v
				}
			case "Symbol errors":
				if v, err := strconv.ParseUint(val, 10, 64); err == nil {
					currentPort.symErrors = v
				}
			case "Link error recovery":
				if v, err := strconv.ParseUint(val, 10, 64); err == nil {
					currentPort.linkErrRec = v
				}
			}
		}
	}
	flush()
	return samples
}

// parseKV splits "Key: Value" into ["Key", "Value"]. Returns nil if not KV format.
func parseKV(line string) []string {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return nil
	}
	return []string{
		strings.TrimSpace(line[:idx]),
		strings.TrimSpace(line[idx+1:]),
	}
}
