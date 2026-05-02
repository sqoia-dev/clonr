package stats

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// runWithTimeout runs a command with a 10-second timeout (or ctx deadline if
// shorter). It captures stdout and returns it as bytes.
// Errors from the command (non-zero exit) are returned as Go errors.
func runWithTimeout(ctx context.Context, name string, args ...string) ([]byte, error) {
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(tCtx, name, args...) //#nosec G204 -- stats plugin; caller validates binary path via LookPath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
