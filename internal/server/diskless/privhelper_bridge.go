package diskless

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// nfsExportHelperPath is the canonical install path for the setuid binary.
const nfsExportHelperPath = "/usr/sbin/clustr-privhelper"

// nfsExportViaPrivhelper invokes clustr-privhelper nfs-export to write
// /etc/exports and run exportfs -ra.  This is always called as the
// unprivileged clustr user; the setuid bit elevates the helper to root.
//
// Argv constructed internally — the caller only supplies bounded identifiers
// (UUID and CIDR string) that the helper validates a second time before
// touching any file.
func nfsExportViaPrivhelper(ctx context.Context, imageID, subnet string) error {
	cmd := exec.CommandContext(ctx, nfsExportHelperPath, //#nosec G204 -- imageID validated as UUID, subnet as CIDR; helper re-validates
		"nfs-export",
		"--image-id", imageID,
		"--subnet", subnet,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clustr-privhelper nfs-export: %w\noutput: %s",
			err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}
