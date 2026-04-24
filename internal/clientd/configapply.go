package clientd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// configTarget describes a whitelisted config file and the optional post-write action.
type configTarget struct {
	path        string
	mode        os.FileMode // permissions for the written file
	applyAction func() error // nil = no restart needed
}

// configTargets is the whitelist of supported config push targets.
// Only targets listed here may be written by a config_push message.
var configTargets = map[string]configTarget{
	"hosts":   {path: "/etc/hosts", mode: 0644, applyAction: nil},
	"sssd":    {path: "/etc/sssd/sssd.conf", mode: 0600, applyAction: restartService("sssd")},
	"chrony":  {path: "/etc/chrony.conf", mode: 0644, applyAction: restartService("chronyd")},
	"ntp":     {path: "/etc/ntp.conf", mode: 0644, applyAction: restartService("ntpd")},
	"resolv":  {path: "/etc/resolv.conf", mode: 0644, applyAction: nil},
	// sudoers: sudo re-reads drop-ins on every invocation — no restart needed.
	"sudoers": {path: "/etc/sudoers.d/clonr-admins", mode: 0440, applyAction: nil},
}

const maxConfigSizeBytes = 1 << 20 // 1 MB

// applyConfig validates, writes, and applies a config push payload atomically.
// It backs up the existing file, writes to a .tmp path, renames into place,
// sets correct permissions, and runs the optional apply action (service restart).
// On restart failure it restores the backup and retries the restart once.
func applyConfig(payload ConfigPushPayload) error {
	target, ok := configTargets[payload.Target]
	if !ok {
		return fmt.Errorf("config push: unknown target %q — not in whitelist", payload.Target)
	}

	if len(payload.Content) > maxConfigSizeBytes {
		return fmt.Errorf("config push: content size %d exceeds 1 MB limit", len(payload.Content))
	}

	if err := validateChecksum(payload.Content, payload.Checksum); err != nil {
		return err
	}

	bakPath := target.path + ".bak"
	tmpPath := target.path + ".tmp"

	// Back up the existing file (copy, not rename, to preserve original permissions and ownership).
	if err := copyFile(target.path, bakPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config push: backup %s → %s: %w", target.path, bakPath, err)
	}

	// Write content to .tmp then atomically rename into place.
	if err := os.WriteFile(tmpPath, []byte(payload.Content), target.mode); err != nil {
		return fmt.Errorf("config push: write temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, target.mode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("config push: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, target.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("config push: rename %s → %s: %w", tmpPath, target.path, err)
	}

	// Run post-write apply action (e.g. service restart) if configured.
	if target.applyAction == nil {
		return nil
	}

	if err := target.applyAction(); err != nil {
		// Restart failed — attempt rollback.
		if bakErr := copyFile(bakPath, target.path); bakErr != nil {
			return fmt.Errorf("config push: apply action failed (%w) and rollback also failed (%v)", err, bakErr)
		}
		// Retry the restart after restoring the old config.
		if retryErr := target.applyAction(); retryErr != nil {
			return fmt.Errorf("config push: apply action failed after rollback (%w); service may be degraded", retryErr)
		}
		return fmt.Errorf("config push: apply action failed, rolled back to previous config: %w", err)
	}

	return nil
}

// validateChecksum checks that sha256(content) matches the "sha256:<hex>" checksum field.
func validateChecksum(content, checksum string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(checksum, prefix) {
		return fmt.Errorf("config push: checksum must start with %q, got %q", prefix, checksum)
	}
	want := strings.TrimPrefix(checksum, prefix)
	sum := sha256.Sum256([]byte(content))
	got := fmt.Sprintf("%x", sum)
	if got != want {
		return fmt.Errorf("config push: checksum mismatch (expected sha256:%s, computed sha256:%s)", want, got)
	}
	return nil
}

// restartService returns an apply action that runs `systemctl restart <name>`.
func restartService(name string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "systemctl", "restart", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart %s: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// copyFile copies src to dst, preserving content. If src does not exist, returns os.ErrNotExist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	stat, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, stat.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
