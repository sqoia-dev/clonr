package clientd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// configTarget describes a whitelisted config file and the optional post-write action.
type configTarget struct {
	// relPath is the file path relative to the filesystem root (no leading slash).
	relPath     string
	mode        os.FileMode  // permissions for the written file
	applyAction func() error // nil = no restart needed; always nil for non-live roots
}

// configTargets is the whitelist of supported config push targets.
// Only targets listed here may be written by a config_push message.
// Paths are relative to the filesystem root so they compose cleanly with any rootDir.
var configTargets = map[string]configTarget{
	// Sprint 36 Day 2: hostname target for the reactive-config observer push.
	// No restart needed — /etc/hostname is read at boot; live hostname is not
	// changed by this write (that's a separate sysctl/hostnamectl concern).
	"hostname": {relPath: "etc/hostname", mode: 0644, applyAction: nil},
	"hosts":    {relPath: "etc/hosts", mode: 0644, applyAction: nil},
	"sssd":     {relPath: "etc/sssd/sssd.conf", mode: 0600, applyAction: restartService("sssd")},
	// Sprint 36 Day 3: limits plugin — anchored writes to /etc/security/limits.conf.
	// No restart needed: PAM reads limits.conf on each login, so changes take effect
	// at next session open without requiring any service restart.
	"limits": {relPath: "etc/security/limits.conf", mode: 0644, applyAction: nil},
	"chrony": {relPath: "etc/chrony.conf", mode: 0644, applyAction: restartService("chronyd")},
	"ntp":    {relPath: "etc/ntp.conf", mode: 0644, applyAction: restartService("ntpd")},
	"resolv": {relPath: "etc/resolv.conf", mode: 0644, applyAction: nil},
	// sudoers: sudo re-reads drop-ins on every invocation — no restart needed.
	"sudoers": {relPath: "etc/sudoers.d/clustr-admins", mode: 0440, applyAction: nil},
	// clustr-internal-repo: pushed by server after InitRepoGPGKey or on node join.
	// The apply action runs "dnf clean metadata" so the node picks up the new/updated repo.
	"clustr-internal-repo": {relPath: "etc/yum.repos.d/clustr-internal-repo.repo", mode: 0644, applyAction: dnfCleanMetadata()},
	// ldap-ca-cert: pushed by the server whenever the LDAP CA is rotated (e.g. after
	// Disable+wipe+Enable). Writing to the system anchor dir is root-only, so the
	// apply action calls clustr-privhelper ca-trust-extract then restarts sssd so
	// the new CA is picked up immediately.
	"ldap-ca-cert": {
		relPath:     "etc/pki/ca-trust/source/anchors/clustr-ca.crt",
		mode:        0644,
		applyAction: caTrustExtractThenRestartSSSD(),
	},
	// sshd-ldap-keys: the sshd_config.d drop-in that wires AuthorizedKeysCommand to
	// sss_ssh_authorizedkeys (GAP-104a-4). Written during initial deploy by
	// writeLDAPConfig; pushed via fanout to already-deployed nodes after a CA rotation
	// so LDAP SSH pubkey auth works without a full reimage. sshd is reloaded (not
	// restarted) so active SSH sessions are not dropped.
	"sshd-ldap-keys": {
		relPath:     "etc/ssh/sshd_config.d/50-clustr-ldap-keys.conf",
		mode:        0644,
		applyAction: reloadService("sshd"),
	},
}

const maxConfigSizeBytes = 1 << 20 // 1 MB

// ConfigApplier writes config-push payloads to a target filesystem root.
// rootDir "/" targets the live running system; any other path (e.g. "/mnt/target")
// targets a mounted filesystem — useful for pre-boot in-chroot reconfiguration.
//
// When rootDir is not "/", applyAction callbacks (service restarts, dnf clean,
// ca-trust-extract) are suppressed: those operations are meaningless against a
// non-running filesystem and would fail or produce side-effects on the deploy host.
type ConfigApplier struct {
	rootDir string
}

// NewConfigApplier creates a ConfigApplier that writes to rootDir.
// Use "/" for the live running system (identical behaviour to the legacy applyConfig).
// Use "/mnt/target" (or any tmpdir) for pre-boot / test writes.
func NewConfigApplier(rootDir string) *ConfigApplier {
	return &ConfigApplier{rootDir: rootDir}
}

// path returns the absolute path for relPath within rootDir.
// relPath must not have a leading slash (e.g. "etc/hostname").
func (ca *ConfigApplier) path(relPath string) string {
	return filepath.Join(ca.rootDir, relPath)
}

// isLiveRoot reports whether this applier targets the live running root.
// When false, applyAction callbacks are skipped.
func (ca *ConfigApplier) isLiveRoot() bool {
	return ca.rootDir == "/"
}

// ApplyOne validates, writes, and (if live-root) applies a config push payload
// atomically. It backs up the existing file, writes to a .tmp path, renames into
// place, sets correct permissions, and runs the optional apply action (service
// restart) when targeting the live root. On restart failure it restores the backup
// and retries the restart once.
func (ca *ConfigApplier) ApplyOne(payload ConfigPushPayload) error {
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

	absPath := ca.path(target.relPath)
	bakPath := absPath + ".bak"
	tmpPath := absPath + ".tmp"

	// Ensure parent directory exists. This is important for in-chroot targets
	// where the directory may not have been created by the image (e.g. /etc/sssd/).
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("config push: mkdir parent %s: %w", filepath.Dir(absPath), err)
	}

	// Back up the existing file (copy, not rename, to preserve original permissions and ownership).
	if err := copyFile(absPath, bakPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("config push: backup %s → %s: %w", absPath, bakPath, err)
	}

	// Write content to .tmp then atomically rename into place.
	if err := os.WriteFile(tmpPath, []byte(payload.Content), target.mode); err != nil {
		return fmt.Errorf("config push: write temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, target.mode); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("config push: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("config push: rename %s → %s: %w", tmpPath, absPath, err)
	}

	// Run post-write apply action (e.g. service restart) only when targeting the
	// live root. In-chroot writes suppress actions: service restarts and dnf clean
	// are meaningless against a non-running filesystem and would fail or corrupt
	// the deploy host state.
	if target.applyAction == nil || !ca.isLiveRoot() {
		return nil
	}

	if err := target.applyAction(); err != nil {
		// Restart failed — attempt rollback.
		if bakErr := copyFile(bakPath, absPath); bakErr != nil {
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

// applyConfig is the legacy entry point used by the live config_push handler.
// It wraps NewConfigApplier("/").ApplyOne for backward compatibility.
func applyConfig(payload ConfigPushPayload) error {
	return NewConfigApplier("/").ApplyOne(payload)
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

// reloadService returns an apply action that runs `systemctl reload <name>`.
// Prefer reload over restart when a graceful in-place config reload is sufficient
// (e.g. sshd: picks up sshd_config.d changes without dropping active sessions).
func reloadService(name string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "systemctl", "reload", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl reload %s: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// dnfCleanMetadata returns an apply action that runs "dnf clean metadata" so the
// node re-indexes yum repos after the repo file is written.  Non-fatal: if dnf
// is not available (not an RPM node) the clean is skipped silently.
func dnfCleanMetadata() func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "dnf", "clean", "metadata")
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Non-fatal — clean is advisory; the repo file is already written.
			// Print to stderr so it shows up in clustr-clientd's journald output.
			fmt.Fprintf(os.Stderr, "config push: dnf clean metadata: %v (output: %s)\n", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// caTrustExtractThenRestartSSSD returns an apply action that:
// 1. Runs `update-ca-trust extract` to rebuild the system trust bundle.
// 2. Runs `systemctl restart sssd` to pick up the new CA.
// clustr-clientd runs as root on nodes, so no privhelper indirection is needed here.
func caTrustExtractThenRestartSSSD() func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Step 1: rebuild system trust bundle.
		extractCmd := exec.CommandContext(ctx, "update-ca-trust", "extract")
		if out, err := extractCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("config push: update-ca-trust extract: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}

		// Step 2: restart sssd so it loads the refreshed trust bundle.
		restartCtx, restartCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer restartCancel()
		restartCmd := exec.CommandContext(restartCtx, "systemctl", "restart", "sssd")
		if out, err := restartCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("config push: sssd restart after ca-trust-extract: %w (output: %s)", err, strings.TrimSpace(string(out)))
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
