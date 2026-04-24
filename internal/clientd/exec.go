package clientd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// limitWriter wraps an io.Writer and silently drops bytes once the cap is reached.
// It sets capped=true when it first discards bytes, so callers can detect truncation.
type limitWriter struct {
	w         *bytes.Buffer
	remaining int
	capped    bool
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		lw.capped = true
		// Pretend we wrote everything so the command does not fail with EPIPE.
		return len(p), nil
	}
	if len(p) > lw.remaining {
		lw.capped = true
		p = p[:lw.remaining]
	}
	n, err := lw.w.Write(p)
	lw.remaining -= n
	return len(p), err // always report full write to avoid EPIPE
}

const (
	execOutputCap     = 64 * 1024 // 64 KB per stream
	execTimeoutSec    = 30 * time.Second
)

// whitelistEntry defines a command and the set of argument prefixes/values
// that are allowed. An empty allowedArgs means the command takes no arguments
// at all (e.g. "hostname", "whoami").
type whitelistEntry struct {
	cmd         string
	allowedArgs []string
}

// execWhitelist defines every command that clonr-clientd will execute on behalf
// of an admin. Commands are matched by exact name; arguments are validated
// against the per-command allowed list. No shell is ever invoked.
var execWhitelist = []whitelistEntry{
	{cmd: "journalctl", allowedArgs: []string{"--unit=", "--lines=", "--no-pager", "--since=", "--until=", "-o", "json"}},
	{cmd: "systemctl", allowedArgs: []string{"status", "is-active", "is-enabled", "list-units"}},
	{cmd: "df", allowedArgs: []string{"-h", "-H", "--output="}},
	{cmd: "free", allowedArgs: []string{"-m", "-g", "-h"}},
	{cmd: "uptime"},
	{cmd: "ip", allowedArgs: []string{"addr", "route", "link", "show", "-s", "-4", "-6"}},
	{cmd: "cat", allowedArgs: []string{"/etc/os-release", "/etc/hostname", "/etc/slurm/slurm.conf", "/etc/sssd/sssd.conf", "/proc/meminfo", "/proc/cpuinfo"}},
	{cmd: "ping", allowedArgs: []string{"-c"}},
	{cmd: "sinfo", allowedArgs: []string{"-N", "-l", "--format=", "-p"}},
	{cmd: "squeue", allowedArgs: []string{"-u", "-p", "-j", "--format=", "-l"}},
	{cmd: "scontrol", allowedArgs: []string{"show", "node", "partition", "job"}},
	{cmd: "hostname"},
	{cmd: "uname", allowedArgs: []string{"-a", "-r", "-n"}},
	{cmd: "whoami"},
	{cmd: "id"},
	{cmd: "ps", allowedArgs: []string{"aux", "-ef", "-eo"}},
	{cmd: "top", allowedArgs: []string{"-bn1"}},
	{cmd: "lscpu"},
	{cmd: "lsblk"},
	{cmd: "mount"},
}

// whitelistedCatPaths is the exhaustive set of paths the cat command may read.
// Validated separately because cat's "allowed args" ARE the file paths, not
// argument flags — any path not in this set must be rejected.
var whitelistedCatPaths = map[string]bool{
	"/etc/os-release":       true,
	"/etc/hostname":         true,
	"/etc/slurm/slurm.conf": true,
	"/etc/sssd/sssd.conf":   true,
	"/proc/meminfo":         true,
	"/proc/cpuinfo":         true,
}

// WhitelistedCommands returns the sorted list of command names in the whitelist.
// Used by the server to populate the UI dropdown.
func WhitelistedCommands() []string {
	out := make([]string, 0, len(execWhitelist))
	for _, e := range execWhitelist {
		out = append(out, e.cmd)
	}
	return out
}

// handleExecRequest validates and executes the requested command.
// It enforces the whitelist on both the command name and every argument.
// Output is capped at execOutputCap bytes per stream.
func handleExecRequest(payload ExecRequestPayload) ExecResultPayload {
	ref := payload.RefMsgID

	entry, ok := findWhitelistEntry(payload.Command)
	if !ok {
		log.Warn().Str("command", payload.Command).Str("ref_msg_id", ref).
			Msg("clientd exec: command not in whitelist — rejected")
		return ExecResultPayload{
			RefMsgID: ref,
			ExitCode: -1,
			Error:    fmt.Sprintf("command %q is not allowed", payload.Command),
		}
	}

	// cat requires every argument to be an exact whitelisted path.
	if payload.Command == "cat" {
		for _, arg := range payload.Args {
			if !whitelistedCatPaths[arg] {
				log.Warn().Str("path", arg).Str("ref_msg_id", ref).
					Msg("clientd exec: cat path not in whitelist — rejected")
				return ExecResultPayload{
					RefMsgID: ref,
					ExitCode: -1,
					Error:    fmt.Sprintf("cat path %q is not allowed", arg),
				}
			}
		}
	} else {
		// For all other commands, validate each argument against the entry's
		// allowed patterns (prefix match OR exact match).
		for _, arg := range payload.Args {
			if !isArgAllowed(arg, entry.allowedArgs) {
				log.Warn().
					Str("command", payload.Command).
					Str("arg", arg).
					Str("ref_msg_id", ref).
					Msg("clientd exec: argument not in allowlist — rejected")
				return ExecResultPayload{
					RefMsgID: ref,
					ExitCode: -1,
					Error:    fmt.Sprintf("argument %q is not allowed for command %q", arg, payload.Command),
				}
			}
		}
	}

	// Reject any shell metacharacters in arguments — belt-and-suspenders guard
	// even though we never pass args through a shell.
	for _, arg := range payload.Args {
		if containsShellMeta(arg) {
			log.Warn().Str("arg", arg).Str("ref_msg_id", ref).
				Msg("clientd exec: shell metacharacter in argument — rejected")
			return ExecResultPayload{
				RefMsgID: ref,
				ExitCode: -1,
				Error:    fmt.Sprintf("argument %q contains disallowed characters", arg),
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), execTimeoutSec)
	defer cancel()

	// NEVER use shell execution. exec.Command with a []string args list is safe.
	cmd := exec.CommandContext(ctx, payload.Command, payload.Args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdoutBuf, remaining: execOutputCap}
	cmd.Stderr = &limitWriter{w: &stderrBuf, remaining: execOutputCap}

	runErr := cmd.Run()

	exitCode := 0
	truncated := false
	errMsg := ""

	if runErr != nil {
		if exitErr, ok2 := runErr.(*exec.ExitError); ok2 {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
			errMsg = "command timed out after 30s"
		} else {
			exitCode = -1
			errMsg = runErr.Error()
		}
	}

	// Detect truncation: if either buffer hit the cap the limitWriter stopped
	// writing, so we check if the capped flag was set on either writer.
	stdoutLW := cmd.Stdout.(*limitWriter)
	stderrLW := cmd.Stderr.(*limitWriter)
	if stdoutLW.capped || stderrLW.capped {
		truncated = true
	}

	log.Info().
		Str("command", payload.Command).
		Strs("args", payload.Args).
		Int("exit_code", exitCode).
		Bool("truncated", truncated).
		Str("ref_msg_id", ref).
		Msg("clientd exec: command completed")

	return ExecResultPayload{
		RefMsgID:  ref,
		ExitCode:  exitCode,
		Stdout:    stdoutBuf.String(),
		Stderr:    stderrBuf.String(),
		Truncated: truncated,
		Error:     errMsg,
	}
}

// findWhitelistEntry looks up the command name in execWhitelist.
func findWhitelistEntry(cmd string) (whitelistEntry, bool) {
	for _, e := range execWhitelist {
		if e.cmd == cmd {
			return e, true
		}
	}
	return whitelistEntry{}, false
}

// isArgAllowed returns true if arg matches any entry in allowed by exact match
// or by prefix match (for entries ending with "=", e.g. "--unit=").
func isArgAllowed(arg string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasSuffix(a, "=") {
			// Prefix match: "--unit=sshd" matches "--unit="
			if strings.HasPrefix(arg, a) {
				return true
			}
		} else {
			if arg == a {
				return true
			}
		}
	}
	return false
}

// containsShellMeta returns true if arg contains characters that have special
// meaning in a shell. We never pass args through a shell, but this provides
// belt-and-suspenders protection against unexpected exec behaviour.
func containsShellMeta(arg string) bool {
	const meta = ";|&`$><!()\\"
	return strings.ContainsAny(arg, meta)
}
