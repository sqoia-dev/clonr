// cmd/clustr-privhelper/ipmi.go — Sprint 34 Bundle B (IPMI-MIN, BMC-IN-DEPLOY,
// SERIAL-CONSOLE) privilege boundary.
//
// The setuid helper is the single point at which clustr-serverd reaches
// freeipmi/ipmitool with elevated privilege. Two operational shapes are
// supported:
//
//  1. Bounded one-shots — power, sel, sensors. Caller passes credentials via
//     stdin (a small JSON envelope, NOT argv) so the BMC password never
//     appears in /proc/<pid>/cmdline. Helper exec's ipmi-power/ipmi-sel/
//     ipmi-sensors with the validated argv and prints stdout to caller.
//
//  2. Long-lived bridge — sol-activate. Helper exec's ipmitool sol activate
//     with stdin/stdout wired through to the calling clustr-serverd PTY
//     pipe. The serverd handler is the websocket bridge; this helper is
//     only the suid hop to root for the kcs/lan+ access.
//
// Per the standing memory rule: caller passes only identifiers (action enum)
// and credentials via stdin. Helper validates each identifier against a
// static allowlist; argv is rebuilt internally.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ipmiCredentials is the JSON envelope the caller writes to stdin so the BMC
// password never appears in /proc/<pid>/cmdline. host may be empty for
// in-band/local KCS operations.
type ipmiCredentials struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// readIPMICreds reads a single-line JSON envelope from stdin and returns the
// parsed credentials. Used by every ipmi-* verb to keep the password off
// argv. Empty stdin yields zero-value credentials (in-band operation).
func readIPMICreds() (ipmiCredentials, error) {
	var creds ipmiCredentials
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return creds, fmt.Errorf("read stdin: %w", err)
	}
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return creds, nil
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return creds, fmt.Errorf("parse credentials envelope: %w", err)
	}
	return creds, nil
}

// allowedIPMIPowerActions is the static allowlist of power verbs.
var allowedIPMIPowerActions = map[string]string{
	"status": "--stat",
	"on":     "--on",
	"off":    "--off",
	"cycle":  "--cycle",
	"reset":  "--reset",
}

// allowedIPMISELOps is the static allowlist of SEL verbs.
var allowedIPMISELOps = map[string]string{
	"list":  "--list",
	"clear": "--clear",
}

// isSafeBMCField returns true when v contains only characters safe to pass
// to freeipmi as a host/username argument. ASCII-only, no shell
// metacharacters. Intentionally restrictive — operator-supplied BMC
// IPs/usernames in clustr's data model are simple identifiers.
func isSafeBMCField(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 256 {
		return false
	}
	for _, c := range v {
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '_' || c == ':' || c == '@'
		if !ok {
			return false
		}
	}
	return true
}

// commonIPMIArgs builds the per-binary common args from validated
// credentials. Identical layout to internal/ipmi/freeipmi.go commonArgs() so
// test parity with the server-side wrapper is straightforward to reason
// about.
func commonIPMIArgs(creds ipmiCredentials) []string {
	if creds.Host == "" {
		return nil
	}
	args := []string{"-h", creds.Host}
	if creds.Username != "" {
		args = append(args, "-u", creds.Username)
	}
	if creds.Password != "" {
		args = append(args, "-p", creds.Password)
	}
	args = append(args, "--driver-type=LAN_2_0")
	return args
}

// validateCreds rejects credentials with shell-unsafe characters in the host
// or username before any exec. The password is passed verbatim — freeipmi
// accepts arbitrary bytes there — but neither host nor username should ever
// contain anything but the restricted character set.
func validateCreds(creds ipmiCredentials) error {
	if !isSafeBMCField(creds.Host) {
		return fmt.Errorf("host contains disallowed characters")
	}
	if !isSafeBMCField(creds.Username) {
		return fmt.Errorf("username contains disallowed characters")
	}
	return nil
}

// verbIPMIPower handles the ipmi-power verb. Argv: ipmi-power <action>.
// The action is validated against allowedIPMIPowerActions; credentials come
// from stdin so the password stays off /proc/<pid>/cmdline.
func verbIPMIPower(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "ipmi-power requires exactly one argument: <action>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-power", args, 1, msg)
		return 1
	}
	action := args[0]
	flag, ok := allowedIPMIPowerActions[action]
	if !ok {
		msg := fmt.Sprintf("ipmi-power: action %q not in allowlist {status,on,off,cycle,reset}", action)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-power", args, 1, msg)
		return 1
	}

	creds, err := readIPMICreds()
	if err != nil {
		msg := fmt.Sprintf("ipmi-power: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-power", args, 1, msg)
		return 1
	}
	if err := validateCreds(creds); err != nil {
		msg := fmt.Sprintf("ipmi-power: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-power", args, 1, msg)
		return 1
	}

	argv := append([]string{}, commonIPMIArgs(creds)...)
	argv = append(argv, flag)

	cmd := exec.Command("ipmi-power", argv...) //#nosec G204 -- creds validated, flag from static allowlist
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	writeAudit(callerUID, "ipmi-power", []string{action}, exitCode, "")
	return exitCode
}

// verbIPMISEL handles the ipmi-sel verb. Argv: ipmi-sel <op>.
func verbIPMISEL(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "ipmi-sel requires exactly one argument: <op>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sel", args, 1, msg)
		return 1
	}
	op := args[0]
	if _, ok := allowedIPMISELOps[op]; !ok {
		msg := fmt.Sprintf("ipmi-sel: op %q not in allowlist {list,clear}", op)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sel", args, 1, msg)
		return 1
	}

	creds, err := readIPMICreds()
	if err != nil {
		msg := fmt.Sprintf("ipmi-sel: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sel", args, 1, msg)
		return 1
	}
	if err := validateCreds(creds); err != nil {
		msg := fmt.Sprintf("ipmi-sel: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sel", args, 1, msg)
		return 1
	}

	argv := append([]string{}, commonIPMIArgs(creds)...)
	switch op {
	case "list":
		argv = append(argv, "--no-header-output", "--comma-separated-output", "--output-event-state")
	case "clear":
		argv = append(argv, "--clear")
	}

	cmd := exec.Command("ipmi-sel", argv...) //#nosec G204 -- creds validated, op from static allowlist
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	writeAudit(callerUID, "ipmi-sel", []string{op}, exitCode, "")
	return exitCode
}

// verbIPMISensors handles the ipmi-sensors verb. No positional args.
func verbIPMISensors(callerUID int, args []string) int {
	if len(args) != 0 {
		msg := "ipmi-sensors takes no positional arguments"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sensors", args, 1, msg)
		return 1
	}

	creds, err := readIPMICreds()
	if err != nil {
		msg := fmt.Sprintf("ipmi-sensors: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sensors", args, 1, msg)
		return 1
	}
	if err := validateCreds(creds); err != nil {
		msg := fmt.Sprintf("ipmi-sensors: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sensors", args, 1, msg)
		return 1
	}

	argv := append([]string{}, commonIPMIArgs(creds)...)
	argv = append(argv, "--no-header-output", "--comma-separated-output", "--output-sensor-state")

	cmd := exec.Command("ipmi-sensors", argv...) //#nosec G204 -- creds validated; flags are fixed literals
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	writeAudit(callerUID, "ipmi-sensors", nil, exitCode, "")
	return exitCode
}

// verbIPMILANSet handles the ipmi-lan-set verb used by BMC-IN-DEPLOY. Argv:
// ipmi-lan-set <channel> <field> <value>. field is one of:
//
//	ipsrc | ipaddr | netmask | defgw | username | password | enable-user
//
// Channel is restricted to 1..16 (IPMI spec channel numbers). The actual BMC
// password (when field=password) is read from stdin so it doesn't appear in
// /proc/<pid>/cmdline.
func verbIPMILANSet(callerUID int, args []string) int {
	if len(args) < 3 {
		msg := "ipmi-lan-set requires three arguments: <channel> <field> <value>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
		return 1
	}
	channel, field, value := args[0], args[1], args[2]

	if !isSafeChannel(channel) {
		msg := fmt.Sprintf("ipmi-lan-set: channel %q must be 1..16", channel)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
		return 1
	}
	if !allowedLANField[field] {
		msg := fmt.Sprintf("ipmi-lan-set: field %q not in allowlist", field)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
		return 1
	}
	if !isSafeBMCField(value) {
		msg := fmt.Sprintf("ipmi-lan-set: value %q contains disallowed characters", value)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
		return 1
	}

	password := ""
	if field == "password" {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			msg := fmt.Sprintf("ipmi-lan-set: read password from stdin: %v", err)
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "ipmi-lan-set", []string{channel, field}, 1, msg)
			return 1
		}
		password = strings.TrimRight(string(raw), "\n")
	}

	var argv []string
	switch field {
	case "ipsrc":
		if value != "static" && value != "dhcp" {
			msg := "ipmi-lan-set: ipsrc value must be 'static' or 'dhcp'"
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
			return 1
		}
		argv = []string{"lan", "set", channel, "ipsrc", value}
	case "ipaddr":
		argv = []string{"lan", "set", channel, "ipaddr", value}
	case "netmask":
		argv = []string{"lan", "set", channel, "netmask", value}
	case "defgw":
		argv = []string{"lan", "set", channel, "defgw", "ipaddr", value}
	case "username":
		uid, name, ok := strings.Cut(value, ":")
		if !ok || !isSafeBMCField(uid) || !isSafeBMCField(name) {
			msg := "ipmi-lan-set: username value must be '<id>:<name>'"
			fmt.Fprintln(os.Stderr, msg)
			writeAudit(callerUID, "ipmi-lan-set", args, 1, msg)
			return 1
		}
		argv = []string{"user", "set", "name", uid, name}
	case "password":
		argv = []string{"user", "set", "password", value, password}
	case "enable-user":
		argv = []string{"user", "enable", value}
	}

	cmd := exec.Command("ipmitool", argv...) //#nosec G204 -- field/value validated above
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	auditArgs := []string{channel, field, value}
	if field == "password" {
		auditArgs = []string{channel, field, "<redacted>"}
	}
	writeAudit(callerUID, "ipmi-lan-set", auditArgs, exitCode, "")
	return exitCode
}

// allowedLANField is the static set of BMC LAN fields ipmi-lan-set may
// write.
var allowedLANField = map[string]bool{
	"ipsrc":       true,
	"ipaddr":      true,
	"netmask":     true,
	"defgw":       true,
	"username":    true,
	"password":    true,
	"enable-user": true,
}

// isSafeChannel returns true when ch is the decimal string for an integer
// 1..16 (IPMI valid channel range).
func isSafeChannel(ch string) bool {
	if ch == "" || len(ch) > 2 {
		return false
	}
	for _, c := range ch {
		if c < '0' || c > '9' {
			return false
		}
	}
	n := 0
	for _, c := range ch {
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 16
}

// verbIPMILANGet runs `ipmitool lan print <channel>` so the caller can read
// the current BMC config back. No credentials needed (in-band).
func verbIPMILANGet(callerUID int, args []string) int {
	if len(args) != 1 {
		msg := "ipmi-lan-get requires one argument: <channel>"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-get", args, 1, msg)
		return 1
	}
	channel := args[0]
	if !isSafeChannel(channel) {
		msg := fmt.Sprintf("ipmi-lan-get: channel %q must be 1..16", channel)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-lan-get", args, 1, msg)
		return 1
	}
	cmd := exec.Command("ipmitool", "lan", "print", channel) //#nosec G204 -- channel validated
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	writeAudit(callerUID, "ipmi-lan-get", args, exitCode, "")
	return exitCode
}

// verbIPMISOLActivate spawns `ipmitool sol activate` with stdin/stdout
// inherited from the caller. clustr-serverd's WS bridge wires the helper's
// stdin/stdout to a websocket — the helper is just the suid hop.
//
// Credentials come from stdin via a one-line JSON envelope, but with a twist:
// the FIRST line of stdin is the JSON envelope (terminated by '\n'), and
// EVERYTHING AFTER is the raw user-typed bytes the BMC's SOL channel should
// receive. The helper reads the first line, builds the password file, and
// then dup2()s the remaining stdin into ipmitool's stdin.
func verbIPMISOLActivate(callerUID int, args []string) int {
	if len(args) != 0 {
		msg := "ipmi-sol-activate takes no positional arguments"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}

	br := newLineReader(os.Stdin)
	credsLine, err := br.ReadLine()
	if err != nil {
		msg := fmt.Sprintf("ipmi-sol-activate: read credentials: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}
	var creds ipmiCredentials
	if err := json.Unmarshal(credsLine, &creds); err != nil {
		msg := fmt.Sprintf("ipmi-sol-activate: parse credentials: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}
	if err := validateCreds(creds); err != nil {
		msg := fmt.Sprintf("ipmi-sol-activate: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}
	if creds.Host == "" {
		msg := "ipmi-sol-activate: remote host is required"
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}

	passFile, err := os.CreateTemp("", "ipmi-sol-pass-*")
	if err != nil {
		msg := fmt.Sprintf("ipmi-sol-activate: create temp password file: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}
	passPath := passFile.Name()
	defer os.Remove(passPath)
	_ = passFile.Chmod(0600)
	if _, err := passFile.WriteString(creds.Password); err != nil {
		_ = passFile.Close()
		msg := fmt.Sprintf("ipmi-sol-activate: write password file: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}
	if err := passFile.Close(); err != nil {
		msg := fmt.Sprintf("ipmi-sol-activate: close password file: %v", err)
		fmt.Fprintln(os.Stderr, msg)
		writeAudit(callerUID, "ipmi-sol-activate", args, 1, msg)
		return 1
	}

	argv := []string{
		"-I", "lanplus",
		"-H", creds.Host,
		"-U", creds.Username,
		"-f", passPath,
		"sol", "activate",
	}

	cmd := exec.Command("ipmitool", argv...) //#nosec G204 -- creds validated; flags are fixed literals
	cmd.Stdin = br
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 2
		}
	}
	writeAudit(callerUID, "ipmi-sol-activate", []string{creds.Host}, exitCode, "")
	return exitCode
}

// lineReader wraps an io.Reader so we can read a single newline-delimited
// line (the credentials envelope) and then expose the rest of the stream as
// an io.Reader for the SOL stdin pipe. This is the cheapest way to multiplex
// credentials + raw bytes through one stdin without a richer protocol.
type lineReader struct {
	src     io.Reader
	residue []byte
}

func newLineReader(src io.Reader) *lineReader { return &lineReader{src: src} }

// ReadLine reads bytes up to and including the first '\n', stores any
// over-read past the newline in r.residue, and returns the line without the
// newline.
func (r *lineReader) ReadLine() ([]byte, error) {
	buf := make([]byte, 1)
	var line []byte
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return line, nil
			}
			line = append(line, buf[0])
		}
		if err != nil {
			if len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
	}
}

// Read drains residue first, then proxies to the underlying source.
func (r *lineReader) Read(p []byte) (int, error) {
	if len(r.residue) > 0 {
		n := copy(p, r.residue)
		r.residue = r.residue[n:]
		return n, nil
	}
	return r.src.Read(p)
}
