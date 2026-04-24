package deploy

import (
	"errors"
	"fmt"
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

// injectClientd writes the clonr-clientd configuration and systemd unit into
// the deployed rootfs at mountRoot.
//
// It:
//  1. Writes /etc/clonr/clonrd-url (0644) — the WebSocket endpoint for clientd.
//  2. Writes /etc/clonr/clientd.conf (0644) — default daemon config.
//  3. Writes /etc/systemd/system/clonr-clientd.service (0644).
//  4. Enables the service by creating the WantedBy=multi-user.target symlink.
//
// This is a no-op when clientdURL is empty (caller opts out by leaving it blank).
// Non-fatal: a missing clientd is acceptable — the node will still boot and report
// via verify-boot. Log a warning rather than failing the deploy.
func injectClientd(mountRoot, clientdURL string) error {
	if clientdURL == "" {
		return nil
	}

	log := logger()

	// Ensure /etc/clonr/ exists (created by injectPhoneHome, but be idempotent).
	clonrDir := filepath.Join(mountRoot, "etc", "clonr")
	if err := os.MkdirAll(clonrDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir /etc/clonr: %w", err)
	}

	// ── 1. Write clonrd-url ──────────────────────────────────────────────────
	urlPath := filepath.Join(clonrDir, "clonrd-url")
	if err := os.WriteFile(urlPath, []byte(clientdURL+"\n"), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clonrd-url: %w", err)
	}

	// ── 2. Write clientd.conf ────────────────────────────────────────────────
	confPath := filepath.Join(clonrDir, "clientd.conf")
	if err := os.WriteFile(confPath, []byte(clientdDefaultConf), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clientd.conf: %w", err)
	}

	// ── 3. Write systemd unit ────────────────────────────────────────────────
	systemdDir := filepath.Join(mountRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return fmt.Errorf("clientd inject: mkdir systemd/system: %w", err)
	}
	unitPath := filepath.Join(systemdDir, "clonr-clientd.service")
	if err := os.WriteFile(unitPath, []byte(clientdServiceUnit), 0o644); err != nil {
		return fmt.Errorf("clientd inject: write clonr-clientd.service: %w", err)
	}

	// ── 4. Enable the unit via direct symlink ─────────────────────────────────
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
