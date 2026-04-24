package deploy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// clientdServiceUnit is the systemd unit file for clonr-clientd on deployed nodes.
// It starts after network-online.target and restarts on failure with an exponential
// backoff (handled in the client itself; RestartSec here is just the floor).
const clientdServiceUnit = `[Unit]
Description=clonr node agent (clientd)
Documentation=https://github.com/sqoia-dev/clonr
After=network-online.target
Wants=network-online.target
ConditionPathExists=/etc/clonr/node-token
ConditionPathExists=/etc/clonr/clonrd-url

[Service]
Type=simple
ExecStart=/usr/local/bin/clonr-clientd
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=clonr-clientd

[Install]
WantedBy=multi-user.target
`

// clientdDefaultConf is the default configuration written to /etc/clonr/clientd.conf.
// In later sprints the daemon will reload this file on SIGHUP.
const clientdDefaultConf = `# clonr-clientd configuration — managed by clonr-serverd
# Do not edit manually; changes may be overwritten during config push.

heartbeat_interval = 60

# Whitelist of systemd services monitored in each heartbeat.
# Comma-separated. Add site-specific services here.
services = sssd,munge,slurmd,slurmctld,sshd,chronyd
`

// findClientdBin resolves the path to the clonr-clientd binary using a priority
// search:
//  1. hint — caller-supplied path (e.g. from CLONR_CLIENTD_BIN_PATH env).
//  2. Alongside the running binary: filepath.Dir(os.Args[0]) + "/clonr-clientd".
//  3. /opt/clonr/bin/clonr-clientd
//  4. /usr/local/bin/clonr-clientd
//
// Returns the first path that exists and is a regular file, or "" if none found.
func findClientdBin(hint string) string {
	candidates := []string{}

	if hint != "" {
		candidates = append(candidates, hint)
	}

	// Resolve path relative to the running binary (works in both initramfs and
	// production server layouts where clientd lives alongside serverd/clonr-static).
	if len(os.Args) > 0 {
		candidates = append(candidates, filepath.Join(filepath.Dir(os.Args[0]), "clonr-clientd"))
	}

	candidates = append(candidates,
		"/opt/clonr/bin/clonr-clientd",
		"/usr/local/bin/clonr-clientd",
		"/usr/bin/clonr-clientd",
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

// copyClientdBin copies the clonr-clientd binary from src to dst with mode 0755.
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

// injectClientd writes the clonr-clientd configuration, systemd unit, and binary
// into the deployed rootfs at mountRoot.
//
// It:
//  1. Copies the clonr-clientd binary to mountRoot/usr/local/bin/clonr-clientd (mode 0755).
//  2. Writes /etc/clonr/clonrd-url (0644) — the WebSocket endpoint for clientd.
//  3. Writes /etc/clonr/clientd.conf (0644) — default daemon config.
//  4. Writes /etc/systemd/system/clonr-clientd.service (0644).
//  5. Enables the service by creating the WantedBy=multi-user.target symlink.
//
// clientdBinHint is the caller-supplied binary path (e.g. from CLONR_CLIENTD_BIN_PATH
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

	// Ensure /etc/clonr/ exists (created by injectPhoneHome, but be idempotent).
	clonrDir := filepath.Join(mountRoot, "etc", "clonr")
	if err := os.MkdirAll(clonrDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir /etc/clonr: %w", err)
	}

	// ── 1. Copy clonr-clientd binary ─────────────────────────────────────────
	clientdBinSrc := findClientdBin(clientdBinHint)
	clientdBinDst := filepath.Join(mountRoot, "usr", "local", "bin", "clonr-clientd")
	if clientdBinSrc != "" {
		if err := copyClientdBin(clientdBinSrc, clientdBinDst); err != nil {
			// Non-fatal: the node still boots and reports via verify-boot.
			// The service unit is still written so the node self-heals on next
			// config push if the binary is later delivered.
			log.Warn().Err(err).
				Str("src", clientdBinSrc).
				Str("dst", clientdBinDst).
				Msg("finalize: failed to copy clonr-clientd binary (non-fatal)")
		} else {
			log.Info().
				Str("src", clientdBinSrc).
				Str("dst", clientdBinDst).
				Msg("finalize: clonr-clientd binary installed")
		}
	} else {
		log.Warn().
			Str("hint", clientdBinHint).
			Msg("finalize: clonr-clientd binary not found — service unit will be written but binary is missing; node-agent will not start until binary is delivered")
	}

	// ── 3. Write clonrd-url ──────────────────────────────────────────────────
	urlPath := filepath.Join(clonrDir, "clonrd-url")
	if err := os.WriteFile(urlPath, []byte(clientdURL+"\n"), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clonrd-url: %w", err)
	}

	// ── 4. Write clientd.conf ────────────────────────────────────────────────
	confPath := filepath.Join(clonrDir, "clientd.conf")
	if err := os.WriteFile(confPath, []byte(clientdDefaultConf), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clientd.conf: %w", err)
	}

	// ── 5. Write systemd unit ────────────────────────────────────────────────
	systemdDir := filepath.Join(mountRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir systemd/system: %w", err)
	}
	unitPath := filepath.Join(systemdDir, "clonr-clientd.service")
	if err := os.WriteFile(unitPath, []byte(clientdServiceUnit), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clonr-clientd.service: %w", err)
	}

	// ── 6. Enable the unit via direct symlink ────────────────────────────────
	wantsDir := filepath.Join(mountRoot, "etc", "systemd", "system", "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir multi-user.target.wants: %w", err)
	}
	linkPath := filepath.Join(wantsDir, "clonr-clientd.service")
	const wantTarget = "../clonr-clientd.service"

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
		Msg("finalize: clonr-clientd systemd unit installed and enabled")

	return nil
}
