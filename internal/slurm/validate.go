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
