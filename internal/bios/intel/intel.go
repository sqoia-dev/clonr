// Package intel implements the bios.Provider interface for Intel servers using
// the Intel SYSCFG utility (syscfg).
//
// # Operator supply chain
//
// Intel SYSCFG is distributed under the Intel EULA For Developer Tools, which
// prohibits redistribution as part of a third-party product.  clustr does NOT
// bundle the binary.  The operator must:
//
//  1. Download the SYSCFG zip from Intel's site (free, requires Intel account).
//     https://www.intel.com/content/www/us/en/download/19779/save-and-restore-system-configuration-utility-syscfg.html
//  2. Drop the `syscfg` binary at /var/lib/clustr/vendor-bios/intel/syscfg on
//     the clustr server.
//  3. Ensure the binary is executable (chmod 755).
//
// See docs/BIOS-INTEL-SETUP.md for the full operator guide.
//
// # Binary invocations
//
//   - ReadCurrent:       syscfg /s -     → stdout, JSON-like key=value output
//   - SupportedSettings: syscfg /d       → stdout, one setting per line
//   - Apply:             syscfg /r <path> → reads profile file, applies settings
//
// # Initramfs vs. post-boot
//
// Inside initramfs (runAutoDeployMode) the binary is exec'd directly — no
// privhelper, since initramfs is already root.
// Post-boot (clientd drift check path): ReadCurrent exec'd directly (clientd
// runs as root on the node).  Apply post-boot routes through privhelper verb
// bios-apply; that path is wired in commit 2.
package intel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sqoia-dev/clustr/internal/bios"
)

// binaryPath is the operator-supplied location for the Intel SYSCFG binary.
const binaryPath = "/var/lib/clustr/vendor-bios/intel/syscfg"

// intelProvider implements bios.Provider for Intel SYSCFG.
type intelProvider struct {
	// binPath allows test injection of a fake binary path.
	binPath string
}

func init() {
	bios.Register(&intelProvider{binPath: binaryPath})
}

// Vendor returns "intel".
func (p *intelProvider) Vendor() string { return "intel" }

// binaryAvailable returns true when the operator-supplied binary exists and is
// executable.  This is the gate for ErrBinaryMissing.
func (p *intelProvider) binaryAvailable() bool {
	info, err := os.Stat(p.binPath)
	if err != nil {
		return false
	}
	// Must be a regular file (not a symlink to nowhere, not a directory).
	return info.Mode().IsRegular() && (info.Mode().Perm()&0o111 != 0)
}

// ReadCurrent executes `syscfg /s -` and parses the key=value output.
// Returns bios.ErrBinaryMissing when the binary has not been placed at binPath.
//
// SYSCFG /s output format (per Intel docs):
//
//	Setting Name=Current Value
//
// Lines that do not contain '=' are skipped (headers, blank lines, etc.).
func (p *intelProvider) ReadCurrent(ctx context.Context) ([]bios.Setting, error) {
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s (see docs/BIOS-INTEL-SETUP.md)", bios.ErrBinaryMissing, p.binPath)
	}

	cmd := exec.CommandContext(ctx, p.binPath, "/s", "-") //#nosec G204 -- binPath is a fixed operator-supplied path, no user input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("intel bios: syscfg /s failed (exit %v): %s", err, strings.TrimSpace(stderr.String()))
	}

	return parseSyscfgSettings(stdout.String()), nil
}

// parseSyscfgSettings parses SYSCFG's "Name=Value" output into Settings.
// Lines without '=' are silently skipped.
func parseSyscfgSettings(output string) []bios.Setting {
	var settings []bios.Setting
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		idx := strings.Index(line, "=")
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if name == "" {
			continue
		}
		settings = append(settings, bios.Setting{Name: name, Value: value})
	}
	return settings
}

// Diff delegates to the shared bios.Diff helper with case-insensitive matching.
func (p *intelProvider) Diff(desired, current []bios.Setting) ([]bios.Change, error) {
	return bios.Diff(desired, current)
}

// Apply writes the change set to the BIOS using `syscfg /r <staging-file>`.
//
// The staging file is written to /var/lib/clustr/bios-staging/ before the
// syscfg invocation.  SYSCFG expects an INI-style key=value file.
//
// Returns bios.ErrBinaryMissing when the binary has not been placed at binPath.
// Returns the applied changes on success (all input changes are considered
// applied when syscfg exits 0; partial failure is signalled by syscfg exit
// codes, propagated as an error).
func (p *intelProvider) Apply(ctx context.Context, changes []bios.Change) ([]bios.Change, error) {
	// Short-circuit: nil/empty changes require no binary invocation.
	// Check this before binaryAvailable so callers probing with no-op payloads
	// don't get ErrBinaryMissing on systems that haven't installed the binary yet.
	if len(changes) == 0 {
		return nil, nil
	}
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s (see docs/BIOS-INTEL-SETUP.md)", bios.ErrBinaryMissing, p.binPath)
	}

	// Write staging file.
	stagingDir := "/var/lib/clustr/bios-staging"
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("intel bios: create staging dir: %w", err)
	}

	f, err := os.CreateTemp(stagingDir, "bios-profile-*.cfg")
	if err != nil {
		return nil, fmt.Errorf("intel bios: create staging file: %w", err)
	}
	stagingPath := f.Name()
	defer os.Remove(stagingPath) // clean up after apply

	for _, c := range changes {
		if _, werr := fmt.Fprintf(f, "%s=%s\n", c.Name, c.To); werr != nil {
			f.Close()
			return nil, fmt.Errorf("intel bios: write staging file: %w", werr)
		}
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("intel bios: close staging file: %w", err)
	}

	cmd := exec.CommandContext(ctx, p.binPath, "/r", stagingPath) //#nosec G204 -- binPath is operator-supplied fixed path; stagingPath is our own temp file
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("intel bios: syscfg /r failed (exit %v): %s\n%s",
			err, strings.TrimSpace(stderr.String()), strings.TrimSpace(stdout.String()))
	}

	// syscfg exited 0 — consider all requested changes applied.
	return changes, nil
}

// SupportedSettings parses `syscfg /d` output and returns the list of
// setting names that the running firmware supports.  Used at profile-create
// time to validate settings_json keys.
//
// Returns an empty slice when the binary is absent (validation is then skipped
// with a warning at the API layer; failures surface at deploy time).
func (p *intelProvider) SupportedSettings(ctx context.Context) ([]string, error) {
	if !p.binaryAvailable() {
		// Non-fatal: caller should warn and proceed.
		return nil, nil
	}

	cmd := exec.CommandContext(ctx, p.binPath, "/d") //#nosec G204 -- binPath is fixed operator-supplied path
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("intel bios: syscfg /d failed (exit %v): %s", err, strings.TrimSpace(stderr.String()))
	}

	var names []string
	scanner := bufio.NewScanner(strings.NewReader(stdout.String()))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// /d output has lines like "Setting Name" or "Setting Name (current: x)"
		// Strip the parenthetical and use the leading text as the setting name.
		if line == "" {
			continue
		}
		if idx := strings.Index(line, " ("); idx != -1 {
			line = line[:idx]
		}
		names = append(names, line)
	}
	return names, nil
}

// NewWithBinaryPath constructs an intelProvider with a custom binary path.
// Used in tests to inject a fake-syscfg binary.
func NewWithBinaryPath(path string) bios.Provider {
	return &intelProvider{binPath: path}
}
