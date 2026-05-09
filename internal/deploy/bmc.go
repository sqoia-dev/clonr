// Package deploy — bmc.go (Sprint 34 BMC-IN-DEPLOY)
//
// Idempotent BMC IP/user/channel reset that runs in initramfs each deploy.
// Reads the desired BMC config from the node's api.BMCNodeConfig, compares
// against the current state via `ipmitool lan print 1` (KCS in-band), and
// writes only the fields that differ.
//
// Idempotency is the load-bearing property: an operator who deploys the
// same node twice in a row must not see any `ipmitool lan set` invocations
// on the second run. The pure-Go diff layer makes this verifiable in unit
// tests without a real BMC.
//
// This step lives next to the existing applyBMCConfig() (which configures
// via the in-tree internal/ipmi.Client wrapper). The split is intentional:
//
//   - applyBMCConfig (existing) — used pre-Sprint-34 by deploy paths that
//     have a fully populated api.BMCNodeConfig and want to write all fields.
//   - ApplyBMCConfigToHardware (this file) — Sprint 34 idempotent variant
//     that reads first, diffs, then writes only the differences.
//
// Both paths converge on the same ipmitool argv shapes; the new path adds
// the "read first" half. The runner abstraction keeps the unit test
// deterministic without touching the real binary.
package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// bmcLANChannel is the channel number used for the LAN settings. Channel 1
// is the universal default for BMC LAN; we do not currently expose a
// per-node override because every BMC clustr targets uses ch=1.
const bmcLANChannel = 1

// bmcRunner is the exec abstraction for ipmitool. The production runner
// shells out via os/exec; tests inject a mock that records argv and returns
// canned output without touching the real binary.
//
// Convention:
//
//   - argv passed to Run is the FULL ipmitool subcommand, e.g.
//     ["lan","print","1"] or ["lan","set","1","ipaddr","10.0.0.5"]. The
//     runner prepends "ipmitool" before exec.
//   - Run returns (stdout, nil) on success; ("", err) on any failure.
type bmcRunner func(ctx context.Context, args ...string) (string, error)

// defaultIpmitoolRunner shells out to ipmitool directly (KCS / in-band, no
// host flag — this code runs in the initramfs against the local BMC).
//
// We do not route through clustr-privhelper here because BMC-IN-DEPLOY runs
// AS root (the initramfs has no unprivileged user), so the privilege
// boundary the privhelper provides is moot. The post-boot live-IPMI paths
// in clustr-serverd DO route through privhelper — see internal/privhelper.
func defaultIpmitoolRunner(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("bmc: empty argv")
	}
	switch args[0] {
	case "lan", "user":
		out, err := exec.CommandContext(ctx, "ipmitool", args...).Output() //#nosec G204 -- args are built internally from validated config; first element is in {lan,user}
		if err != nil {
			var stderr string
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = strings.TrimSpace(string(ee.Stderr))
			}
			if stderr != "" {
				return "", fmt.Errorf("ipmitool %s: %w: %s", strings.Join(args, " "), err, stderr)
			}
			return "", fmt.Errorf("ipmitool %s: %w", strings.Join(args, " "), err)
		}
		return string(out), nil
	}
	return "", fmt.Errorf("bmc: unsupported ipmitool subcommand %q", args[0])
}

// ApplyBMCConfigToHardware applies cfg to the local BMC idempotently:
//  1. Read current `lan print 1` output.
//  2. Parse the existing IP / netmask / gateway / source.
//  3. For each desired field, write only if it differs. No write =
//     ipmitool is never invoked for unchanged fields — verifiable in tests.
//
// Returns nil on success or when there is nothing to do (idempotent no-op).
// Best-effort by callers — the deploy chain wraps a failure in a non-fatal
// log per the existing applyBMCConfig contract.
func ApplyBMCConfigToHardware(ctx context.Context, cfg *api.BMCNodeConfig) error {
	return applyBMCConfigWithRunner(ctx, cfg, defaultIpmitoolRunner)
}

// applyBMCConfigWithRunner is the runner-injected variant for unit tests.
// Production callers go through ApplyBMCConfigToHardware.
func applyBMCConfigWithRunner(ctx context.Context, cfg *api.BMCNodeConfig, run bmcRunner) error {
	if cfg == nil || cfg.IPAddress == "" {
		return nil
	}

	currentOut, err := run(ctx, "lan", "print", fmt.Sprintf("%d", bmcLANChannel))
	if err != nil {
		return fmt.Errorf("bmc: read current lan config: %w", err)
	}
	current := parseLanPrint(currentOut)

	steps := planBMCDiff(current, cfg)

	for _, s := range steps {
		if _, err := run(ctx, s...); err != nil {
			return fmt.Errorf("bmc: %s: %w", strings.Join(s, " "), err)
		}
	}

	return nil
}

// bmcCurrentState is the parsed view of `ipmitool lan print N`.
type bmcCurrentState struct {
	IPAddress string
	Netmask   string
	Gateway   string
	IPSource  string // "static" | "dhcp" | "" if unknown
}

// parseLanPrint extracts the four fields we care about from `ipmitool lan
// print` output. Whitespace and label variants are tolerated.
func parseLanPrint(out string) bmcCurrentState {
	var s bmcCurrentState
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		switch {
		case k == "IP Address":
			s.IPAddress = v
		case strings.Contains(k, "Subnet Mask"):
			s.Netmask = v
		case strings.Contains(k, "Default Gateway IP"):
			s.Gateway = v
		case strings.Contains(k, "IP Address Source"):
			lv := strings.ToLower(v)
			switch {
			case strings.Contains(lv, "static"):
				s.IPSource = "static"
			case strings.Contains(lv, "dhcp"):
				s.IPSource = "dhcp"
			}
		}
	}
	return s
}

// planBMCDiff returns the ordered list of `ipmitool lan set` argvs needed to
// converge the BMC from `current` to `desired`. Returns an empty slice when
// nothing differs (idempotent no-op).
//
// IP source change is emitted FIRST because some BMCs reject the per-field
// writes that follow when source != static.
func planBMCDiff(current bmcCurrentState, desired *api.BMCNodeConfig) [][]string {
	var steps [][]string
	ch := fmt.Sprintf("%d", bmcLANChannel)

	if current.IPSource != "static" {
		steps = append(steps, []string{"lan", "set", ch, "ipsrc", "static"})
	}
	if !sameIP(current.IPAddress, desired.IPAddress) {
		steps = append(steps, []string{"lan", "set", ch, "ipaddr", desired.IPAddress})
	}
	if desired.Netmask != "" && current.Netmask != desired.Netmask {
		steps = append(steps, []string{"lan", "set", ch, "netmask", desired.Netmask})
	}
	if desired.Gateway != "" && !sameIP(current.Gateway, desired.Gateway) {
		steps = append(steps, []string{"lan", "set", ch, "defgw", "ipaddr", desired.Gateway})
	}
	return steps
}

// sameIP returns true when two IP-shaped strings represent the same
// address. Trims whitespace; tolerates the "0.0.0.0" placeholder some BMCs
// return for "unset".
func sameIP(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	return a == b
}
