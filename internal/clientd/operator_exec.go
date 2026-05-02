package clientd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// operatorExecOutputCap is the per-stream output cap for operator_exec_request (1 MB).
	// Higher than the diagnostic exec cap because operator commands may produce large output.
	operatorExecOutputCap = 1 << 20 // 1 MB

	// operatorExecDefaultTimeout is the default execution timeout for operator commands.
	operatorExecDefaultTimeout = 60 * time.Second

	// operatorExecMaxTimeout is the hard-cap on operator execution timeout (1 hour).
	operatorExecMaxTimeout = time.Hour
)

// handleOperatorExecRequest executes an arbitrary command on behalf of the server.
// Unlike handleExecRequest (which enforces a whitelist), this runs any command the
// operator requests. The server is responsible for ensuring only admin/operator-scoped
// API keys can trigger this path.
//
// Commands are NEVER executed through a shell — exec.Command takes the args list
// directly so there is no shell injection risk from the command/args themselves.
// However, the operator MUST be trusted: they can run any binary on the node.
func handleOperatorExecRequest(payload OperatorExecRequestPayload) OperatorExecResultPayload {
	ref := payload.RefMsgID
	if payload.Command == "" {
		return OperatorExecResultPayload{
			RefMsgID: ref,
			ExitCode: -1,
			Error:    "command is required",
		}
	}

	// Resolve timeout: use payload value, fall back to default, cap at max.
	timeout := operatorExecDefaultTimeout
	if payload.TimeoutSec > 0 {
		t := time.Duration(payload.TimeoutSec) * time.Second
		if t > operatorExecMaxTimeout {
			t = operatorExecMaxTimeout
		}
		timeout = t
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if payload.Args == nil {
		payload.Args = []string{}
	}

	// NEVER use /bin/sh -c or any shell expansion. Direct exec only.
	cmd := exec.CommandContext(ctx, payload.Command, payload.Args...) //#nosec G204 -- operator-issued command; server validates scope

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutLW := &limitWriter{w: &stdoutBuf, remaining: operatorExecOutputCap}
	stderrLW := &limitWriter{w: &stderrBuf, remaining: operatorExecOutputCap}
	cmd.Stdout = stdoutLW
	cmd.Stderr = stderrLW

	runErr := cmd.Run()

	exitCode := 0
	errMsg := ""

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			exitCode = -1
			errMsg = fmt.Sprintf("command timed out after %s", timeout)
		} else {
			exitCode = -1
			errMsg = runErr.Error()
		}
	}

	truncated := stdoutLW.capped || stderrLW.capped

	log.Info().
		Str("command", payload.Command).
		Strs("args", payload.Args).
		Int("exit_code", exitCode).
		Bool("truncated", truncated).
		Str("ref_msg_id", ref).
		Msg("clientd operator-exec: command completed")

	return OperatorExecResultPayload{
		RefMsgID:  ref,
		ExitCode:  exitCode,
		Stdout:    stdoutBuf.String(),
		Stderr:    stderrBuf.String(),
		Truncated: truncated,
		Error:     errMsg,
	}
}
