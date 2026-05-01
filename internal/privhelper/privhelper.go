// internal/privhelper/privhelper.go — server-side client for clustr-privhelper.
//
// clustr-serverd runs as the unprivileged "clustr" OS user. Host-side
// operations that require root privilege are dispatched through the setuid
// binary at /usr/sbin/clustr-privhelper. This package provides a typed Go API
// so callers never construct raw argv strings themselves.
//
// All public functions in this package are safe to call from multiple goroutines.
package privhelper

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// helperPath is the canonical install path for the setuid binary.
const helperPath = "/usr/sbin/clustr-privhelper"

// DnfInstall installs each package in pkgs via the clustr-privhelper
// dnf-install verb. Each package is installed in a separate helper invocation
// so that a failure on one package produces a clear per-package error.
//
// The helper validates each package name against the embedded deps_matrix
// allowlist before invoking dnf. Packages not in the allowlist are rejected
// with a structured error — no shell exec occurs for rejected names.
func DnfInstall(ctx context.Context, pkgs []string) error {
	for _, pkg := range pkgs {
		if err := dnfInstallOne(ctx, pkg); err != nil {
			return fmt.Errorf("privhelper: dnf-install %s: %w", pkg, err)
		}
	}
	return nil
}

// dnfInstallOne runs a single dnf-install invocation for pkg.
func dnfInstallOne(ctx context.Context, pkg string) error {
	cmd := exec.CommandContext(ctx, helperPath, "dnf-install", pkg) //#nosec G204 -- pkg is a plain name, no shell injection possible; helper validates against allowlist
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Surface the helper's stderr in the error so callers can surface the
		// structured rejection message or dnf's output to the build log.
		return fmt.Errorf("%w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// ServiceControl runs `systemctl <action> <unit>` via the clustr-privhelper
// service-control verb. Both unit and action are validated by the helper
// against static allowlists before any exec occurs — callers must not
// construct these values from user input without their own prior validation.
//
// Returns a wrapped error that includes the helper's stderr output on failure.
// If the helper rejects the unit with a structured "unit_not_allowed" message,
// that text is preserved in the error string for caller inspection.
func ServiceControl(ctx context.Context, unit, action string) error {
	cmd := exec.CommandContext(ctx, helperPath, "service-control", unit, action) //#nosec G204 -- unit and action are plain identifiers; helper validates against allowlists
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: service-control %s %s: %w\noutput: %s",
			action, unit, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// CapBitTest invokes the cap-bit-test verb and returns the reported effective
// UID. Returns (0, nil) when the setuid bit is set correctly; returns (n, nil)
// where n is the server process UID if the bit is missing.
func CapBitTest(ctx context.Context) (int, error) {
	cmd := exec.CommandContext(ctx, helperPath, "cap-bit-test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("privhelper: cap-bit-test: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}

	// Parse "clustr-privhelper cap-bit-test: euid=<n>"
	line := strings.TrimSpace(string(out))
	var euid int
	if _, scanErr := fmt.Sscanf(line, "clustr-privhelper cap-bit-test: euid=%d", &euid); scanErr != nil {
		return -1, fmt.Errorf("privhelper: cap-bit-test: unexpected output %q: %w", line, scanErr)
	}
	return euid, nil
}
