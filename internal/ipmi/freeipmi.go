// Package ipmi — freeipmi.go provides FreeIPMI-based wrappers for power, SEL,
// and sensor operations.
//
// # Why a second backend
//
// The existing Client (ipmi.go) wraps `ipmitool`, which is the historical
// in-tree implementation. Sprint 34 Bundle B (IPMI-MIN) introduces a
// FreeIPMI-based path — `ipmi-power`, `ipmi-sel`, `ipmi-sensors` — because:
//
//  1. FreeIPMI emits structured, comma-separated output that is significantly
//     cheaper to parse than ipmitool's free-form text (ipmi-sensors
//     --no-header-output --comma-separated-output).
//  2. FreeIPMI ships a SOL daemon split (ipmiconsole) that can run unattended
//     more reliably than ipmitool sol activate on long-running BMC sessions.
//  3. The HPC distros clustr targets (RHEL/Rocky 8/9/10) ship FreeIPMI in
//     EPEL and many sites already standardise on it.
//
// # Privilege boundary
//
// On the management host (clustr-serverd's machine) the FreeIPMI binaries do
// NOT need root for remote-out-of-band operations — they only talk LAN+ to a
// remote BMC. To keep one consistent boundary, every FreeIPMI invocation in
// this file is dispatched through the runner abstraction defined below; the
// production runner in clustr-serverd shells out via clustr-privhelper which
// audits + validates argv. Tests inject a mock runner directly so they never
// touch the real binary.
//
// # Argv shape
//
// The wrapper composes argv internally from validated parameters; callers
// never pass raw strings through. Action and SELOp are typed enums; the
// host/user/password come from the BMC config struct.
package ipmi

import (
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strings"
)

// FreeIPMIAction enumerates the power verbs supported by ipmi-power.
type FreeIPMIAction string

const (
	FreeIPMIPowerStatus FreeIPMIAction = "status"
	FreeIPMIPowerOn     FreeIPMIAction = "on"
	FreeIPMIPowerOff    FreeIPMIAction = "off"
	FreeIPMIPowerCycle  FreeIPMIAction = "cycle"
	FreeIPMIPowerReset  FreeIPMIAction = "reset"
)

// FreeIPMISELOp enumerates the SEL verbs supported by ipmi-sel.
type FreeIPMISELOp string

const (
	FreeIPMISELList  FreeIPMISELOp = "list"
	FreeIPMISELClear FreeIPMISELOp = "clear"
)

// FreeIPMIRunner is the exec abstraction for FreeIPMI binaries. The
// production runner shells out via os/exec; tests inject a mock that returns
// canned output without touching the real binaries.
//
// Contract:
//   - argv[0] is the binary name (e.g. "ipmi-power"). The runner is
//     responsible for resolving it on PATH and invoking it.
//   - On exit code 0 the runner returns (stdout, nil).
//   - On any non-zero exit or system error the runner returns ("", err)
//     where err wraps both the exit status and any stderr captured.
//
// Each verb in this file builds the full argv internally so the caller never
// passes raw flag strings — preventing argv injection by construction.
type FreeIPMIRunner interface {
	Run(ctx context.Context, argv ...string) (stdout string, err error)
}

// defaultFreeIPMIRunner is the production runner: it execs the binary
// directly. Use NewExecRunner() to obtain one. SOL is handled separately
// (see SOLBridge) because it streams stdin/stdout instead of running to
// completion.
type defaultFreeIPMIRunner struct{}

// NewExecRunner returns a FreeIPMIRunner that shells out via os/exec.
func NewExecRunner() FreeIPMIRunner { return defaultFreeIPMIRunner{} }

// Run executes argv and returns stdout. Combines stderr into the error on
// non-zero exit so callers see the BMC error message.
func (defaultFreeIPMIRunner) Run(ctx context.Context, argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("freeipmi: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //#nosec G204 -- argv is built internally from validated enums; no caller-supplied strings reach here
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return "", fmt.Errorf("freeipmi: %s: %w: %s", argv[0], err, stderr)
		}
		return "", fmt.Errorf("freeipmi: %s: %w", argv[0], err)
	}
	return string(out), nil
}

// FreeIPMIClient is a thin wrapper around a FreeIPMIRunner that knows how to
// build argv for power/sel/sensor verbs against a single BMC. Construct one
// per request — Client carries credentials, not connection state.
type FreeIPMIClient struct {
	// Host is the BMC LAN address. Empty means in-band/local KCS.
	Host string
	// Username + Password are passed on argv via -u/-p. The clustr-privhelper
	// front-end takes credentials via stdin to keep the password off
	// /proc/<pid>/cmdline; this struct is the in-process shape used by
	// callers that do their own argv-stripping (or by tests).
	Username string
	Password string
	// Runner injects exec; nil means use the production runner.
	Runner FreeIPMIRunner
}

// runner returns c.Runner or the default exec runner.
func (c *FreeIPMIClient) runner() FreeIPMIRunner {
	if c.Runner != nil {
		return c.Runner
	}
	return defaultFreeIPMIRunner{}
}

// commonArgs builds the host/user/pass flags shared by every freeipmi verb.
// Returns an empty slice when c.Host is empty (in-band / local BMC).
func (c *FreeIPMIClient) commonArgs() []string {
	if c.Host == "" {
		return nil
	}
	args := []string{"-h", c.Host}
	if c.Username != "" {
		args = append(args, "-u", c.Username)
	}
	if c.Password != "" {
		args = append(args, "-p", c.Password)
	}
	args = append(args, "--driver-type=LAN_2_0")
	return args
}

// PowerArgv composes the full argv for an ipmi-power invocation.
// Exported for unit tests: callers should prefer the typed Power method.
func PowerArgv(c *FreeIPMIClient, action FreeIPMIAction) ([]string, error) {
	if !validPowerAction(action) {
		return nil, fmt.Errorf("freeipmi: unsupported power action %q", action)
	}
	argv := []string{"ipmi-power"}
	argv = append(argv, c.commonArgs()...)
	switch action {
	case FreeIPMIPowerStatus:
		argv = append(argv, "--stat")
	case FreeIPMIPowerOn:
		argv = append(argv, "--on")
	case FreeIPMIPowerOff:
		argv = append(argv, "--off")
	case FreeIPMIPowerCycle:
		argv = append(argv, "--cycle")
	case FreeIPMIPowerReset:
		argv = append(argv, "--reset")
	}
	return argv, nil
}

// validPowerAction returns true when action is a known enum value.
func validPowerAction(action FreeIPMIAction) bool {
	switch action {
	case FreeIPMIPowerStatus, FreeIPMIPowerOn, FreeIPMIPowerOff,
		FreeIPMIPowerCycle, FreeIPMIPowerReset:
		return true
	}
	return false
}

// Power performs a power action and returns the trimmed output.
func (c *FreeIPMIClient) Power(ctx context.Context, action FreeIPMIAction) (string, error) {
	argv, err := PowerArgv(c, action)
	if err != nil {
		return "", err
	}
	out, err := c.runner().Run(ctx, argv...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SELArgv composes the full argv for an ipmi-sel invocation.
func SELArgv(c *FreeIPMIClient, op FreeIPMISELOp) ([]string, error) {
	if op != FreeIPMISELList && op != FreeIPMISELClear {
		return nil, fmt.Errorf("freeipmi: unsupported sel op %q", op)
	}
	argv := []string{"ipmi-sel"}
	argv = append(argv, c.commonArgs()...)
	switch op {
	case FreeIPMISELList:
		argv = append(argv,
			"--no-header-output",
			"--comma-separated-output",
			"--output-event-state",
		)
	case FreeIPMISELClear:
		argv = append(argv, "--clear")
	}
	return argv, nil
}

// SEL runs the SEL list/clear verb. For list, returns the parsed entries.
// For clear, returns nil entries on success.
func (c *FreeIPMIClient) SEL(ctx context.Context, op FreeIPMISELOp) ([]SELEntry, error) {
	argv, err := SELArgv(c, op)
	if err != nil {
		return nil, err
	}
	out, err := c.runner().Run(ctx, argv...)
	if err != nil {
		return nil, err
	}
	if op == FreeIPMISELClear {
		return nil, nil
	}
	return parseFreeIPMISEL(out), nil
}

// SensorsArgv composes the full argv for an ipmi-sensors invocation.
func SensorsArgv(c *FreeIPMIClient) []string {
	argv := []string{"ipmi-sensors"}
	argv = append(argv, c.commonArgs()...)
	argv = append(argv,
		"--no-header-output",
		"--comma-separated-output",
		"--output-sensor-state",
	)
	return argv
}

// Sensors runs ipmi-sensors and parses the comma-separated output.
func (c *FreeIPMIClient) Sensors(ctx context.Context) ([]Sensor, error) {
	out, err := c.runner().Run(ctx, SensorsArgv(c)...)
	if err != nil {
		return nil, err
	}
	return parseFreeIPMISensors(out), nil
}

// parseFreeIPMISensors parses the comma-separated output of `ipmi-sensors
// --comma-separated-output --output-sensor-state`. Expected columns:
//
//	ID,Name,Type,State,Reading,Units,Event
func parseFreeIPMISensors(out string) []Sensor {
	var sensors []Sensor
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 6 {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(rec[3]))
		var status string
		switch state {
		case "nominal", "ok":
			status = "ok"
		case "warning", "warn":
			status = "warning"
		case "critical":
			status = "critical"
		case "n/a", "na", "":
			status = "ns"
		default:
			status = state
		}
		sensors = append(sensors, Sensor{
			Name:   strings.TrimSpace(rec[1]),
			Value:  strings.TrimSpace(rec[4]),
			Units:  strings.TrimSpace(rec[5]),
			Status: status,
		})
	}
	return sensors
}

// parseFreeIPMISEL parses the comma-separated output of `ipmi-sel --list
// --comma-separated-output --output-event-state`. Expected columns:
//
//	ID,Date,Time,Name,Type,State,Event
func parseFreeIPMISEL(out string) []SELEntry {
	var entries []SELEntry
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 6 {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(rec[5]))
		var sev string
		switch state {
		case "nominal", "ok":
			sev = SELSeverityInfo
		case "warning", "warn":
			sev = SELSeverityWarn
		case "critical":
			sev = SELSeverityCritical
		default:
			sev = SELSeverityInfo
		}
		event := ""
		if len(rec) >= 7 {
			event = strings.TrimSpace(rec[6])
		}
		entries = append(entries, SELEntry{
			ID:       strings.TrimSpace(rec[0]),
			Date:     strings.TrimSpace(rec[1]),
			Time:     strings.TrimSpace(rec[2]),
			Sensor:   strings.TrimSpace(rec[3]),
			Event:    event,
			Severity: sev,
			Raw:      strings.Join(rec, ","),
		})
	}
	return entries
}
