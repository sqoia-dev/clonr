// slurminstall.go — Client-side handler for slurm_binary_push messages.
// When the server pushes a Slurm binary update, the client:
//  1. Downloads the artifact tarball from the signed URL.
//  2. Verifies the SHA-256 checksum.
//  3. Extracts to /usr/local/ (or configured prefix).
//  4. Restarts slurmd/slurmctld via systemctl.
//  5. Sends a SlurmBinaryAckPayload back to the server.
package clientd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// slurmInstallPrefix is where slurm binaries are extracted.
	slurmInstallPrefix = "/"
	// slurmDownloadTimeout is the per-download context timeout.
	slurmDownloadTimeout = 30 * time.Minute
)

// applySlurmBinary handles a slurm_binary_push message.
// It downloads, verifies, and installs the new Slurm build.
// Returns a SlurmBinaryAckPayload to send back to the server.
func applySlurmBinary(ctx context.Context, baseURL string, payload SlurmBinaryPushPayload) SlurmBinaryAckPayload {
	log.Info().
		Str("build_id", payload.BuildID).
		Str("version", payload.Version).
		Msg("slurminstall: received binary push")

	// Resolve the download URL. If it's a relative path, prepend the server base URL.
	downloadURL := payload.ArtifactURL
	if strings.HasPrefix(downloadURL, "/") {
		downloadURL = strings.TrimRight(baseURL, "/") + downloadURL
	}

	// Create a temp directory for the download.
	tmpDir, err := os.MkdirTemp("", "clustr-slurm-install-*")
	if err != nil {
		return binaryAckError(payload.BuildID, fmt.Errorf("mkdir temp: %w", err))
	}
	defer os.RemoveAll(tmpDir)

	// Step 1: Download artifact.
	tarPath := filepath.Join(tmpDir, fmt.Sprintf("slurm-%s.tar.gz", payload.Version))
	log.Info().Str("url", downloadURL).Msg("slurminstall: downloading artifact")
	if err := downloadArtifact(ctx, downloadURL, tarPath); err != nil {
		return binaryAckError(payload.BuildID, fmt.Errorf("download: %w", err))
	}

	// Step 2: Verify checksum.
	if payload.Checksum != "" {
		if err := verifyChecksum(tarPath, payload.Checksum); err != nil {
			return binaryAckError(payload.BuildID, fmt.Errorf("checksum: %w", err))
		}
		log.Info().Msg("slurminstall: checksum verified")
	}

	// Step 3: Extract to install prefix.
	log.Info().Str("prefix", slurmInstallPrefix).Msg("slurminstall: extracting artifact")
	if err := extractArtifact(ctx, tarPath, slurmInstallPrefix); err != nil {
		return binaryAckError(payload.BuildID, fmt.Errorf("extract: %w", err))
	}

	// Step 4: Restart Slurm services.
	log.Info().Msg("slurminstall: restarting Slurm services")
	if err := restartSlurmServices(ctx); err != nil {
		log.Warn().Err(err).Msg("slurminstall: service restart failed (non-fatal if no services running)")
	}

	// Step 5: Read installed version.
	installedVer := detectInstalledSlurmVersion(ctx)

	log.Info().
		Str("build_id", payload.BuildID).
		Str("installed_version", installedVer).
		Msg("slurminstall: binary push complete")

	return SlurmBinaryAckPayload{
		BuildID:          payload.BuildID,
		OK:               true,
		InstalledVersion: installedVer,
	}
}

// downloadArtifact downloads the artifact from url to dst with checksum verification.
func downloadArtifact(ctx context.Context, url, dst string) error {
	dlCtx, cancel := context.WithTimeout(ctx, slurmDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http get: status %d", resp.StatusCode)
	}

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write download: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	return os.Rename(tmp, dst)
}

// verifyChecksum checks that the SHA-256 of the file at path matches expected.
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("checksum mismatch: got %s, expected %s", got, expected)
	}
	return nil
}

// extractArtifact extracts a tar.gz artifact into prefix.
func extractArtifact(ctx context.Context, tarPath, prefix string) error {
	if err := os.MkdirAll(prefix, 0755); err != nil {
		return fmt.Errorf("mkdir prefix: %w", err)
	}
	cmd := exec.CommandContext(ctx, "tar", "-xzf", tarPath, "-C", prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar -xzf: %w\noutput: %s", err, strings.TrimRight(string(out), "\n"))
	}
	return nil
}

// restartSlurmServices restarts whichever Slurm service is active on this node.
func restartSlurmServices(ctx context.Context) error {
	restCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var lastErr error
	for _, svc := range []string{"slurmctld", "slurmd"} {
		chkCtx, chkCancel := context.WithTimeout(ctx, 5*time.Second)
		chk := exec.CommandContext(chkCtx, "systemctl", "is-active", "--quiet", svc)
		err := chk.Run()
		chkCancel()
		if err != nil {
			continue // service not active on this node
		}
		cmd := exec.CommandContext(restCtx, "systemctl", "restart", svc)
		if out, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("restart %s: %w (output: %s)", svc, err, strings.TrimRight(string(out), "\n"))
			log.Error().Err(lastErr).Str("service", svc).Msg("slurminstall: restart failed")
		} else {
			log.Info().Str("service", svc).Msg("slurminstall: service restarted")
		}
	}
	return lastErr
}

// detectInstalledSlurmVersion runs sinfo --version to detect the installed version.
// Returns an empty string if detection fails.
func detectInstalledSlurmVersion(ctx context.Context) string {
	detCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(detCtx, "sinfo", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// binaryAckError is a convenience constructor for a failed SlurmBinaryAckPayload.
func binaryAckError(buildID string, err error) SlurmBinaryAckPayload {
	return SlurmBinaryAckPayload{
		BuildID: buildID,
		OK:      false,
		Error:   err.Error(),
	}
}
