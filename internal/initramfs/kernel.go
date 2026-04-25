// Package initramfs provides helpers for inspecting clustr initramfs images.
package initramfs

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// ExtractKernelVersion returns the kernel version embedded in a gzipped cpio
// initramfs image at path.
//
// The version is read from the directory name under lib/modules/<version>/ in
// the archive — the same location used by modprobe and the Linux kernel.
//
// Implementation: shells out to "zcat <path> | cpio -it" and greps the first
// lib/modules/<version> entry. This avoids a pure-Go cpio dependency (none is
// in go.mod) while being straightforward and reliable on any Linux host that
// has cpio and gzip in PATH (both are standard on EL9/Ubuntu).
//
// Returns a non-nil error if the file cannot be decompressed, if cpio is not
// available, or if no lib/modules entry is found. The caller should treat an
// error as "version unknown" and continue rather than fail hard.
func ExtractKernelVersion(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("initramfs: path is empty")
	}

	// We pipe zcat output into cpio via the shell.  The shell is always
	// present on the target host (it runs clustr-serverd via systemd).
	cmd := exec.Command("sh", "-c", //nolint:gosec
		"zcat "+shellQuote(path)+" | cpio -it 2>/dev/null",
	)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Stderr from cpio (e.g. "NNN blocks") goes to /dev/null via the shell
	// redirect above; zcat stderr is discarded so we do not conflate it with
	// a real error — exit status is our signal.

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("initramfs: extract listing failed: %w", err)
	}

	// Scan output lines for the first "lib/modules/<version>" prefix.
	// cpio -it prints one path per line, e.g.:
	//   lib/modules/5.14.0-611.5.1.el9_7.x86_64
	//   lib/modules/5.14.0-611.5.1.el9_7.x86_64/kernel
	//   ...
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		const prefix = "lib/modules/"
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		// rest is "<version>" or "<version>/<more>"; take only the version part.
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
		}
		if rest == "" {
			continue
		}
		return rest, nil
	}

	return "", fmt.Errorf("initramfs: no lib/modules entry found in %s", path)
}

// shellQuote returns a single-quoted shell-safe version of s.
// Single quotes are safe for file paths that do not themselves contain single
// quotes (initramfs paths on clustr hosts never do).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
