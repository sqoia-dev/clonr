// slurmadmin.go — Client-side handler for slurm_admin_cmd messages.
// Only processed when slurmctld is active on this node (controller role).
// Supports: drain, resume, check_queue, reconfigure — all via scontrol.
//
// Security: all commands are built as []string (no shell expansion), and
// node names are validated to be safe before use. This handler is intentionally
// restricted to the controller node because only slurmctld can execute scontrol.
package clientd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"
)

const (
	// slurmAdminCmdTimeout is the per-command execution timeout.
	slurmAdminCmdTimeout = 60 * time.Second
	// maxJobCheckNodes is the max node count we'll pass to squeue in one call.
	maxJobCheckNodes = 256
)

// handleSlurmAdminCmd executes a Slurm administrative command on behalf of the
// upgrade orchestrator. Returns a SlurmAdminCmdResult to send back to the server.
//
// The caller (client.go) should only invoke this if slurmctld is active here.
func handleSlurmAdminCmd(payload SlurmAdminCmdPayload) SlurmAdminCmdResult {
	// Validate that all node names are safe before building args.
	for _, n := range payload.Nodes {
		if !isSafeNodeName(n) {
			return adminCmdError(fmt.Sprintf("unsafe node name %q rejected", n))
		}
	}

	switch payload.Command {
	case "drain":
		return adminDrain(payload.Nodes, payload.Reason)
	case "resume":
		return adminResume(payload.Nodes)
	case "check_queue":
		return adminCheckQueue(payload.Nodes)
	case "reconfigure":
		return adminReconfigure()
	default:
		return adminCmdError(fmt.Sprintf("unknown slurm_admin_cmd command %q", payload.Command))
	}
}

// adminDrain runs: scontrol update NodeName=<nodes> State=DRAIN Reason=<reason>
func adminDrain(nodes []string, reason string) SlurmAdminCmdResult {
	if len(nodes) == 0 {
		return adminCmdError("drain: no nodes specified")
	}
	if reason == "" {
		reason = "clonr-upgrade"
	}

	nodeArg := "NodeName=" + strings.Join(nodes, ",")
	ctx, cancel := context.WithTimeout(context.Background(), slurmAdminCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "scontrol", "update", nodeArg, "State=DRAIN", "Reason="+reason)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		log.Warn().Err(err).Strs("nodes", nodes).Msg("slurmadmin: drain failed")
		return SlurmAdminCmdResult{
			OK:     false,
			Output: output,
			Error:  fmt.Sprintf("scontrol update drain: %v", err),
		}
	}

	log.Info().Strs("nodes", nodes).Msg("slurmadmin: drain applied")
	return SlurmAdminCmdResult{OK: true, Output: output}
}

// adminResume runs: scontrol update NodeName=<nodes> State=RESUME
func adminResume(nodes []string) SlurmAdminCmdResult {
	if len(nodes) == 0 {
		return adminCmdError("resume: no nodes specified")
	}

	nodeArg := "NodeName=" + strings.Join(nodes, ",")
	ctx, cancel := context.WithTimeout(context.Background(), slurmAdminCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "scontrol", "update", nodeArg, "State=RESUME")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		log.Warn().Err(err).Strs("nodes", nodes).Msg("slurmadmin: resume failed")
		return SlurmAdminCmdResult{
			OK:     false,
			Output: output,
			Error:  fmt.Sprintf("scontrol update resume: %v", err),
		}
	}

	log.Info().Strs("nodes", nodes).Msg("slurmadmin: resume applied")
	return SlurmAdminCmdResult{OK: true, Output: output}
}

// adminCheckQueue runs: squeue --noheader --nodes=<nodes>
// Returns the count of running/pending jobs on those nodes.
// If nodes is empty, checks the entire cluster.
func adminCheckQueue(nodes []string) SlurmAdminCmdResult {
	ctx, cancel := context.WithTimeout(context.Background(), slurmAdminCmdTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if len(nodes) > 0 {
		nodeList := strings.Join(nodes, ",")
		cmd = exec.CommandContext(ctx, "squeue", "--noheader", "--nodes="+nodeList)
	} else {
		cmd = exec.CommandContext(ctx, "squeue", "--noheader")
	}

	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		// squeue returns non-zero if no nodes found or cluster is down.
		// Treat this as job count = 0 if output is empty (all drained).
		if output == "" {
			return SlurmAdminCmdResult{OK: true, Output: "", JobCount: 0}
		}
		log.Warn().Err(err).Strs("nodes", nodes).Msg("slurmadmin: check_queue failed")
		return SlurmAdminCmdResult{
			OK:     false,
			Output: output,
			Error:  fmt.Sprintf("squeue: %v", err),
		}
	}

	// Count non-empty lines — each represents one job step.
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}

	log.Debug().Int("job_count", count).Strs("nodes", nodes).Msg("slurmadmin: check_queue complete")
	return SlurmAdminCmdResult{OK: true, Output: output, JobCount: count}
}

// adminReconfigure runs: scontrol reconfigure
func adminReconfigure() SlurmAdminCmdResult {
	ctx, cancel := context.WithTimeout(context.Background(), slurmAdminCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "scontrol", "reconfigure")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		log.Warn().Err(err).Msg("slurmadmin: reconfigure failed")
		return SlurmAdminCmdResult{
			OK:     false,
			Output: output,
			Error:  fmt.Sprintf("scontrol reconfigure: %v", err),
		}
	}

	log.Info().Msg("slurmadmin: reconfigure applied")
	return SlurmAdminCmdResult{OK: true, Output: output}
}

// adminCmdError is a convenience constructor for a failed SlurmAdminCmdResult.
func adminCmdError(msg string) SlurmAdminCmdResult {
	return SlurmAdminCmdResult{OK: false, Error: msg}
}

// isSafeNodeName validates a Slurm node name.
// Allows alphanumeric, hyphens, underscores, brackets (for node range notation),
// and dots. Rejects everything else to prevent command injection.
func isSafeNodeName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			continue
		}
		switch c {
		case '-', '_', '.', '[', ']':
			continue
		}
		return false
	}
	return true
}
