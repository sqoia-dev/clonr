package deploy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// clientdServiceUnit is the systemd unit file for clustr-clientd on deployed nodes.
// It starts after network-online.target and restarts on failure with an exponential
// backoff (handled in the client itself; RestartSec here is just the floor).
const clientdServiceUnit = `[Unit]
Description=clustr node agent (clientd)
Documentation=https://github.com/sqoia-dev/clustr
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/clustr/node-token
ConditionPathExists=/etc/clustr/clustrd-url

[Service]
Type=simple
ExecStart=/usr/local/bin/clustr-clientd
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=clustr-clientd

[Install]
WantedBy=multi-user.target
`

// clientdDefaultConf is the default configuration written to /etc/clustr/clientd.conf.
// In later sprints the daemon will reload this file on SIGHUP.
const clientdDefaultConf = `# clustr-clientd configuration — managed by clustr-serverd
# Do not edit manually; changes may be overwritten during config push.

heartbeat_interval = 60

# Whitelist of systemd services monitored in each heartbeat.
# Comma-separated. Add site-specific services here.
services = sssd,munge,slurmd,slurmctld,sshd,chronyd
`

// findClientdBin resolves the path to the clustr-clientd binary using a priority
// search:
//  1. hint — caller-supplied path (e.g. from CLUSTR_CLIENTD_BIN_PATH env).
//  2. Alongside the running binary: filepath.Dir(os.Args[0]) + "/clustr-clientd".
//  3. /opt/clustr/bin/clustr-clientd
//  4. /usr/local/bin/clustr-clientd
//
// Returns the first path that exists and is a regular file, or "" if none found.
func findClientdBin(hint string) string {
	candidates := []string{}

	if hint != "" {
		candidates = append(candidates, hint)
	}

	// Resolve path relative to the running binary (works in both initramfs and
	// production server layouts where clientd lives alongside serverd/clustr-static).
	if len(os.Args) > 0 {
		candidates = append(candidates, filepath.Join(filepath.Dir(os.Args[0]), "clustr-clientd"))
	}

	candidates = append(candidates,
		"/opt/clustr/bin/clustr-clientd",
		"/usr/local/bin/clustr-clientd",
		"/usr/bin/clustr-clientd",
	)

	for _, p := range candidates {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// copyClientdBin copies the clustr-clientd binary from src to dst with mode 0755.
func copyClientdBin(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir dest dir: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return out.Close()
}

// injectClientd writes the clustr-clientd configuration, systemd unit, and binary
// into the deployed rootfs at mountRoot.
//
// It:
//  1. Copies the clustr-clientd binary to mountRoot/usr/local/bin/clustr-clientd (mode 0755).
//  2. Writes /etc/clustr/clustrd-url (0644) — the WebSocket endpoint for clientd.
//  3. Writes /etc/clustr/clientd.conf (0644) — default daemon config.
//  4. Writes /etc/systemd/system/clustr-clientd.service (0644).
//  5. Enables the service by creating the WantedBy=multi-user.target symlink.
//
// clientdBinHint is the caller-supplied binary path (e.g. from CLUSTR_CLIENTD_BIN_PATH
// env). Pass "" to rely on auto-detection.
//
// This is a no-op when clientdURL is empty (caller opts out by leaving it blank).
// Non-fatal: a missing clientd binary is warned but does not fail the deploy — the
// node still boots and reports via verify-boot.
func injectClientd(mountRoot, clientdURL, clientdBinHint string) error {
	if clientdURL == "" {
		return nil
	}

	log := logger()

	// Ensure /etc/clustr/ exists (created by injectPhoneHome, but be idempotent).
	clustrDir := filepath.Join(mountRoot, "etc", "clustr")
	if err := os.MkdirAll(clustrDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir /etc/clustr: %w", err)
	}

	// ── 1. Copy clustr-clientd binary ─────────────────────────────────────────
	clientdBinSrc := findClientdBin(clientdBinHint)
	clientdBinDst := filepath.Join(mountRoot, "usr", "local", "bin", "clustr-clientd")
	if clientdBinSrc != "" {
		if err := copyClientdBin(clientdBinSrc, clientdBinDst); err != nil {
			// Non-fatal: the node still boots and reports via verify-boot.
			// The service unit is still written so the node self-heals on next
			// config push if the binary is later delivered.
			log.Warn().Err(err).
				Str("src", clientdBinSrc).
				Str("dst", clientdBinDst).
				Msg("finalize: failed to copy clustr-clientd binary (non-fatal)")
		} else {
			log.Info().
				Str("src", clientdBinSrc).
				Str("dst", clientdBinDst).
				Msg("finalize: clustr-clientd binary installed")
		}
	} else {
		log.Warn().
			Str("hint", clientdBinHint).
			Msg("finalize: clustr-clientd binary not found — service unit will be written but binary is missing; node-agent will not start until binary is delivered")
	}

	// ── 3. Write clustrd-url ──────────────────────────────────────────────────
	urlPath := filepath.Join(clustrDir, "clustrd-url")
	if err := os.WriteFile(urlPath, []byte(clientdURL+"\n"), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clustrd-url: %w", err)
	}

	// ── 4. Write clientd.conf ────────────────────────────────────────────────
	confPath := filepath.Join(clustrDir, "clientd.conf")
	if err := os.WriteFile(confPath, []byte(clientdDefaultConf), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clientd.conf: %w", err)
	}

	// ── 5. Write systemd unit ────────────────────────────────────────────────
	systemdDir := filepath.Join(mountRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir systemd/system: %w", err)
	}
	unitPath := filepath.Join(systemdDir, "clustr-clientd.service")
	if err := os.WriteFile(unitPath, []byte(clientdServiceUnit), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clustr-clientd.service: %w", err)
	}

	// ── 6. Enable the unit via direct symlink ────────────────────────────────
	wantsDir := filepath.Join(mountRoot, "etc", "systemd", "system", "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir multi-user.target.wants: %w", err)
	}
	linkPath := filepath.Join(wantsDir, "clustr-clientd.service")
	const wantTarget = "../clustr-clientd.service"

	// Idempotent: remove stale/wrong symlink before creating the correct one.
	if existing, lstatErr := os.Lstat(linkPath); lstatErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			if target, rErr := os.Readlink(linkPath); rErr == nil && target == wantTarget {
				goto symlinkDone
			}
		}
		if rmErr := os.Remove(linkPath); rmErr != nil {
			return fmt.Errorf("clientd inject: remove stale wants entry: %w", rmErr)
		}
	}
	if err := os.Symlink(wantTarget, linkPath); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("clientd inject: create WantedBy symlink: %w", err)
	}
symlinkDone:

	log.Info().
		Str("clientd_url", clientdURL).
		Str("unit_path", unitPath).
		Msg("finalize: clustr-clientd systemd unit installed and enabled")

	return nil
}
