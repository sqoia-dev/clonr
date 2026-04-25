package deploy

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed embedded/clustr-verify-boot.service
var verifyBootServiceUnit []byte

//go:embed embedded/clustr-verify-boot.sh
var verifyBootScript []byte

// injectPhoneHome writes the post-reboot verification phone-home components into
// the deployed rootfs at mountRoot. ADR-0008.
//
// It:
//  1. Creates /etc/clustr/ (0755) in the chroot.
//  2. Writes /etc/clustr/node-token (0600) with nodeToken.
//  3. Writes /etc/clustr/verify-boot-url (0644) with verifyBootURL.
//  4. Writes /etc/systemd/system/clustr-verify-boot.service from the embedded unit.
//  5. Writes /usr/local/bin/clustr-verify-boot from the embedded shell script (0755).
//  6. Creates the WantedBy=multi-user.target symlink directly inside the chroot
//     (equivalent to `systemctl --root enable`, but without requiring systemctl).
//
// Returns a fatal error on any write or enable failure — the caller must treat this
// as a hard error and surface ExitFinalize so the deploy is not falsely reported as
// succeeded without a phone-home path in place.
//
// If nodeToken or verifyBootURL is empty, injectPhoneHome is a no-op (the caller
// opts out of phone-home injection by leaving these fields blank).
func injectPhoneHome(mountRoot, nodeToken, verifyBootURL string) error {
	if nodeToken == "" || verifyBootURL == "" {
		return nil
	}

	log := logger()

	// ── 1. Create /etc/clustr/ ────────────────────────────────────────────────
	clustrDir := filepath.Join(mountRoot, "etc", "clustr")
	if err := os.MkdirAll(clustrDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir /etc/clustr: %w", err)
	}

	// ── 2. Write node-token (0600) ───────────────────────────────────────────
	tokenPath := filepath.Join(clustrDir, "node-token")
	if err := os.WriteFile(tokenPath, []byte(nodeToken), 0o600); err != nil {
		return fmt.Errorf("phonehome: write node-token: %w", err)
	}

	// ── 3. Write verify-boot-url (0644) ─────────────────────────────────────
	urlPath := filepath.Join(clustrDir, "verify-boot-url")
	if err := os.WriteFile(urlPath, []byte(verifyBootURL), 0o644); err != nil {
		return fmt.Errorf("phonehome: write verify-boot-url: %w", err)
	}

	// ── 4. Write systemd unit file ──────────────────────────────────────────
	systemdDir := filepath.Join(mountRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir systemd/system: %w", err)
	}
	unitPath := filepath.Join(systemdDir, "clustr-verify-boot.service")
	if err := os.WriteFile(unitPath, verifyBootServiceUnit, 0o644); err != nil {
		return fmt.Errorf("phonehome: write clustr-verify-boot.service: %w", err)
	}

	// ── 5. Write verify-boot script (0755) ──────────────────────────────────
	binDir := filepath.Join(mountRoot, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir usr/local/bin: %w", err)
	}
	scriptPath := filepath.Join(binDir, "clustr-verify-boot")
	if err := os.WriteFile(scriptPath, verifyBootScript, 0o755); err != nil {
		return fmt.Errorf("phonehome: write clustr-verify-boot script: %w", err)
	}

	// ── 6. Enable the unit via direct symlink ────────────────────────────────
	// `systemctl --root enable` is not available in the initramfs environment
	// (systemctl is not staged in build-initramfs.sh). We replicate the exact
	// action systemctl would take: create the WantedBy symlink directly.
	// The unit declares WantedBy=multi-user.target, so the symlink target is
	// ../clustr-verify-boot.service (relative to the wants directory).
	wantsDir := filepath.Join(mountRoot, "etc", "systemd", "system", "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir multi-user.target.wants: %w", err)
	}
	linkPath := filepath.Join(wantsDir, "clustr-verify-boot.service")
	const wantTarget = "../clustr-verify-boot.service"

	// Idempotent: if a symlink already exists with the correct target, nothing
	// to do. If the path exists as anything else (stale symlink with wrong
	// target, regular file from a previous broken deploy), remove it.
	if existing, lstatErr := os.Lstat(linkPath); lstatErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			// It's a symlink — check the target.
			if target, rErr := os.Readlink(linkPath); rErr == nil && target == wantTarget {
				// Already correct — skip symlink creation.
				goto symlinkDone
			}
		}
		// Exists but wrong type or wrong target — remove it.
		if rmErr := os.Remove(linkPath); rmErr != nil {
			return fmt.Errorf("phonehome: remove stale wants entry %s: %w", linkPath, rmErr)
		}
	}
	if err := os.Symlink(wantTarget, linkPath); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("phonehome: create WantedBy symlink: %w", err)
	}
symlinkDone:

	log.Info().
		Str("mountRoot", mountRoot).
		Str("unitPath", unitPath).
		Msg("finalize: phone-home systemd unit enabled — node will verify boot on first userspace reach")

	return nil
}
