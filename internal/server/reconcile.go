package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ReconcileStuckBuilds finds all images in "building" state in the database and
// determines whether they have a real build process behind them. Since build
// goroutines are in-process, any "building" image after a server restart has no
// corresponding goroutine — it will never progress. This pass marks them
// interrupted and resumable (Feature F3) rather than hard-failing them.
//
// Decision logic per image:
//  1. If build-state.json shows the last known phase, set resume_from_phase to
//     that phase so the resume endpoint can re-enter at the right point.
//  2. If <imagedir>/<id>/rootfs/ exists and is non-empty, the installer finished
//     but the process died during finalization — mark resumable at "finalizing".
//  3. All other cases — mark interrupted/resumable at "downloading_iso" so the
//     operator can resume from scratch via the Resume button.
//
// Call this once at startup, after db.Open() and before ListenAndServe().
func (s *Server) ReconcileStuckBuilds(ctx context.Context) error {
	images, err := s.db.ListBaseImages(ctx, string(api.ImageStatusBuilding), "")
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return nil
	}

	reconciled := 0
	for _, img := range images {
		// Initramfs artifacts (build_method="initramfs") are created by
		// BuildInitramfsFromImage as a side-effect of the initramfs build.
		// They are not resumable through the phase-based resume mechanism;
		// marking them interrupted would show a misleading "Resume" button in
		// the UI. Mark them error instead so the operator knows to re-run the
		// initramfs build.
		if img.BuildMethod == "initramfs" {
			log.Warn().
				Str("image_id", img.ID).
				Str("image_name", img.Name).
				Msg("reconcile: initramfs artifact stuck in building — marking error")
			if dbErr := s.db.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusError, "initramfs build was interrupted by server restart"); dbErr != nil {
				log.Error().Err(dbErr).Str("image_id", img.ID).
					Msg("reconcile: failed to mark initramfs artifact as error")
			}
			continue
		}

		phase := s.classifyStuckPhase(img.ID)
		log.Warn().
			Str("image_id", img.ID).
			Str("image_name", img.Name).
			Str("resume_from_phase", phase).
			Msg("reconcile: marking stuck build as interrupted/resumable")

		if err := s.db.SetImageResumable(ctx, img.ID, phase); err != nil {
			log.Error().Err(err).Str("image_id", img.ID).
				Msg("reconcile: failed to set image resumable")
			continue
		}

		// Synthesise an interrupted entry in the in-memory BuildProgressStore so that
		// any UI client that happens to poll immediately after startup gets a
		// sensible response instead of a 404.
		h := s.buildProgress.Start(img.ID)
		h.Fail("build interrupted — server was restarted; use Resume to continue")

		reconciled++
	}

	if reconciled > 0 {
		log.Info().Int("count", reconciled).Msg("reconcile: marked stuck builds as interrupted/resumable")
	}
	return nil
}

// ReconcileStuckInitramfsBuilds inspects every initramfs_builds row still in
// 'pending' state (orphaned by a server restart during a build) and attempts to
// self-heal those whose staging artifact is intact:
//
//   - If the staging file exists and is non-empty: compute its SHA256, promote it
//     to the live path, and mark the build "success (recovered after restart)".
//   - If the staging file is empty: remove it and mark the build failed.
//   - If the staging file is absent: mark the build failed.
//
// This allows a build that completed its script run but whose handler was preempted
// before the final DB write to self-heal on the next startup.
//
// Call this once at startup, after ReconcileStuckBuilds.
func (s *Server) ReconcileStuckInitramfsBuilds(ctx context.Context) error {
	pending, err := s.db.ListPendingInitramfsBuilds(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	bootDir := s.cfg.PXE.BootDir
	livePath := filepath.Join(bootDir, "initramfs-clustr.img")

	healed := 0
	failed := 0
	for _, build := range pending {
		shortID := build.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		stagingPath := filepath.Join(bootDir, "initramfs-clustr.img.build-"+shortID)

		info, statErr := os.Stat(stagingPath)
		if statErr != nil {
			// No artifact — definitely failed.
			log.Warn().
				Str("build_id", build.ID).
				Msg("reconcile: pending initramfs build has no staging artifact — marking failed")
			finishInitramfsBuild(ctx, s, build.ID, "", 0, "", "failed: server restarted during build")
			failed++
			continue
		}

		if info.Size() == 0 {
			// Empty artifact — failed mid-write.
			os.Remove(stagingPath) //nolint:errcheck
			log.Warn().
				Str("build_id", build.ID).
				Msg("reconcile: pending initramfs build has empty staging artifact — marking failed")
			finishInitramfsBuild(ctx, s, build.ID, "", 0, "", "failed: empty artifact after restart")
			failed++
			continue
		}

		// Check for the .modules.manifest sidecar written by build-initramfs.sh
		// after dracut completes. Its presence proves the script ran to completion
		// (past the module-copy and dracut phases). Without it, the staging file
		// may be a large-but-incomplete artifact from a mid-build crash.
		// BUG-17: this check was missing; the reconciler was promoting incomplete
		// artifacts and marking them success, which produced a corrupt initramfs.
		manifestPath := stagingPath + ".modules.manifest"
		if _, manifestErr := os.Stat(manifestPath); manifestErr != nil {
			// Manifest absent — script did not complete past dracut.
			log.Warn().
				Str("build_id", build.ID).
				Str("staging_path", stagingPath).
				Msg("reconcile: pending initramfs build missing .modules.manifest sidecar — build incomplete, marking failed")
			finishInitramfsBuild(ctx, s, build.ID, "", 0, "", "failed: build artifact incomplete (no modules manifest)")
			failed++
			continue
		}

		// Artifact is present, non-empty, and has its manifest sidecar —
		// compute SHA256 and promote to the live path.
		sha, hashErr := computeReconcileSHA256(stagingPath)
		if hashErr != nil {
			log.Warn().Err(hashErr).
				Str("build_id", build.ID).
				Msg("reconcile: failed to hash staging artifact — marking failed")
			finishInitramfsBuild(ctx, s, build.ID, "", 0, "", "failed: hash on recovery: "+hashErr.Error())
			failed++
			continue
		}

		if renameErr := os.Rename(stagingPath, livePath); renameErr != nil {
			log.Warn().Err(renameErr).
				Str("build_id", build.ID).
				Msg("reconcile: failed to promote staging artifact — marking failed")
			finishInitramfsBuild(ctx, s, build.ID, sha, info.Size(), "", "failed: rename on recovery: "+renameErr.Error())
			failed++
			continue
		}

		// Clean up the manifest sidecar now that the artifact is promoted.
		os.Remove(manifestPath) //nolint:errcheck

		log.Info().
			Str("build_id", build.ID).
			Str("sha256", sha).
			Int64("size_bytes", info.Size()).
			Msg("reconcile: self-healed orphaned initramfs build")
		finishInitramfsBuild(ctx, s, build.ID, sha, info.Size(), "", "success (recovered after restart)")
		healed++
	}

	if healed > 0 {
		log.Info().Int("count", healed).Msg("reconcile: self-healed orphaned initramfs builds")
	}
	if failed > 0 {
		log.Warn().Int("count", failed).Msg("reconcile: marked pending initramfs builds as failed")
	}
	return nil
}

// finishInitramfsBuild is a helper that calls db.FinishInitramfsBuild with a
// short timeout and logs any error.
func finishInitramfsBuild(ctx context.Context, s *Server, buildID, sha string, sizeBytes int64, kernelVer, outcome string) {
	dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.FinishInitramfsBuild(dbCtx, buildID, sha, sizeBytes, kernelVer, outcome); err != nil {
		log.Error().Err(err).Str("build_id", buildID).Msg("reconcile: failed to update initramfs build record")
	}
}

// computeReconcileSHA256 computes the hex-encoded SHA256 of the file at path.
func computeReconcileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CleanupOrphanedInitramfsTmpDirs removes clustr-initramfs-build-* temp dirs
// under the OS temp directory that are older than maxAge. Call this at startup
// to reclaim disk space left by builds that crashed before their defer ran.
func (s *Server) CleanupOrphanedInitramfsTmpDirs(maxAge time.Duration) {
	tmpDir := os.TempDir()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		log.Warn().Err(err).Str("dir", tmpDir).Msg("reconcile: cannot read tmp dir for orphaned build cleanup")
		return
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), "clustr-initramfs-build-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue // too recent — might belong to a live build
		}
		fullPath := filepath.Join(tmpDir, e.Name())
		if rmErr := os.RemoveAll(fullPath); rmErr != nil {
			log.Warn().Err(rmErr).Str("path", fullPath).Msg("reconcile: failed to remove orphaned build tmp dir")
		} else {
			removed++
		}
	}
	if removed > 0 {
		log.Info().Int("count", removed).Msg("reconcile: removed orphaned initramfs build tmp dirs")
	}
}

// AutoResumeBuilds is called at startup when CLUSTR_BUILD_AUTO_RESUME=1 is set.
// It scans for resumable=true builds and re-submits them to the factory.
// The factory field is injected so this function doesn't import pkg/image (would be circular).
func (s *Server) AutoResumeBuilds(ctx context.Context, resumeFn func(imageID, phase string)) error {
	images, err := s.db.ListResumableImages(ctx)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return nil
	}
	for _, img := range images {
		phase, resumable, err := s.db.GetImageResumePhase(ctx, img.ID)
		if err != nil || !resumable {
			continue
		}
		log.Info().
			Str("image_id", img.ID).
			Str("phase", phase).
			Msg("auto-resume: re-submitting interrupted build")
		resumeFn(img.ID, phase)
	}
	return nil
}

// classifyStuckPhase inspects the image directory to determine what phase the
// build was in, returning the best phase to resume from.
func (s *Server) classifyStuckPhase(imageID string) string {
	imageDir := filepath.Join(s.cfg.ImageDir, imageID)

	// Check persisted build-state.json for the last known phase before restart.
	stateFile := filepath.Join(imageDir, "build-state.json")
	if data, err := os.ReadFile(stateFile); err == nil {
		var state api.BuildState
		if json.Unmarshal(data, &state) == nil {
			if state.Phase != "" && state.Phase != PhaseFailed && state.Phase != PhaseCanceled {
				return state.Phase
			}
		}
	}

	// Check if build.json (the successful build manifest) already exists.
	if _, err := os.Stat(filepath.Join(imageDir, "build.json")); err == nil {
		return PhaseFinalizing
	}

	// Check if rootfs directory exists and is non-empty (extraction finished).
	rootfsDir := filepath.Join(imageDir, "rootfs")
	if entries, err := os.ReadDir(rootfsDir); err == nil && len(entries) > 0 {
		return PhaseFinalizing
	}

	return PhaseDownloadingISO
}

// buildStateOnDisk is the structure persisted to <imagedir>/<id>/build-state.json
// on every BuildProgressStore update. Used by ReconcileStuckBuilds and admins
// doing post-mortems to see the last known build state before a restart.
type buildStateOnDisk struct {
	ImageID      string    `json:"image_id"`
	Phase        string    `json:"phase"`
	BytesDone    int64     `json:"bytes_done,omitempty"`
	BytesTotal   int64     `json:"bytes_total,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	ElapsedMS    int64     `json:"elapsed_ms,omitempty"`
}
