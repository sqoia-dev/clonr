// internal/privhelper/ipmi.go — Sprint 34 IPMI-MIN client surface.
//
// Typed wrappers over the clustr-privhelper ipmi-power / ipmi-sel /
// ipmi-sensors / ipmi-lan-* verbs. BMC credentials are passed via stdin as a
// one-line JSON envelope so the password never lands on /proc/<pid>/cmdline.
package privhelper

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// IPMICredentials carries the BMC connection credentials passed via stdin to
// the ipmi-* verbs so the password never lands on /proc/<pid>/cmdline. Empty
// Host means in-band/local KCS operation.
type IPMICredentials struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// IPMIPower runs `ipmi-power <action>` via the privhelper. action must be
// one of "status", "on", "off", "cycle", "reset". creds carry the BMC
// details on stdin. Returns (stdout, err); stdout is the trimmed freeipmi
// output (e.g. "10.0.0.5: on").
func IPMIPower(ctx context.Context, creds IPMICredentials, action string) (string, error) {
	return runIPMIVerb(ctx, "ipmi-power", creds, action)
}

// IPMISEL runs `ipmi-sel <op>` via the privhelper. op is "list" or "clear".
// Returns (stdout, err); stdout is the comma-separated SEL output for list,
// empty for clear.
func IPMISEL(ctx context.Context, creds IPMICredentials, op string) (string, error) {
	return runIPMIVerb(ctx, "ipmi-sel", creds, op)
}

// IPMISensors runs `ipmi-sensors` via the privhelper. Returns the
// comma-separated sensor output.
func IPMISensors(ctx context.Context, creds IPMICredentials) (string, error) {
	return runIPMIVerb(ctx, "ipmi-sensors", creds)
}

// IPMILANGet reads the BMC LAN config for `channel` via the privhelper and
// returns the raw `ipmitool lan print <channel>` output for the caller to
// parse.
func IPMILANGet(ctx context.Context, channel string) (string, error) {
	cmd := exec.CommandContext(ctx, helperPath, "ipmi-lan-get", channel) //#nosec G204 -- channel validated by helper (1..16)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("privhelper: ipmi-lan-get %s: %w", channel, err)
	}
	return string(out), nil
}

// IPMILANSet writes a single BMC LAN config field via the privhelper.
//
// field must be one of: "ipsrc", "ipaddr", "netmask", "defgw", "username",
// "enable-user". For "password", use IPMILANSetPassword instead so the
// secret stays off argv.
func IPMILANSet(ctx context.Context, channel, field, value string) error {
	if field == "password" {
		return fmt.Errorf("privhelper: use IPMILANSetPassword for the password field")
	}
	cmd := exec.CommandContext(ctx, helperPath, "ipmi-lan-set", channel, field, value) //#nosec G204 -- helper validates field/value
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: ipmi-lan-set %s/%s: %w\noutput: %s",
			channel, field, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// IPMILANSetPassword writes a BMC user password via the privhelper. The
// channel argument is the IPMI channel number; userSlot is the user-table
// slot ID (e.g. "2"); password is supplied via stdin so it never appears on
// argv or in /proc/<pid>/cmdline.
func IPMILANSetPassword(ctx context.Context, channel, userSlot, password string) error {
	cmd := exec.CommandContext(ctx, helperPath, "ipmi-lan-set", channel, "password", userSlot) //#nosec G204 -- helper validates fields; password supplied via stdin
	cmd.Stdin = strings.NewReader(password + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: ipmi-lan-set password slot=%s: %w\noutput: %s",
			userSlot, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// runIPMIVerb dispatches one of the bounded ipmi verbs, passing credentials
// over stdin as a one-line JSON envelope so the BMC password stays off argv.
// Returns the helper's stdout (the verb's freeipmi output).
func runIPMIVerb(ctx context.Context, verb string, creds IPMICredentials, args ...string) (string, error) {
	helperArgs := append([]string{verb}, args...)
	cmd := exec.CommandContext(ctx, helperPath, helperArgs...) //#nosec G204 -- verb is hardcoded by caller; args validated by helper
	credsJSON, err := encodeIPMICreds(creds)
	if err != nil {
		return "", fmt.Errorf("privhelper: %s: encode creds: %w", verb, err)
	}
	cmd.Stdin = strings.NewReader(credsJSON)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("privhelper: %s: %w: %s", verb, err, stderr)
	}
	return string(out), nil
}

// encodeIPMICreds emits a one-line JSON envelope terminated by '\n'. The
// helper consumes everything until the first newline as the credentials, so
// inline newlines must not appear in any field — the validator below rejects
// those before encoding.
func encodeIPMICreds(creds IPMICredentials) (string, error) {
	if strings.ContainsAny(creds.Host+creds.Username, "\n\r") {
		return "", fmt.Errorf("host/username may not contain newlines")
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
