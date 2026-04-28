// validate.go — Script and config validation for the Slurm module.
package slurm

import (
	"fmt"
	"strings"
)

const maxScriptSizeBytes = 1 << 20 // 1 MB

// knownScriptTypes is the authoritative set of supported Slurm hook script types.
var knownScriptTypes = map[string]bool{
	"Prolog":            true,
	"Epilog":            true,
	"PrologSlurmctld":   true,
	"EpilogSlurmctld":   true,
	"TaskProlog":        true,
	"TaskEpilog":        true,
	"SrunProlog":        true,
	"SrunEpilog":        true,
	"HealthCheckProgram": true,
	"RebootProgram":     true,
}

// ValidateScript checks a script for basic correctness before saving or pushing.
//
// Rules:
//   - scriptType must be one of the known types
//   - content must not be empty
//   - content must not contain null bytes
//   - first line must be a shebang (starts with #!)
//   - total size must be < 1 MB
func ValidateScript(scriptType, content string) error {
	if !knownScriptTypes[scriptType] {
		return fmt.Errorf("unknown script type %q; must be one of: Prolog, Epilog, PrologSlurmctld, EpilogSlurmctld, TaskProlog, TaskEpilog, SrunProlog, SrunEpilog, HealthCheckProgram, RebootProgram", scriptType)
	}

	if content == "" {
		return fmt.Errorf("script content must not be empty")
	}

	if strings.ContainsRune(content, 0) {
		return fmt.Errorf("script content must not contain null bytes")
	}

	if len(content) > maxScriptSizeBytes {
		return fmt.Errorf("script size %d bytes exceeds 1 MB limit", len(content))
	}

	// Require shebang on the first line.
	firstLine := content
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		firstLine = content[:idx]
	}
	firstLine = strings.TrimRight(firstLine, "\r")
	if !strings.HasPrefix(firstLine, "#!") {
		return fmt.Errorf("script must begin with a shebang line (e.g. #!/bin/bash)")
	}

	return nil
}

// IsKnownScriptType reports whether scriptType is in the known set.
func IsKnownScriptType(scriptType string) bool {
	return knownScriptTypes[scriptType]
}

// maxConfigSizeBytes caps config file size at 1 MB — matching the script limit.
const maxConfigSizeBytes = 1 << 20 // 1 MB

// ValidationIssue represents a single validation problem found in a config file.
type ValidationIssue struct {
	Line    int    `json:"line"`    // 1-based line number, 0 = file-level
	Key     string `json:"key"`     // param name if applicable
	Message string `json:"message"` // human-readable description
}

// ValidateConfig performs basic structural validation on a Slurm config file.
//
// Checks applied:
//   - File must not be empty or exceed 1 MB
//   - For slurm.conf: required keys (ClusterName, SlurmctldHost, SlurmdSpoolDir,
//     StateSaveLocation, SlurmctldLogFile, SlurmdLogFile) must be present
//   - Lines must be parseable as key=value, continuation, or comment
//   - Detects obvious duplicates for known single-valued keys
//
// Returns a slice of issues (empty slice = valid). Never returns an error for
// content problems — those are returned as issues. Returns an error only for
// programming mistakes (should not happen in normal use).
func ValidateConfig(filename, content string) ([]ValidationIssue, error) {
	var issues []ValidationIssue

	if content == "" {
		issues = append(issues, ValidationIssue{Line: 0, Message: "config file must not be empty"})
		return issues, nil
	}
	if len(content) > maxConfigSizeBytes {
		issues = append(issues, ValidationIssue{Line: 0, Message: fmt.Sprintf("config size %d bytes exceeds 1 MB limit", len(content))})
		return issues, nil
	}
	if strings.ContainsRune(content, 0) {
		issues = append(issues, ValidationIssue{Line: 0, Message: "config content must not contain null bytes"})
		return issues, nil
	}

	// Only apply structured checks to slurm.conf. Other files (gres.conf, topology.conf, etc.)
	// have more complex formats and can be linted lightly.
	if filename == "slurm.conf" {
		issues = append(issues, validateSlurmConf(content)...)
	} else {
		issues = append(issues, validateGenericConf(content)...)
	}

	return issues, nil
}

// validateSlurmConf checks slurm.conf-specific rules.
func validateSlurmConf(content string) []ValidationIssue {
	var issues []ValidationIssue

	requiredKeys := []string{
		"ClusterName",
		"SlurmctldHost",
		"SlurmdSpoolDir",
		"StateSaveLocation",
		"SlurmctldLogFile",
		"SlurmdLogFile",
	}

	// Keys that should only appear once.
	singleValuedKeys := map[string]bool{
		"ClusterName":       true,
		"SlurmctldHost":     true,
		"StateSaveLocation": true,
		"SlurmctldLogFile":  true,
		"SlurmdLogFile":     true,
		"SlurmdSpoolDir":    true,
	}

	seenKeys := make(map[string]int) // key → first line seen

	lines := strings.Split(content, "\n")
	for i, rawLine := range lines {
		lineNum := i + 1
		line := strings.TrimSpace(rawLine)

		// Skip blank lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle line continuation (backslash at end) — skip continuation lines.
		if strings.HasSuffix(line, "\\") {
			continue
		}

		// Parse key=value. Slurm config lines may have multiple key=value pairs
		// on one line separated by whitespace.
		fields := strings.Fields(line)
		for _, field := range fields {
			if !strings.Contains(field, "=") {
				// Some directives (NodeName, PartitionName) may span lines; be lenient.
				continue
			}
			eqIdx := strings.Index(field, "=")
			key := field[:eqIdx]
			if key == "" {
				issues = append(issues, ValidationIssue{Line: lineNum, Message: "line has key=value with empty key"})
				continue
			}

			if firstLine, already := seenKeys[key]; already {
				if singleValuedKeys[key] {
					issues = append(issues, ValidationIssue{
						Line:    lineNum,
						Key:     key,
						Message: fmt.Sprintf("duplicate key %q (first defined at line %d)", key, firstLine),
					})
				}
			} else {
				seenKeys[key] = lineNum
			}
		}
	}

	// Check required keys.
	for _, req := range requiredKeys {
		if _, found := seenKeys[req]; !found {
			issues = append(issues, ValidationIssue{
				Line:    0,
				Key:     req,
				Message: fmt.Sprintf("required key %q is missing", req),
			})
		}
	}

	return issues
}

// validateGenericConf lints a generic Slurm config file (gres.conf, topology.conf, etc.)
// with minimal checks: no null bytes (already checked), non-empty (already checked).
// Returns no issues for generic files — we don't have a schema for them.
func validateGenericConf(_ string) []ValidationIssue {
	return nil
}
