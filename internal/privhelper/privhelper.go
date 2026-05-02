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
	"os"
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

// RepoPush copies a signed RPM from src to dst via the clustr-privhelper
// repo-push verb (requires root to write under /var/lib/clustr/repo/).
// dst must be under /var/lib/clustr/repo/clustr-internal-repo/ and end in .rpm.
func RepoPush(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, helperPath, "repo-push", src, dst) //#nosec G204 -- paths are plain identifiers; helper validates prefix
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: repo-push: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// RepoRefresh runs createrepo_c on the given directory via the clustr-privhelper
// repo-refresh verb. dir must be under /var/lib/clustr/repo/clustr-internal-repo/.
func RepoRefresh(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, helperPath, "repo-refresh", dir) //#nosec G204 -- dir is a plain path; helper validates prefix
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: repo-refresh: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// DnfUpgrade installs/upgrades one or more slurm package specs from
// clustr-internal-repo only via the clustr-privhelper dnf-upgrade verb.
// All specs must start with "slurm" or "munge" — the helper enforces this.
func DnfUpgrade(ctx context.Context, pkgSpecs []string) error {
	if len(pkgSpecs) == 0 {
		return nil
	}
	args := append([]string{"dnf-upgrade"}, pkgSpecs...)
	cmd := exec.CommandContext(ctx, helperPath, args...) //#nosec G204 -- pkg specs are plain identifiers; helper validates each
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: dnf-upgrade: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// CATrustExtract runs `update-ca-trust extract` via the clustr-privhelper
// ca-trust-extract verb. Used after writing a new CA certificate to the system
// trust anchor directory (/etc/pki/ca-trust/source/anchors/) so the change
// is reflected in the consolidated bundle immediately.
func CATrustExtract(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, helperPath, "ca-trust-extract") //#nosec G204 -- no user-supplied arguments; verb is a fixed literal
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: ca-trust-extract: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// BiosRead invokes the bios-read verb to read current BIOS settings from the
// node.  The vendor binary (operator-supplied) is exec'd by the helper and its
// stdout (key=value lines) is returned as a byte slice for the caller to parse.
//
// Used by the post-boot drift check path (clientd → privhelper → vendor binary).
// Inside initramfs, ReadCurrent is called directly without privhelper.
func BiosRead(ctx context.Context, vendor string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, helperPath, "bios-read", vendor) //#nosec G204 -- vendor is a plain identifier; helper validates against allowlist
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("privhelper: bios-read %s: %w\noutput: %s", vendor, err, strings.TrimRight(string(out), "\n"))
	}
	return out, nil
}

// BiosApply invokes the bios-apply verb to apply BIOS settings from a staged
// profile file.  The profilePath must be a regular .json file under
// /var/lib/clustr/bios-staging/ — the caller is responsible for writing the
// profile JSON there before calling this.
//
// This is the post-boot apply path: clientd writes profile JSON, calls
// BiosApply, then cleans up the staging file.  Returns a wrapped error that
// includes the helper's stderr output on failure.
func BiosApply(ctx context.Context, vendor, profilePath string) error {
	cmd := exec.CommandContext(ctx, helperPath, "bios-apply", vendor, profilePath) //#nosec G204 -- vendor and profilePath are plain identifiers; helper validates both
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: bios-apply %s: %w\noutput: %s", vendor, err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// RuleWrite atomically writes content to /etc/clustr/rules.d/{name}.yml via the
// clustr-privhelper rule-write verb. name must match ^[a-zA-Z0-9._-]+$ and
// must not contain path separators. The helper creates the file as root:clustr
// 0640, overwriting any existing file of the same name.
//
// content is passed through a temp file in /tmp so argv stays bounded; the
// helper reads it, validates the destination, and moves it into place.
func RuleWrite(ctx context.Context, name string, content []byte) error {
	// Write content to a temp file so we can pass it as a path argument.
	// The helper reads from this path and the original file is removed after.
	tmp, err := os.CreateTemp("", "clustr-rule-*.yml")
	if err != nil {
		return fmt.Errorf("privhelper: rule-write: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("privhelper: rule-write: write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("privhelper: rule-write: close tmp: %w", err)
	}

	cmd := exec.CommandContext(ctx, helperPath, "rule-write", name, tmpPath) //#nosec G204 -- name validated by helper; tmpPath is a /tmp path we created
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("privhelper: rule-write %s: %w\noutput: %s", name, err, strings.TrimRight(string(out), "\n"))
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
