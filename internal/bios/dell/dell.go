// Package dell implements the bios.Provider interface for Dell servers using
// the Dell RACADM utility (racadm).
//
// # Operator supply chain
//
// Dell's racadm is distributed under the Dell EULA, which prohibits
// redistribution as part of a third-party product.  clustr does NOT bundle the
// binary.  The operator must:
//
//  1. Download iDRAC Tools for Linux from Dell's support site.
//     https://www.dell.com/support/home/ → search "iDRAC Tools for Linux"
//  2. Extract the archive and locate the `racadm` binary.
//  3. Drop the binary at /var/lib/clustr/vendor-bios/dell/racadm on the clustr
//     server node.
//  4. Ensure the binary is executable (chmod 0755).
//
// # Binary invocations
//
//   - ReadCurrent:  racadm get BIOS.SetupConfig  → stdout, key=value per line
//   - Apply:        racadm set <Key> <Value> (one invocation per change),
//                   then racadm jobqueue create BIOS.Setup.1-1 to schedule
//                   the staged changes for apply on next POST.
//
// # Output format
//
// racadm get BIOS.SetupConfig emits lines of the form:
//
//	[Key=Value]
//	Attribute0=Value0
//	Attribute1=Value1
//
// Section headers enclosed in brackets (e.g. "[BIOS]") are skipped.
// Lines without '=' are skipped.
//
// # Privhelper path
//
// For the post-boot drift-check path, Apply writes a staging JSON file under
// /var/lib/clustr/bios-staging/ and invokes privhelper bios-apply dell.
// The privhelper builds the racadm set + jobqueue create argv internally.
// Inside initramfs, Apply exec's racadm directly (already root, no helper).
package dell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sqoia-dev/clustr/internal/bios"
)

// binaryPath is the operator-supplied location for the Dell racadm binary.
const binaryPath = "/var/lib/clustr/vendor-bios/dell/racadm"

// dellProvider implements bios.Provider for Dell via racadm.
type dellProvider struct {
	// binPath allows test injection of a fake binary path.
	binPath string
}

func init() {
	bios.Register(&dellProvider{binPath: binaryPath})
}

// Vendor returns "dell".
func (p *dellProvider) Vendor() string { return "dell" }

// binaryAvailable returns true when the operator-supplied binary exists and is
// executable.
func (p *dellProvider) binaryAvailable() bool {
	info, err := os.Stat(p.binPath)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && (info.Mode().Perm()&0o111 != 0)
}

// ReadCurrent executes `racadm get BIOS.SetupConfig` and parses the output.
// Returns bios.ErrBinaryMissing when the binary is absent.
//
// racadm output format:
//
//	[BIOS]
//	Key=Value
//	# comment lines (skipped)
//
// Section headers ([...]) and lines without '=' are silently skipped.
func (p *dellProvider) ReadCurrent(ctx context.Context) ([]bios.Setting, error) {
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s", bios.ErrBinaryMissing, p.binPath)
	}

	cmd := exec.CommandContext(ctx, p.binPath, "get", "BIOS.SetupConfig") //#nosec G204 -- binPath is a fixed operator-supplied path, no user input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dell bios: racadm get failed (exit %v): %s",
			err, strings.TrimSpace(stderr.String()))
	}

	return parseRacadmSettings(stdout.String()), nil
}

// parseRacadmSettings parses racadm's Key=Value output into Settings.
// Section headers ([...]) and lines without '=' are silently skipped.
// Leading/trailing whitespace is trimmed from keys and values.
func parseRacadmSettings(output string) []bios.Setting {
	var settings []bios.Setting
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Skip section headers: [BIOS], [NIC.Slot.1-1], etc.
		if strings.HasPrefix(line, "[") {
			continue
		}
		// Skip comment lines.
		if strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if name == "" {
			continue
		}
		settings = append(settings, bios.Setting{Name: name, Value: value})
	}
	return settings
}

// Diff delegates to the shared bios.Diff helper.
func (p *dellProvider) Diff(desired, current []bios.Setting) ([]bios.Change, error) {
	return bios.Diff(desired, current)
}

// Apply writes each change via `racadm set <Key> <Value>` and then schedules
// them for POST application with `racadm jobqueue create BIOS.Setup.1-1`.
//
// For the post-boot path, callers MUST route through privhelper (bios-apply
// verb).  This direct path is used only inside initramfs (already root, no
// privhelper present).
//
// Returns bios.ErrBinaryMissing when the binary is absent.
// Returns the applied changes on success (all input changes are considered
// applied when racadm exits 0 for every set invocation).
func (p *dellProvider) Apply(ctx context.Context, changes []bios.Change) ([]bios.Change, error) {
	if len(changes) == 0 {
		return nil, nil
	}
	if !p.binaryAvailable() {
		return nil, fmt.Errorf("%w: expected at %s", bios.ErrBinaryMissing, p.binPath)
	}

	// Issue one `racadm set <key> <value>` per change.
	for _, c := range changes {
		cmd := exec.CommandContext(ctx, p.binPath, "set", c.Name, c.To) //#nosec G204 -- binPath is operator-supplied fixed path; c.Name/c.To are BIOS attribute identifiers
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("dell bios: racadm set %s failed (exit %v): %s",
				c.Name, err, strings.TrimSpace(stderr.String()))
		}
	}

	// Schedule all staged settings for application at next POST.
	cmd := exec.CommandContext(ctx, p.binPath, "jobqueue", "create", "BIOS.Setup.1-1") //#nosec G204 -- binPath and args are all fixed literals
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dell bios: racadm jobqueue create failed (exit %v): %s",
			err, strings.TrimSpace(stderr.String()))
	}

	return changes, nil
}

// SupportedSettings returns a curated set of commonly-edited Dell BIOS
// attribute names.  Operators may submit keys outside this list; the provider
// passes them through to racadm without error — this list is used only for
// validation warnings at profile-create time, not hard rejection.
//
// Returns nil when the binary is absent (validation is then skipped with a
// warning at the API layer; failures surface at deploy time).
func (p *dellProvider) SupportedSettings(_ context.Context) ([]string, error) {
	if !p.binaryAvailable() {
		// Non-fatal: caller should warn and proceed.
		return nil, nil
	}
	return []string{
		"BIOS.SysProfileSettings.SysProfile",
		"BIOS.ProcSettings.LogicalProc",
		"BIOS.ProcSettings.ProcVirtualization",
		"BIOS.ProcSettings.NumaNodesPerSocket",
		"BIOS.IntegratedDevices.IoAt",
		"BIOS.SysSecurity.SecureBoot",
		"BIOS.BootSettings.BootMode",
	}, nil
}

// ApplyStaged applies settings from a staged JSON file written by the caller.
// The JSON file must be an array of [{"name":"...","to":"..."}] objects.
// This is the path invoked by the privhelper's bios-apply verb after it has
// already validated vendor and path.
//
// Not part of the bios.Provider interface — used directly by the privhelper.
func ApplyStaged(ctx context.Context, profilePath string) error {
	data, err := os.ReadFile(profilePath) //#nosec G304 -- profilePath validated by privhelper before this call
	if err != nil {
		return fmt.Errorf("dell bios: read staged profile: %w", err)
	}

	var entries []struct {
		Name string `json:"name"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("dell bios: parse staged profile: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	for _, e := range entries {
		cmd := exec.CommandContext(ctx, binaryPath, "set", e.Name, e.To) //#nosec G204 -- binaryPath is a fixed operator path; e.Name/e.To come from the validated staging file
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("dell bios: racadm set %s: %w\noutput: %s", e.Name, err, strings.TrimRight(string(out), "\n"))
		}
	}

	cmd := exec.CommandContext(ctx, binaryPath, "jobqueue", "create", "BIOS.Setup.1-1") //#nosec G204 -- all fixed literals
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dell bios: racadm jobqueue create: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}

	return nil
}

// NewWithBinaryPath constructs a dellProvider with a custom binary path.
// Used in tests to inject a fake racadm binary.
func NewWithBinaryPath(path string) bios.Provider {
	return &dellProvider{binPath: path}
}
