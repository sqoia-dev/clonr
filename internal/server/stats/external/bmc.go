package external

// bmc.go — agent-less BMC sensor collector.
//
// The collector runs ipmi-sensors in CSV mode against a remote BMC
// (LAN_2_0) and stores the parsed sensor map as the latest sample for
// (node_id, source='bmc'). It is a thin shim around
// internal/ipmi.FreeIPMIClient: that package owns the argv builder
// and the parser; we own the orchestration (target list, cadence,
// db write).

import (
	"context"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/ipmi"
)

// BMCSensor is the JSON shape stored under each sensor key in
// node_external_stats.payload_json (source='bmc'). It is a deliberately
// small subset of the ipmi.Sensor struct — units + reading is what the
// UI needs; the raw IPMI sensor type is left for a future extension.
type BMCSensor struct {
	Value  string `json:"value"`
	Unit   string `json:"unit,omitempty"`
	Status string `json:"status,omitempty"`
}

// BMCPayload is the full payload JSON for source='bmc'.
//
// CollectedAt records when we last successfully polled the BMC. It is
// duplicated with the row's last_seen_at column so that callers
// reading the JSON alone (e.g. the API response) get a self-contained
// document.
type BMCPayload struct {
	Sensors     map[string]BMCSensor `json:"sensors"`
	CollectedAt time.Time            `json:"collected_at"`
	Source      string               `json:"source"` // "ipmi-sensors"
	Error       string               `json:"error,omitempty"`
}

// BMCCollector polls a single node's BMC. The pool calls Collect once
// per cadence per (node_id, BMCAddr) tuple.
type BMCCollector struct {
	Runner ipmi.FreeIPMIRunner // nil → production exec runner
}

// Collect runs ipmi-sensors against the supplied BMC and returns the
// parsed payload. On error, returns a payload whose Error field is set
// and Sensors is empty — the caller still writes the row so the UI
// shows "we tried; here's the failure mode" rather than going silent.
func (c *BMCCollector) Collect(ctx context.Context, addr, user, pass string) BMCPayload {
	pl := BMCPayload{
		Sensors:     map[string]BMCSensor{},
		CollectedAt: time.Now().UTC(),
		Source:      "ipmi-sensors",
	}
	if addr == "" {
		pl.Error = "no BMC address configured"
		return pl
	}
	cli := &ipmi.FreeIPMIClient{
		Host:     addr,
		Username: user,
		Password: pass,
		Runner:   c.Runner,
	}
	sensors, err := cli.Sensors(ctx)
	if err != nil {
		pl.Error = err.Error()
		return pl
	}
	for _, s := range sensors {
		// Some BMCs emit duplicate sensor names ("CPU Temp",
		// "CPU Temp"); the second one wins. That matches the UI's
		// single-value-per-name expectation.
		pl.Sensors[s.Name] = BMCSensor{
			Value:  s.Value,
			Unit:   s.Units,
			Status: s.Status,
		}
	}
	return pl
}

// BMCCollectArgv re-exports the sensors argv builder for unit tests
// that want to assert on the exact command line. Keeps the public
// surface small while still letting tests verify argv shape.
func BMCCollectArgv(addr, user, pass string) []string {
	if addr == "" {
		return nil
	}
	cli := &ipmi.FreeIPMIClient{Host: addr, Username: user, Password: pass}
	return ipmi.SensorsArgv(cli)
}

//lint:ignore U1000 sentinel for external BMC callers to errors.Is against; tree-shaken when BMC collector is disabled
// errMissingBMC is returned by Collect when no BMC address is set.
// Exported as a package-level var so callers can errors.Is against it.
var errMissingBMC = fmt.Errorf("bmc collector: no BMC address configured")
