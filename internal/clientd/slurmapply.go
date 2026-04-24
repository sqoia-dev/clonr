// slurmapply.go — Client-side handler for slurm_config_push messages.
// Validates checksums, writes Slurm config files atomically, runs the apply
// action (reconfigure or restart), and returns a SlurmConfigAckPayload.
package clientd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// slurmDestPath maps logical filenames to their destination paths under /etc/slurm/.
// Only filenames in this whitelist may be written. Unknown filenames are rejected.
var slurmDestPath = map[string]string{
	"slurm.conf":    "/etc/slurm/slurm.conf",
	"gres.conf":     "/etc/slurm/gres.conf",
	"cgroup.conf":   "/etc/slurm/cgroup.conf",
	"topology.conf": "/etc/slurm/topology.conf",
	"plugstack.conf": "/etc/slurm/plugstack.conf",
	"slurmdbd.conf": "/etc/slurm/slurmdbd.conf",
}

// slurmFileMode returns the file permission for a given Slurm config file.
// slurmdbd.conf is 0600 (contains DB credentials); all others are 0644.
func slurmFileMode(filename string) os.FileMode {
	if filename == "slurmdbd.conf" {
		return 0600
	}
	return 0644
}

const maxSlurmConfigSizeBytes = 2 << 20 // 2 MB

// applySlurmConfig writes all files from the push payload atomically, then
// runs the specified apply action. Returns a SlurmConfigAckPayload describing
// the outcome of every file write and the apply action.
func applySlurmConfig(payload SlurmConfigPushPayload) SlurmConfigAckPayload {
	fileResults := make([]SlurmFileApplyResult, 0, len(payload.Files))
	var bakPaths []string // track backups for rollback on restart failure

	// Phase 1: validate + write all files atomically.
	allFilesOK := true
	for _, f := range payload.Files {
		result := writeSlurmFile(f)
		fileResults = append(fileResults, result)
		if result.OK {
			dest := resolveDestPath(f)
			bakPaths = append(bakPaths, dest+".bak")
		} else {
			allFilesOK = false
		}
	}

	if !allFilesOK {
		failedFiles := collectFailedFilenames(fileResults)
		return SlurmConfigAckPayload{
			PushOpID:    payload.PushOpID,
			OK:          false,
			Error:       fmt.Sprintf("file write failed for: %s", strings.Join(failedFiles, ", ")),
			FileResults: fileResults,
		}
	}

	// Phase 2: run apply action.
	applyOutput, applyExit, applyErr := runSlurmApplyAction(payload.ApplyAction)

	if applyErr != nil {
		// For restart: attempt rollback of all written files, then retry restart.
		if payload.ApplyAction == "restart" {
			log.Warn().Err(applyErr).Msg("slurmapply: restart failed; attempting rollback")
			rollbackSlurmFiles(payload.Files)
			// Retry restart after rollback.
			retryOut, retryExit, retryErr := runSlurmApplyAction(payload.ApplyAction)
			if retryErr != nil {
				applyOutput = applyOutput + "\n[rollback attempted]\n" + retryOut
				applyExit = retryExit
			} else {
				applyOutput = applyOutput + "\n[rollback successful, restart recovered]\n" + retryOut
				applyExit = retryExit
				// Files were rolled back, so mark all as failed.
				for i := range fileResults {
					fileResults[i].OK = false
					fileResults[i].Error = "rolled back due to restart failure"
				}
				return SlurmConfigAckPayload{
					PushOpID:      payload.PushOpID,
					OK:            false,
					Error:         "restart failed after file write; files rolled back",
					FileResults:   fileResults,
					ApplyOutput:   truncate(applyOutput, 2048),
					ApplyExitCode: applyExit,
				}
			}
		}

		return SlurmConfigAckPayload{
			PushOpID:      payload.PushOpID,
			OK:            false,
			Error:         "apply action failed: " + applyErr.Error(),
			FileResults:   fileResults,
			ApplyOutput:   truncate(applyOutput, 2048),
			ApplyExitCode: applyExit,
		}
	}

	return SlurmConfigAckPayload{
		PushOpID:      payload.PushOpID,
		OK:            true,
		FileResults:   fileResults,
		ApplyOutput:   truncate(applyOutput, 2048),
		ApplyExitCode: applyExit,
	}
}

// writeSlurmFile validates and atomically writes one Slurm config file.
func writeSlurmFile(f SlurmFilePush) SlurmFileApplyResult {
	// Validate filename against whitelist.
	destPath, ok := slurmDestPath[f.Filename]
	if !ok {
		// Allow server-provided DestPath if it's under /etc/slurm/ (belt-and-suspenders).
		if f.DestPath != "" && strings.HasPrefix(f.DestPath, "/etc/slurm/") {
			destPath = f.DestPath
		} else {
			return SlurmFileApplyResult{
				Filename: f.Filename,
				OK:       false,
				Error:    fmt.Sprintf("unknown/disallowed filename %q", f.Filename),
			}
		}
	}

	if len(f.Content) > maxSlurmConfigSizeBytes {
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    fmt.Sprintf("content size %d exceeds 2 MB limit", len(f.Content)),
		}
	}

	if err := validateChecksum(f.Content, f.Checksum); err != nil {
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    err.Error(),
		}
	}

	mode := slurmFileMode(f.Filename)
	bakPath := destPath + ".bak"
	tmpPath := destPath + ".tmp"

	// Back up existing file before overwrite.
	if err := copyFile(destPath, bakPath); err != nil && !os.IsNotExist(err) {
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    fmt.Sprintf("backup failed: %v", err),
		}
	}

	// Write to .tmp then rename into place.
	if err := os.WriteFile(tmpPath, []byte(f.Content), mode); err != nil {
		_ = os.Remove(tmpPath)
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    fmt.Sprintf("write temp file: %v", err),
		}
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    fmt.Sprintf("chmod temp file: %v", err),
		}
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return SlurmFileApplyResult{
			Filename: f.Filename,
			OK:       false,
			Error:    fmt.Sprintf("rename into place: %v", err),
		}
	}

	log.Info().Str("filename", f.Filename).Str("dest", destPath).Msg("slurmapply: file written successfully")
	return SlurmFileApplyResult{Filename: f.Filename, OK: true}
}

// rollbackSlurmFiles restores .bak files for every file in the push.
// Best-effort: errors are logged but do not propagate.
func rollbackSlurmFiles(files []SlurmFilePush) {
	for _, f := range files {
		destPath := resolveDestPath(f)
		bakPath := destPath + ".bak"
		if err := copyFile(bakPath, destPath); err != nil {
			if !os.IsNotExist(err) {
				log.Error().Err(err).Str("filename", f.Filename).Msg("slurmapply: rollback failed")
			}
		} else {
			log.Info().Str("filename", f.Filename).Msg("slurmapply: file rolled back")
		}
	}
}

// resolveDestPath returns the destination path for a SlurmFilePush, preferring
// the whitelist entry and falling back to the server-provided DestPath.
func resolveDestPath(f SlurmFilePush) string {
	if p, ok := slurmDestPath[f.Filename]; ok {
		return p
	}
	return f.DestPath
}

// runSlurmApplyAction executes either "scontrol reconfigure" or detects which
// Slurm service is running and restarts it. Returns (combinedOutput, exitCode, error).
func runSlurmApplyAction(action string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch action {
	case "reconfigure":
		cmd := exec.CommandContext(ctx, "scontrol", "reconfigure")
		out, err := cmd.CombinedOutput()
		exit := 0
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}
		if err != nil {
			return string(out), exit, fmt.Errorf("scontrol reconfigure: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}
		log.Info().Msg("slurmapply: scontrol reconfigure succeeded")
		return string(out), exit, nil

	case "restart":
		// Detect which Slurm daemon is active on this node.
		service := detectActiveSlurmService()
		if service == "" {
			// No Slurm daemon running — files written, apply action is a no-op.
			log.Warn().Msg("slurmapply: no active slurm service detected; skipping restart")
			return "no active slurm service found", 0, nil
		}

		cmd := exec.CommandContext(ctx, "systemctl", "restart", service)
		out, err := cmd.CombinedOutput()
		exit := 0
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}
		if err != nil {
			return string(out), exit, fmt.Errorf("systemctl restart %s: %w (output: %s)", service, err, strings.TrimSpace(string(out)))
		}
		log.Info().Str("service", service).Msg("slurmapply: service restarted successfully")
		return string(out), exit, nil

	default:
		return "", 0, fmt.Errorf("unknown apply action %q; must be 'reconfigure' or 'restart'", action)
	}
}

// detectActiveSlurmService checks which Slurm service (slurmctld or slurmd) is
// currently active on this node. Returns the service name or empty string if neither is active.
func detectActiveSlurmService() string {
	for _, svc := range []string{"slurmctld", "slurmd"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", svc)
		err := cmd.Run()
		cancel()
		if err == nil {
			return svc
		}
	}
	return ""
}

// collectFailedFilenames returns the filenames of all failed SlurmFileApplyResult entries.
func collectFailedFilenames(results []SlurmFileApplyResult) []string {
	var failed []string
	for _, r := range results {
		if !r.OK {
			failed = append(failed, r.Filename)
		}
	}
	return failed
}

// truncate returns s truncated to maxLen bytes with a suffix if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…[truncated]"
}
