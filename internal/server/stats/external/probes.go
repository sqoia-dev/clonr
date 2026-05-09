// Package external implements clustr-serverd's agent-less collector
// pool: PROBE-3 reachability probes (ping / ssh-banner / ipmi-mc) and
// EXTERNAL-STATS BMC + SNMP polls. Everything runs server-side in
// goroutines — the target node does NOT need clustr-clientd installed.
//
// This file owns PROBE-3 specifically. The pool dispatcher and the
// EXTERNAL-STATS workers live in pool.go / bmc.go / snmp.go.
//
// Why the three probes:
//   - ping     — answers "is the host's primary NIC up?" Catches the
//     class of failure where the OS panics but the link stays.
//     Implementation: shell out to /usr/bin/ping with -c1 -W2. We
//     intentionally avoid the raw-socket ICMP path so clustr-serverd
//     doesn't need CAP_NET_RAW.
//   - ssh      — answers "is sshd accepting TCP and emitting a banner?"
//     Distinguishes "alive but partitioned" from "alive but kernel
//     wedged". Implementation: net.DialTimeout to TCP/22, read ≤16 B,
//     match the "SSH-2.0-" prefix.
//   - ipmi_mc  — answers "is the BMC's management controller alive?"
//     Independent of the host OS — a healthy BMC stays up across host
//     panics, kernel hangs, and even shutdown. Implementation: shell
//     out to ipmi-sensors over LAN+ with a brief timeout; we only care
//     about exit status, not the parsed sensor list.
//
// The probes run in parallel per node via the pool in pool.go on a
// configurable cadence (default 60 s). Results land in
// node_external_stats with source='probe'.
package external

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is the three-boolean envelope written to
// node_external_stats with source='probe'. The JSON shape matches what
// the API handler returns under "probes": {ping, ssh, ipmi_mc}.
type ProbeResult struct {
	Ping      bool      `json:"ping"`
	SSH       bool      `json:"ssh"`
	IPMIMC    bool      `json:"ipmi_mc"`
	CheckedAt time.Time `json:"checked_at"`
}

// ProbeTargets is the per-node input to RunAllOnce. The pool fills
// these from db.GetNodeConfig: HostIP comes from the primary
// interface; BMCAddr/User/Pass come from the (decrypted) bmc_config.
//
// Empty fields skip the corresponding probe (the result boolean stays
// false). This lets a node with no BMC-config still get the ping +
// ssh probes.
type ProbeTargets struct {
	HostIP   string
	BMCAddr  string
	BMCUser  string
	BMCPass  string
	HostPort int // SSH; 0 → 22
}

// CommandRunner is the exec abstraction. The production runner shells
// out via os/exec; tests inject a deterministic mock.
//
// argv[0] is the binary name, resolved on PATH. ctx must be honoured
// (we set 2-3 s timeouts). Combined exit + stderr is folded into err
// so callers can log a useful message; stdout is the parsed return.
type CommandRunner interface {
	Run(ctx context.Context, argv ...string) (stdout string, err error)
}

// ExecRunner is the production CommandRunner.
type ExecRunner struct{}

// Run executes argv via os/exec, with ctx-based timeout enforcement.
func (ExecRunner) Run(ctx context.Context, argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("probes: empty argv")
	}
	//#nosec G204 -- argv is built internally by *Argv builders below
	// from validated parameters; no caller-supplied strings reach here.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.TrimSpace(string(ee.Stderr))
			if stderr != "" {
				return "", fmt.Errorf("%s: %w: %s", argv[0], err, stderr)
			}
		}
		return "", fmt.Errorf("%s: %w", argv[0], err)
	}
	return string(out), nil
}

// PingArgv builds argv for the ping probe. Single packet, 2-second
// wait deadline, deadline cap of 3 s overall. -n suppresses DNS
// lookups (we already have the IP) so a broken resolver can't make
// a healthy host look unreachable.
//
// Exported for unit tests — the production code calls Probe(...).Ping
// which threads it through the runner.
func PingArgv(host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("probes: ping host empty")
	}
	if !validHostOrIP(host) {
		return nil, fmt.Errorf("probes: ping host not a valid hostname/IP: %q", host)
	}
	return []string{
		"ping",
		"-c", "1",
		"-W", "2",
		"-n",
		host,
	}, nil
}

// IPMIMCArgv builds argv for the BMC management-controller probe. We
// run `ipmi-sensors --no-output --session-timeout=2000
// --retransmission-timeout=500` over LAN+ — the binary connects,
// authenticates, fetches the SDR, and returns 0 on success. We don't
// care about the parsed sensor table; we only check exit status.
//
// Exported for unit tests.
func IPMIMCArgv(addr, user, pass string) ([]string, error) {
	if addr == "" {
		return nil, fmt.Errorf("probes: ipmi-mc addr empty")
	}
	if !validHostOrIP(addr) {
		return nil, fmt.Errorf("probes: ipmi-mc addr not a valid hostname/IP: %q", addr)
	}
	argv := []string{
		"ipmi-sensors",
		"-h", addr,
		"--driver-type=LAN_2_0",
		"--session-timeout=2000",
		"--retransmission-timeout=500",
		"--no-output",
	}
	if user != "" {
		argv = append(argv, "-u", user)
	}
	if pass != "" {
		argv = append(argv, "-p", pass)
	}
	return argv, nil
}

// SSHBannerProbe is exposed for testing. It dials addr:port (default
// 22), reads up to bannerReadLimit bytes within timeout, and reports
// whether the response begins with "SSH-2.0-" (per RFC 4253). RFC 4253
// also permits "SSH-1.99-" for transitional servers; we accept that
// too so we don't false-negative against legacy hardware BMCs whose
// SOL-bridge sshd is pre-1999.
func SSHBannerProbe(ctx context.Context, host string, port int, timeout time.Duration) (bool, error) {
	if host == "" {
		return false, fmt.Errorf("probes: ssh host empty")
	}
	if port == 0 {
		port = 22
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(dl)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}
	r := bufio.NewReader(conn)
	// SSH banner is a CR/LF-terminated line. ReadString reads up to
	// the LF, then we sanity-check the prefix.
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	return matchSSHBanner(line), nil
}

// matchSSHBanner returns true if line starts with the SSH protocol
// banner prefix. Exposed for tests.
func matchSSHBanner(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "SSH-2.0-") || strings.HasPrefix(line, "SSH-1.99-")
}

// validHostOrIP is a permissive guard against argv injection: refuse
// anything that isn't a plausible DNS name, IPv4, or IPv6 literal.
// We're strict on whitespace and shell metacharacters.
func validHostOrIP(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == ':' || r == '%':
			// '%' allowed for IPv6 zone identifiers; ':' for IPv6.
		default:
			return false
		}
	}
	return true
}

// Prober runs the three probes for a single node. Construct one per
// pool worker so the runner / timeouts are configurable per
// deployment. The default is suitable for in-cluster use where every
// probe should finish in under 3 s.
type Prober struct {
	Runner       CommandRunner
	PingTimeout  time.Duration
	SSHTimeout   time.Duration
	IPMITimeout  time.Duration
	SkipPing     bool // for environments where ICMP is firewalled (CI)
	SkipSSH      bool
	SkipIPMI     bool
}

// NewProber returns a Prober with sensible defaults. Pass nil runner
// to get the production exec-based one.
func NewProber(runner CommandRunner) *Prober {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Prober{
		Runner:      runner,
		PingTimeout: 3 * time.Second,
		SSHTimeout:  2 * time.Second,
		IPMITimeout: 5 * time.Second,
	}
}

// RunAllOnce runs every probe applicable to t and returns a
// ProbeResult. It is safe to call concurrently from many goroutines.
//
// Behaviour:
//   - HostIP empty → Ping=false, SSH=false (skipped, never attempted).
//   - BMCAddr empty → IPMIMC=false (skipped).
//   - Each probe is independent; one failure never short-circuits the
//     other two. CheckedAt is set to the wall-clock time when the
//     function returns.
func (p *Prober) RunAllOnce(ctx context.Context, t ProbeTargets) ProbeResult {
	res := ProbeResult{}

	if t.HostIP != "" && !p.SkipPing {
		res.Ping = p.runPing(ctx, t.HostIP)
	}
	if t.HostIP != "" && !p.SkipSSH {
		res.SSH = p.runSSH(ctx, t.HostIP, t.HostPort)
	}
	if t.BMCAddr != "" && !p.SkipIPMI {
		res.IPMIMC = p.runIPMIMC(ctx, t.BMCAddr, t.BMCUser, t.BMCPass)
	}
	res.CheckedAt = time.Now().UTC()
	return res
}

func (p *Prober) runPing(ctx context.Context, host string) bool {
	argv, err := PingArgv(host)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, p.PingTimeout)
	defer cancel()
	_, err = p.Runner.Run(cctx, argv...)
	return err == nil
}

func (p *Prober) runSSH(ctx context.Context, host string, port int) bool {
	cctx, cancel := context.WithTimeout(ctx, p.SSHTimeout)
	defer cancel()
	ok, _ := SSHBannerProbe(cctx, host, port, p.SSHTimeout)
	return ok
}

func (p *Prober) runIPMIMC(ctx context.Context, addr, user, pass string) bool {
	argv, err := IPMIMCArgv(addr, user, pass)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, p.IPMITimeout)
	defer cancel()
	_, err = p.Runner.Run(cctx, argv...)
	return err == nil
}
