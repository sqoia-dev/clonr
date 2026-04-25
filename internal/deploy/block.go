package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/hardware"
)

// BlockDeployer deploys a raw block image directly to a disk.
// It supports two modes:
//   - streaming: pipes the HTTP download directly into dd (no temp file needed)
//   - verified: downloads to a temp file first to compute sha256, then writes
//
// Streaming is used when SkipVerify is true. Verified mode is the default when
// ExpectedChecksum is provided, to avoid writing corrupt data to disk.
type BlockDeployer struct {
	// layout and targetDisk are resolved by Preflight.
	layout     api.DiskLayout
	targetDisk string

	// NodeToken is the node-scoped Bearer token written to /etc/clustr/node-token
	// inside the deployed rootfs during Finalize. ADR-0008.
	// Leave empty to skip phone-home injection (e.g. in tests or non-auto mode).
	NodeToken string

	// VerifyBootURL is the full URL for the verify-boot endpoint, e.g.
	// "http://clustr-server:8080/api/v1/nodes/<nodeID>/verify-boot".
	// Written to /etc/clustr/verify-boot-url inside the deployed rootfs. ADR-0008.
	VerifyBootURL string

	// ClientdURL is the WebSocket URL for clustr-clientd, e.g.
	// "ws://clustr-server:8080/api/v1/nodes/<nodeID>/clientd/ws".
	// Written to /etc/clustr/clustrd-url inside the deployed rootfs.
	// Leave empty to skip clientd injection.
	ClientdURL string

	// ClientdBinPath is the filesystem path to the clustr-clientd binary that
	// is copied into the deployed rootfs at /usr/local/bin/clustr-clientd.
	// Empty means auto-detect via findClientdBin (searches alongside os.Args[0],
	// /opt/clustr/bin/, and /usr/local/bin/).
	ClientdBinPath string
}

// ResolvedDisk returns the target disk path resolved by Preflight.
// Returns "" if Preflight has not been called yet.
func (d *BlockDeployer) ResolvedDisk() string { return d.targetDisk }

// SetPhoneHome implements PhoneHomeInjector. Call before Finalize to enable
// post-reboot verification injection (ADR-0008).
func (d *BlockDeployer) SetPhoneHome(nodeToken, verifyBootURL string) {
	d.NodeToken = nodeToken
	d.VerifyBootURL = verifyBootURL
}

// SetClientdURL implements ClientdInjector. Call before Finalize to enable
// clustr-clientd WebSocket agent injection.
func (d *BlockDeployer) SetClientdURL(clientdURL string) {
	d.ClientdURL = clientdURL
}

// SetClientdBinPath sets the path to the clustr-clientd binary copied into the
// deployed rootfs. Call before Finalize. Empty means auto-detect.
func (d *BlockDeployer) SetClientdBinPath(p string) {
	d.ClientdBinPath = p
}

// Preflight validates that a suitable target disk exists and resolves its path.
func (d *BlockDeployer) Preflight(ctx context.Context, layout api.DiskLayout, hw hardware.SystemInfo) error {
	target, err := selectTargetDisk(layout, hw)
	if err != nil {
		return err
	}

	// Validate disk size and produce an actionable error message.
	diskSize, sizeErr := diskSizeBytes(target)
	if sizeErr == nil {
		needed := totalLayoutBytes(layout)
		if needed > 0 && diskSize < needed {
			return fmt.Errorf("%w: disk %s is too small (%s) — layout requires at least %s minimum",
				ErrPreflightFailed, target,
				humanReadableBytes(diskSize), humanReadableBytes(needed))
		}
	}

	d.layout = layout
	d.targetDisk = target
	return nil
}

// Deploy streams the block image from opts.ImageURL and writes it to the target
// disk. When opts.ExpectedChecksum is set and opts.SkipVerify is false, the blob
// is downloaded to a temp file first for checksum verification before writing.
func (d *BlockDeployer) Deploy(ctx context.Context, opts DeployOpts, progress ProgressFunc) error {
	disk := opts.TargetDisk
	if disk == "" {
		disk = d.targetDisk
	}
	if disk == "" {
		return fmt.Errorf("deploy/block: Preflight must be called before Deploy")
	}

	// ── Rollback setup ────────────────────────────────────────────────────────
	log := logger()
	var rollbackPath string
	if !opts.NoRollback {
		backup, empty, err := backupPartitionTable(disk)
		if err != nil {
			log.Warn().Str("disk", disk).Err(err).Msg("could not back up partition table — proceeding without rollback")
		} else if empty {
			log.Info().Str("disk", disk).Msg("disk has no existing partition table — no rollback possible if deployment fails")
		} else {
			rollbackPath = backup
			log.Info().Str("backup", rollbackPath).Msg("partition table backup saved (will restore on failure)")
		}
	}

	doRollback := func(reason string) {
		if rollbackPath == "" {
			return
		}
		log.Warn().Str("reason", reason).Str("disk", disk).Msg("ROLLBACK triggered — restoring partition table")
		if err := restorePartitionTable(disk, rollbackPath); err != nil {
			log.Error().Err(err).Str("disk", disk).Msg("ROLLBACK FAILED — disk may be in inconsistent state; re-run deployment to recover")
		} else {
			log.Info().Str("disk", disk).Msg("rollback complete — partition table restored to pre-deployment state")
			rollbackPath = ""
		}
	}

	if progress != nil {
		progress(0, 0, "downloading")
	}
	if opts.Reporter != nil {
		opts.Reporter.StartPhase("downloading", 0) // total updated once content-length is known
	}

	var writeErr error
	for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
		if attempt > 1 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			log.Warn().Dur("backoff", backoff).Int("attempt", attempt).Int("max", maxDownloadAttempts).
				Msg("network error downloading image blob — retrying")
			if progress != nil {
				progress(0, 0, fmt.Sprintf("retrying (attempt %d/%d)", attempt, maxDownloadAttempts))
			}
			select {
			case <-ctx.Done():
				doRollback("context cancelled during retry")
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		writeErr = d.attemptBlockWrite(ctx, disk, opts, progress)
		if writeErr == nil {
			break
		}
		if ctx.Err() != nil {
			doRollback("context cancelled during download")
			return ctx.Err()
		}
		log.Warn().Int("attempt", attempt).Int("max", maxDownloadAttempts).Err(writeErr).Msg("block write attempt failed")
	}

	if writeErr != nil {
		doRollback("block write failed after all retries")
		if opts.Reporter != nil {
			opts.Reporter.EndPhase(writeErr.Error())
		}
		return fmt.Errorf("deploy/block: image write failed after %d attempts: %w", maxDownloadAttempts, writeErr)
	}
	if opts.Reporter != nil {
		opts.Reporter.EndPhase("")
	}

	// Deployment succeeded — remove the rollback backup.
	if rollbackPath != "" {
		os.Remove(rollbackPath)
		log.Info().Msg("deployment succeeded — partition table backup removed")
	}

	// Re-read the partition table after writing.
	_ = runAndLog(ctx, "partprobe", exec.CommandContext(ctx, "partprobe", disk))
	_ = runCmd(ctx, "udevadm", "settle")

	return nil
}

// attemptBlockWrite performs a single attempt at downloading and writing the block image.
func (d *BlockDeployer) attemptBlockWrite(ctx context.Context, disk string, opts DeployOpts, progress ProgressFunc) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.ImageURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if opts.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+opts.AuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("network error downloading image blob: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("network error downloading image blob: HTTP %d from %s", resp.StatusCode, opts.ImageURL)
	}

	totalBytes := resp.ContentLength
	needsVerify := !opts.SkipVerify && opts.ExpectedChecksum != ""

	if needsVerify {
		return d.downloadVerifyAndWrite(ctx, resp.Body, totalBytes, disk, opts, progress)
	}

	if opts.SkipVerify && opts.ExpectedChecksum != "" {
		logger().Warn().Msg("checksum verification skipped (--skip-verify set)")
	}
	return d.streamBlockWrite(ctx, resp.Body, totalBytes, disk, opts, progress)
}

// downloadVerifyAndWrite streams the block image directly to disk while
// computing its sha256 checksum, then verifies the checksum after the write
// completes. No temp file is created, so RAM usage is bounded by the copy
// buffer regardless of image size.
func (d *BlockDeployer) downloadVerifyAndWrite(ctx context.Context, body io.Reader, totalBytes int64, disk string, opts DeployOpts, progress ProgressFunc) error {
	// Open the target disk before starting the download so we fail fast on
	// permission or device errors without wasting bandwidth.
	f, err := os.OpenFile(disk, os.O_WRONLY|os.O_SYNC, 0o600)
	if err != nil {
		return fmt.Errorf("open disk %s: %w", disk, err)
	}
	defer f.Close()

	// Tee the download body through the hasher so we compute the checksum in
	// a single streaming pass — no temp file, no second read.
	hasher := sha256.New()
	tee := io.TeeReader(body, hasher)

	if opts.Reporter != nil {
		opts.Reporter.StartPhase("downloading", totalBytes)
	}
	pr := &progressReader{r: tee, total: totalBytes, fn: progress, phase: "downloading", reporter: opts.Reporter}

	buf := make([]byte, 4*1024*1024)
	if _, err := io.CopyBuffer(f, pr, buf); err != nil {
		return fmt.Errorf("write to %s: %w", disk, err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync disk %s: %w", disk, err)
	}

	// Verify checksum after the write is durable.
	gotChecksum := hex.EncodeToString(hasher.Sum(nil))
	if gotChecksum != opts.ExpectedChecksum {
		return fmt.Errorf("image integrity check failed: written blob sha256=%s does not match "+
			"expected=%s — the image may be corrupt or the server checksum is stale; "+
			"use --skip-verify to deploy anyway", gotChecksum, opts.ExpectedChecksum)
	}
	logger().Info().Str("sha256", gotChecksum).Msg("image checksum verified")

	return nil
}

// streamBlockWrite streams the download directly to disk without checksum verification.
func (d *BlockDeployer) streamBlockWrite(ctx context.Context, body io.Reader, totalBytes int64, disk string, opts DeployOpts, progress ProgressFunc) error {
	f, err := os.OpenFile(disk, os.O_WRONLY|os.O_SYNC, 0o600)
	if err != nil {
		return fmt.Errorf("open disk %s: %w", disk, err)
	}
	defer f.Close()

	// Update downloading phase total now that we know the content-length.
	if opts.Reporter != nil {
		opts.Reporter.StartPhase("downloading", totalBytes)
	}
	pr := &progressReader{r: body, total: totalBytes, fn: progress, phase: "writing", reporter: opts.Reporter}
	buf := make([]byte, 4*1024*1024)
	if _, err := io.CopyBuffer(f, pr, buf); err != nil {
		return fmt.Errorf("write to %s: %w", disk, err)
	}

	return f.Sync()
}

// Finalize applies node-specific configuration to the deployed filesystem.
// For block images, the partitions must be mounted first. This method mounts
// the root partition at mountRoot, applies config, then unmounts.
func (d *BlockDeployer) Finalize(ctx context.Context, cfg api.NodeConfig, mountRoot string) error {
	if mountRoot == "" {
		return fmt.Errorf("deploy/block: mountRoot is required for Finalize")
	}

	// Mount the root partition (first partition with mountpoint "/").
	rootDev := ""
	for i, p := range d.layout.Partitions {
		if p.MountPoint == "/" {
			rootDev = partitionDevice(d.targetDisk, i+1)
			break
		}
	}
	if rootDev == "" && len(d.layout.Partitions) > 0 {
		// Fall back to first partition if no explicit "/" mountpoint.
		rootDev = partitionDevice(d.targetDisk, 1)
	}
	if rootDev == "" {
		return fmt.Errorf("deploy/block: cannot determine root partition for Finalize")
	}

	if err := os.MkdirAll(mountRoot, 0o755); err != nil {
		return fmt.Errorf("deploy/block: mkdir mountRoot: %w", err)
	}
	if err := runCmd(ctx, "mount", rootDev, mountRoot); err != nil {
		return fmt.Errorf("deploy/block: mount root %s: %w", rootDev, err)
	}
	defer func() {
		_ = exec.Command("umount", mountRoot).Run()
	}()

	if err := applyNodeConfig(ctx, cfg, mountRoot); err != nil {
		return err
	}

	// ── Post-write integrity spot-check ──────────────────────────────────────
	if err := verifyBlockSpotCheck(mountRoot); err != nil {
		return fmt.Errorf("deploy/block: finalize: integrity check: %w", err)
	}

	// ── Phone-home injection (ADR-0008) ──────────────────────────────────────
	if err := injectPhoneHome(mountRoot, d.NodeToken, d.VerifyBootURL); err != nil {
		return fmt.Errorf("deploy/block: finalize: phone-home injection: %w", err)
	}

	// ── clustr-clientd injection ───────────────────────────────────────────────
	// Non-fatal: clientd missing means no live heartbeat, but the node boots fine.
	if err := injectClientd(mountRoot, d.ClientdURL, d.ClientdBinPath); err != nil {
		logger().Warn().Err(err).Msg("WARNING: finalize: clientd injection failed (non-fatal)")
	}

	return nil
}

// verifyBlockSpotCheck does a basic integrity check on the deployed block image
// by verifying presence of key files in the mounted filesystem.
func verifyBlockSpotCheck(mountRoot string) error {
	criticalPaths := []string{
		"/etc/hostname",
		"/etc/fstab",
		"/sbin/init",
	}
	var missing []string
	for _, p := range criticalPaths {
		if _, err := os.Stat(mountRoot + p); os.IsNotExist(err) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("deployed block image is missing critical files: %v — "+
			"the image may be corrupt or the deployment was incomplete", missing)
	}
	return nil
}
