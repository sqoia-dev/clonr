package deploy

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed embedded/clonr-verify-boot.service
var verifyBootServiceUnit []byte

//go:embed embedded/clonr-verify-boot.sh
var verifyBootScript []byte

// injectPhoneHome writes the post-reboot verification phone-home components into
// the deployed rootfs at mountRoot. ADR-0008.
//
// It:
//  1. Creates /etc/clonr/ (0755) in the chroot.
//  2. Writes /etc/clonr/node-token (0600) with nodeToken.
//  3. Writes /etc/clonr/verify-boot-url (0644) with verifyBootURL.
//  4. Writes /etc/systemd/system/clonr-verify-boot.service from the embedded unit.
//  5. Writes /usr/local/bin/clonr-verify-boot from the embedded shell script (0755).
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

	// ── 1. Create /etc/clonr/ ────────────────────────────────────────────────
	clonrDir := filepath.Join(mountRoot, "etc", "clonr")
	if err := os.MkdirAll(clonrDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir /etc/clonr: %w", err)
	}

	// ── 2. Write node-token (0600) ───────────────────────────────────────────
	tokenPath := filepath.Join(clonrDir, "node-token")
	if err := os.WriteFile(tokenPath, []byte(nodeToken), 0o600); err != nil {
		return fmt.Errorf("phonehome: write node-token: %w", err)
	}

	// ── 3. Write verify-boot-url (0644) ─────────────────────────────────────
	urlPath := filepath.Join(clonrDir, "verify-boot-url")
	if err := os.WriteFile(urlPath, []byte(verifyBootURL), 0o644); err != nil {
		return fmt.Errorf("phonehome: write verify-boot-url: %w", err)
	}

	// ── 4. Write systemd unit file ──────────────────────────────────────────
	systemdDir := filepath.Join(mountRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir systemd/system: %w", err)
	}
	unitPath := filepath.Join(systemdDir, "clonr-verify-boot.service")
	if err := os.WriteFile(unitPath, verifyBootServiceUnit, 0o644); err != nil {
		return fmt.Errorf("phonehome: write clonr-verify-boot.service: %w", err)
	}

	// ── 5. Write verify-boot script (0755) ──────────────────────────────────
	binDir := filepath.Join(mountRoot, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir usr/local/bin: %w", err)
	}
	scriptPath := filepath.Join(binDir, "clonr-verify-boot")
	if err := os.WriteFile(scriptPath, verifyBootScript, 0o755); err != nil {
		return fmt.Errorf("phonehome: write clonr-verify-boot script: %w", err)
	}

	// ── 6. Enable the unit via direct symlink ────────────────────────────────
	// `systemctl --root enable` is not available in the initramfs environment
	// (systemctl is not staged in build-initramfs.sh). We replicate the exact
	// action systemctl would take: create the WantedBy symlink directly.
	// The unit declares WantedBy=multi-user.target, so the symlink target is
	// ../clonr-verify-boot.service (relative to the wants directory).
	wantsDir := filepath.Join(mountRoot, "etc", "systemd", "system", "multi-user.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("phonehome: mkdir multi-user.target.wants: %w", err)
	}
	linkPath := filepath.Join(wantsDir, "clonr-verify-boot.service")
	_ = os.Remove(linkPath) // idempotent — remove stale link if present
	if err := os.Symlink("../clonr-verify-boot.service", linkPath); err != nil {
		return fmt.Errorf("phonehome: create WantedBy symlink: %w", err)
	}

	log.Info().
		Str("mountRoot", mountRoot).
		Str("unitPath", unitPath).
		Msg("finalize: phone-home systemd unit enabled — node will verify boot on first userspace reach")

	return nil
}
