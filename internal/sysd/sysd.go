// Package sysd provides shared helpers for managing systemd units from
// clustr-serverd. All three helpers query systemd live — no cached fields.
//
// Design constraints:
//   - Disable is idempotent: stop + disable + reset-failed, no error if unit is
//     already stopped or does not exist.
//   - Status always reflects actual systemd state (is-active + is-enabled +
//     systemctl show). Never returns a cached value.
//   - Enable checks whether the target port is already bound before starting
//     the unit. If yes it returns ErrPortInUse with the holding PID so the
//     caller can decide to take over or surface the conflict.
package sysd

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// Status holds the live systemd state for a unit.
// Every field is populated on each call to QueryStatus — nothing is cached.
type Status struct {
	// Active is the output of `systemctl is-active <unit>` ("active", "inactive",
	// "activating", "deactivating", "failed", "unknown").
	Active string
	// Enabled is the output of `systemctl is-enabled <unit>` ("enabled",
	// "disabled", "static", "masked", "not-found", etc.).
	Enabled string
	// LoadState is the LoadState property from `systemctl show`.
	LoadState string
	// ActiveState is the ActiveState property from `systemctl show`.
	ActiveState string
	// UnitFileState is the UnitFileState property from `systemctl show`.
	UnitFileState string
}

// EnableResult is returned by Enable on success.
type EnableResult struct {
	Unit string
	Port uint16
}

// PortInUseError is returned by Enable when the target port is already bound
// by a process that is NOT the unit we are about to start.
type PortInUseError struct {
	Port    uint16
	HoldPID int
	Unit    string
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("sysd: port %d is already bound (pid %d); unit %s not started", e.Port, e.HoldPID, e.Unit)
}

// ErrUnitNotFound is returned by QueryStatus when systemd does not know the unit.
var ErrUnitNotFound = errors.New("sysd: unit not found")

// Disable stops, disables, and resets the failed state of a unit.
// Idempotent: if the unit does not exist or is already stopped, the call
// succeeds. The only hard error is a non-zero exit from systemctl with an
// unexpected error message.
func Disable(unit string) error {
	// stop — ignore "not loaded" / "not found" exits (exit code 5).
	if out, err := runSystemctl("stop", unit); err != nil {
		if !isNotLoadedOrNotFound(string(out), err) {
			return fmt.Errorf("sysd: stop %s: %w (output: %s)", unit, err, string(out))
		}
	}

	// disable — ignore "not found" exits.
	if out, err := runSystemctl("disable", unit); err != nil {
		if !isNotLoadedOrNotFound(string(out), err) {
			return fmt.Errorf("sysd: disable %s: %w (output: %s)", unit, err, string(out))
		}
	}

	// reset-failed — always non-fatal; unit may never have failed.
	_, _ = runSystemctl("reset-failed", unit)

	return nil
}

// QueryStatus returns the live systemd state for unit.
// Every call invokes systemctl — no caching.
func QueryStatus(unit string) (Status, error) {
	var s Status

	// is-active
	out, _ := runSystemctl("is-active", unit)
	s.Active = strings.TrimSpace(string(out))
	if s.Active == "" {
		s.Active = "unknown"
	}

	// is-enabled
	out, _ = runSystemctl("is-enabled", unit)
	s.Enabled = strings.TrimSpace(string(out))
	if s.Enabled == "" {
		s.Enabled = "unknown"
	}

	// systemctl show for structured properties.
	showOut, err := runSystemctlArgs("show", unit,
		"--property=LoadState,ActiveState,UnitFileState", "--value")
	if err != nil {
		// Unit not loaded at all — return what we have.
		if isNotLoadedOrNotFound(string(showOut), err) {
			return s, ErrUnitNotFound
		}
		return s, fmt.Errorf("sysd: show %s: %w (output: %s)", unit, err, string(showOut))
	}

	// systemctl show --property=A,B,C --value prints one value per line in
	// the same order as the --property list.
	lines := strings.Split(strings.TrimSpace(string(showOut)), "\n")
	if len(lines) >= 1 {
		s.LoadState = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		s.ActiveState = strings.TrimSpace(lines[1])
	}
	if len(lines) >= 3 {
		s.UnitFileState = strings.TrimSpace(lines[2])
	}

	return s, nil
}

// Enable checks port availability, then enables and starts unit.
// If port is 0 the port-check is skipped.
//
// Port-conflict handling: if the port is already bound, Enable returns a
// *PortInUseError. The caller decides whether to proceed (takeover) or surface
// the error to the operator.
//
// After port-check passes, Enable runs:
//   - systemctl daemon-reload (in case the unit file was just written)
//   - systemctl enable <unit>  (creates the WantedBy symlink)
//   - systemctl start  <unit>
func Enable(unit string, port uint16) (EnableResult, error) {
	if port != 0 {
		if pid, bound := isPortBound(port); bound {
			return EnableResult{}, &PortInUseError{Port: port, HoldPID: pid, Unit: unit}
		}
	}

	// daemon-reload so systemd picks up any newly-written unit file.
	if out, err := runSystemctl("daemon-reload"); err != nil {
		return EnableResult{}, fmt.Errorf("sysd: daemon-reload before enable %s: %w (output: %s)", unit, err, string(out))
	}

	if out, err := runSystemctl("enable", unit); err != nil {
		return EnableResult{}, fmt.Errorf("sysd: enable %s: %w (output: %s)", unit, err, string(out))
	}

	if out, err := runSystemctl("start", unit); err != nil {
		return EnableResult{}, fmt.Errorf("sysd: start %s: %w (output: %s)", unit, err, string(out))
	}

	return EnableResult{Unit: unit, Port: port}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// runSystemctl runs `systemctl <args...>` and returns combined output + error.
func runSystemctl(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}

// runSystemctlArgs runs `systemctl <verb> <unit> <extra...>`.
func runSystemctlArgs(verb, unit string, extra ...string) ([]byte, error) {
	args := append([]string{verb, unit}, extra...)
	return exec.Command("systemctl", args...).CombinedOutput()
}

// isNotLoadedOrNotFound returns true when a systemctl exit indicates the unit
// simply does not exist or was never loaded. Exit code 5 = unit not found in
// many systemd versions; we also check the output string as a safety net.
func isNotLoadedOrNotFound(output string, err error) bool {
	if err == nil {
		return false
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		if code == 5 || code == 1 {
			lc := strings.ToLower(output)
			if strings.Contains(lc, "not found") ||
				strings.Contains(lc, "could not be found") ||
				strings.Contains(lc, "no such file") ||
				strings.Contains(lc, "loaded units listed") ||
				code == 5 {
				return true
			}
		}
	}
	return false
}

// isPortBound checks whether port is currently bound using a connection
// attempt to localhost. Returns (pid, true) when bound — pid is 0 when we
// cannot determine the holding PID (best-effort via /proc/net/tcp).
func isPortBound(port uint16) (int, bool) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		// Also try ":port" in case it is bound on all interfaces.
		conn, err = net.Dial("tcp", fmt.Sprintf("[::]:%d", port))
		if err != nil {
			return 0, false
		}
	}
	conn.Close()

	// Best-effort PID lookup from /proc/net/tcp.
	pid := pidForPort(port)
	return pid, true
}

// pidForPort attempts to find the PID listening on port by scanning
// /proc/net/tcp (IPv4 only). Returns 0 if not determinable.
func pidForPort(port uint16) int {
	// /proc/net/tcp entries use hex local address in little-endian byte order:
	// "0100007F:1770" == 127.0.0.1:6000
	// We search for ":XXXX" where XXXX is the port in uppercase hex.
	portHex := strings.ToUpper(fmt.Sprintf("%04X", port))

	data, err := readFile("/proc/net/tcp")
	if err != nil {
		data, err = readFile("/proc/net/tcp6")
		if err != nil {
			return 0
		}
	}

	// Each line: sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		localAddr := fields[1] // "XXXXXXXX:PPPP"
		parts := strings.SplitN(localAddr, ":", 2)
		if len(parts) == 2 && parts[1] == portHex {
			// fields[9] is inode; we'd need /proc/<pid>/fd to map inode → pid.
			// That walk is expensive; return 0 (best-effort).
			return 0
		}
	}
	return 0
}

// readFile reads a file path — abstracted for testability.
var readFile = func(path string) ([]byte, error) {
	return exec.Command("cat", path).Output()
}

// ButtonState returns the recommended UI button set for a given Status.
// The returned slice contains zero or more of:
//   "enable", "disable", "takeover", "stop", "start"
func ButtonState(s Status) []string {
	active := s.Active == "active" || s.ActiveState == "active"
	enabled := s.Enabled == "enabled" || s.UnitFileState == "enabled"

	switch {
	case active && enabled:
		return []string{"disable"}
	case !active && !enabled:
		return []string{"enable"}
	case active && !enabled:
		// Running but not managed (orphan from prior install).
		return []string{"takeover", "stop"}
	case !active && enabled:
		// Will restart at boot; not running now.
		return []string{"start"}
	default:
		return []string{"enable"}
	}
}

// FormatPort formats a uint16 port as a decimal string for use in error messages.
func FormatPort(port uint16) string {
	return strconv.Itoa(int(port))
}
