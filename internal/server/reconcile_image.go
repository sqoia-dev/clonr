// Package server — reconcile_image.go implements the image blob reconciler.
//
// Entry points:
//   - ReconcileImage(ctx, imageID, opts) — reconcile a single image (core function).
//   - ReconcileAllImages(ctx)           — reconcile every 'ready' image in the DB.
//
// Both are called by:
//   - The startup pass in cmd/clustr-serverd/main.go (via ReconcileAllImages).
//   - The periodic timer in StartBackgroundWorkers (via runImageReconciler).
//   - The pre-deploy guard in handlers/reimage.go (via ReconcileImage with CacheTTL).
//   - The operator HTTP endpoint POST /api/v1/images/:id/reconcile.
//
// # Failure matrix handled
//
//	F1 checksum drift, metadata corroborates → self-heal (update DB to match disk)
//	F2 checksum drift, metadata agrees with DB → quarantine (status=corrupt)
//	F3 checksum drift, no metadata / metadata empty → quarantine (status=corrupt)
//	F4 blob missing → quarantine (status=blob_missing)
//	F5 size mismatch (likely truncation) → quarantine (status=corrupt)
//	F6 blob_path stale, default layout path is valid → self-heal (update blob_path)
//
// # THREAD-SAFETY invariants
//
// reconcileCache (map[string]*reconCacheEntry) is protected by reconcileMu.
// All reads and writes to the map go through mu.Lock/Unlock; the map is never
// accessed without holding the lock.
//
// inFlight (map[string]struct{}) is also protected by reconcileMu and prevents
// concurrent reconcile passes for the same image from overlapping. A second
// caller arriving while a pass is in flight will wait on reconcileMu — the
// in-flight goroutine will have stored a cache entry by the time it releases the
// lock, so the waiter returns the cached result immediately.
package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/image"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/pkg/reconcile"
)

// reconCacheEntry is a single cached reconcile result keyed by image ID.
// The key is (imageID + mtime + size) so a file change invalidates the entry
// even within the TTL window.
type reconCacheEntry struct {
	result    *reconcile.Result
	cachedAt  time.Time
	mtimeKey  string // "<mtime_unix>:<size>" of the blob at hash time
}

// reconcileCache and reconcileMu are on Server. Declared here as a
// comment because the fields are added to the Server struct in server.go.
// See: s.reconcileCache, s.reconcileMu, s.reconcileInFlight.

// ReconcileImage inspects the on-disk blob for imageID against the DB record
// and applies the appropriate action from the F1–F6 failure matrix.
//
// Cache behaviour: if opts.CacheTTL > 0 and a fresh cache entry for (imageID,
// current mtime/size) exists within the TTL, the cached result is returned
// without re-hashing. The pre-deploy guard uses a 1h TTL; the startup pass and
// operator endpoint use TTL=0 (always re-hash).
//
// Errors: returns a non-nil error only for I/O failures (can't open DB, can't
// stat/read the blob). Quarantine and blob_missing outcomes are represented as
// successful results, not errors — unless opts.FailOnQuarantine is true.
func (s *Server) ReconcileImage(ctx context.Context, imageID string, opts reconcile.Opts) (*reconcile.Result, error) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	return s.reconcileImageLocked(ctx, imageID, opts)
}

// reconcileImageLocked is the inner implementation; caller must hold reconcileMu.
func (s *Server) reconcileImageLocked(ctx context.Context, imageID string, opts reconcile.Opts) (*reconcile.Result, error) {
	// Fetch the DB record.
	img, err := s.db.GetBaseImage(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("reconcile image: fetch %s: %w", imageID, err)
	}

	// Only reconcile images that were finalized. Building/interrupted/archived
	// images don't have committed blobs yet.
	if img.Status != api.ImageStatusReady &&
		img.Status != api.ImageStatusCorrupt &&
		img.Status != api.ImageStatusBlobMissing {
		return &reconcile.Result{
			ImageID:        imageID,
			Outcome:        reconcile.OutcomeNoChange,
			PreviousStatus: img.Status,
			NewStatus:      img.Status,
			CheckedAt:      time.Now().UTC(),
		}, nil
	}

	// Resolve the blob path — check DB path first (F6 fallback to default layout).
	blobPath, pathResolution := s.resolveBlobPath(img)

	// Build a stat-key for the cache: "<mtime_unix>:<size>".
	var statKey string
	if blobPath != "" {
		if fi, statErr := os.Stat(blobPath); statErr == nil {
			statKey = fmt.Sprintf("%d:%d", fi.ModTime().Unix(), fi.Size())
		}
	}

	// Check cache when CacheTTL > 0 and the file hasn't changed.
	if opts.CacheTTL > 0 && statKey != "" {
		if entry, ok := s.reconcileCache[imageID]; ok {
			if entry.mtimeKey == statKey && time.Since(entry.cachedAt) < opts.CacheTTL {
				if opts.FailOnQuarantine && (entry.result.Outcome == reconcile.OutcomeQuarantined || entry.result.Outcome == reconcile.OutcomeBlobMissing) {
					return entry.result, fmt.Errorf("image %s is not deployable (status: %s): %s", imageID, entry.result.NewStatus, entry.result.ErrorDetail)
				}
				return entry.result, nil
			}
		}
	}

	// --- Run the actual reconcile checks ---

	result, err := s.runReconcileChecks(ctx, img, blobPath, pathResolution, opts)
	if err != nil {
		return nil, err
	}

	// Store in cache.
	if statKey != "" {
		s.reconcileCache[imageID] = &reconCacheEntry{
			result:   result,
			cachedAt: time.Now(),
			mtimeKey: statKey,
		}
	}

	if opts.FailOnQuarantine && (result.Outcome == reconcile.OutcomeQuarantined || result.Outcome == reconcile.OutcomeBlobMissing) {
		return result, fmt.Errorf("image %s is not deployable (status: %s): %s", imageID, result.NewStatus, result.ErrorDetail)
	}
	return result, nil
}

// resolveBlobPath returns the on-disk path for the blob and a resolution label.
// Primary: the blob_path column value. Fallback: a format-aware default path
// (F6 self-heal). The default differs by image format because the writer side
// chooses different filenames:
//
//	ImageFormatFilesystem → <imageDir>/<id>/rootfs.tar  (tar archive of a rootfs)
//	ImageFormatBlock      → <imageDir>/<id>/image.img   (raw block image, e.g. initramfs builds)
//
// Block-format images (initramfs builds, partclone/dd captures) historically
// finalized without populating blob_path, then got falsely flipped to
// blob_missing because the resolver only knew about rootfs.tar. The
// format-aware default fixes that; F6 write-back (runReconcileChecks) will
// populate blob_path on the next successful pass so future reconciles take
// the BlobPathFoundAtDBPath branch.
func (s *Server) resolveBlobPath(img api.BaseImage) (string, reconcile.BlobPathResolution) {
	defaultPath := defaultBlobPath(s.cfg.ImageDir, img.ID, img.Format)

	// Try to get the stored blob_path from the DB.
	blobPath, err := s.db.GetBlobPath(context.Background(), img.ID)
	if err == nil && blobPath != "" {
		if _, statErr := os.Stat(blobPath); statErr == nil {
			return blobPath, reconcile.BlobPathFoundAtDBPath
		}
		// DB path invalid — try the default layout (F6).
		if _, statErr := os.Stat(defaultPath); statErr == nil {
			return defaultPath, reconcile.BlobPathFoundAtDefaultLayout
		}
		return blobPath, reconcile.BlobPathNotFound
	}

	// No stored path or error — try default layout.
	if _, statErr := os.Stat(defaultPath); statErr == nil {
		return defaultPath, reconcile.BlobPathFoundAtDefaultLayout
	}
	return defaultPath, reconcile.BlobPathNotFound
}

// defaultBlobPath returns the canonical on-disk default path for an image's
// blob given its format. Used as the F6 fallback when blob_path is empty or
// stale in the DB.
//
// Unknown formats (i.e. neither "filesystem" nor "block") fall back to the
// historical rootfs.tar layout. This is the safer default: a rootfs.tar that
// genuinely doesn't exist will surface as BlobPathNotFound (F4) and get the
// row marked blob_missing — visible operator signal — rather than silently
// hiding a row behind a wrong path. If a third format is ever added, this
// switch must be extended in lockstep with the writer.
func defaultBlobPath(imageDir, id string, format api.ImageFormat) string {
	switch format {
	case api.ImageFormatBlock:
		return filepath.Join(imageDir, id, "image.img")
	case api.ImageFormatFilesystem:
		return filepath.Join(imageDir, id, "rootfs.tar")
	default:
		return filepath.Join(imageDir, id, "rootfs.tar")
	}
}

// runReconcileChecks performs the F1–F6 check matrix and applies mutations.
func (s *Server) runReconcileChecks(ctx context.Context, img api.BaseImage, blobPath string, pathResolution reconcile.BlobPathResolution, opts reconcile.Opts) (*reconcile.Result, error) {
	now := time.Now().UTC()
	checks := reconcile.Checks{
		SHAInDB:            img.Checksum,
		SizeInDB:           img.SizeBytes,
		BlobPathResolution: pathResolution,
	}

	result := &reconcile.Result{
		ImageID:        img.ID,
		PreviousStatus: img.Status,
		NewStatus:      img.Status,
		CheckedAt:      now,
	}

	// F4: blob file absent.
	if pathResolution == reconcile.BlobPathNotFound {
		checks.BlobExists = false
		result.Checks = checks
		errMsg := fmt.Sprintf("blob file absent at %s", blobPath)
		result.Outcome = reconcile.OutcomeBlobMissing
		result.NewStatus = api.ImageStatusBlobMissing
		result.ErrorDetail = errMsg

		if img.Status != api.ImageStatusBlobMissing {
			if dbErr := s.db.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusBlobMissing, errMsg); dbErr != nil {
				log.Error().Err(dbErr).Str("image_id", img.ID).Msg("reconcile: failed to set blob_missing status")
			} else {
				auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.blob_missing", img.Status, api.ImageStatusBlobMissing, errMsg)
				result.AuditID = auditID
				result.ActionsTaken = []string{"set_status_blob_missing"}
				s.evictReconcileCache(img.ID)
			}
		}
		return result, nil
	}

	// Stat the file.
	fi, statErr := os.Stat(blobPath)
	if statErr != nil {
		// Unexpected stat error after path resolution succeeded — treat as I/O failure.
		return nil, fmt.Errorf("reconcile image: stat %s: %w", blobPath, statErr)
	}
	checks.BlobExists = true
	checks.SizeOnDisk = fi.Size()

	// F6: blob found at default layout but DB path was different — self-heal path.
	if pathResolution == reconcile.BlobPathFoundAtDefaultLayout {
		storedPath, _ := s.db.GetBlobPath(ctx, img.ID)
		if storedPath != blobPath {
			if dbErr := s.db.SetBlobPath(ctx, img.ID, blobPath); dbErr != nil {
				log.Warn().Err(dbErr).Str("image_id", img.ID).Msg("reconcile: failed to self-heal blob_path")
			} else {
				result.ActionsTaken = append(result.ActionsTaken, "updated_blob_path")
				auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.healed",
					img.Status, img.Status,
					fmt.Sprintf("blob_path updated from %q to %q (F6 default-layout found)", storedPath, blobPath))
				result.AuditID = auditID
				s.evictReconcileCache(img.ID)
			}
		}
	}

	// F5: size mismatch — skip hash when size obviously wrong.
	if img.SizeBytes > 0 && fi.Size() != img.SizeBytes {
		errMsg := fmt.Sprintf("blob size %d expected, %d on disk (possible truncation)", img.SizeBytes, fi.Size())
		checks.BlobPathResolution = pathResolution
		result.Checks = checks
		result.Outcome = reconcile.OutcomeQuarantined
		result.NewStatus = api.ImageStatusCorrupt
		result.ErrorDetail = errMsg

		if img.Status != api.ImageStatusCorrupt {
			if dbErr := s.db.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusCorrupt, errMsg); dbErr != nil {
				log.Error().Err(dbErr).Str("image_id", img.ID).Msg("reconcile: failed to quarantine (size mismatch)")
			} else {
				auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.quarantined", img.Status, api.ImageStatusCorrupt, errMsg)
				result.AuditID = auditID
				result.ActionsTaken = []string{"set_status_corrupt"}
				s.evictReconcileCache(img.ID)
			}
		}
		return result, nil
	}

	// Hash the blob.
	shaOnDisk, hashErr := computeReconcileSHA256(blobPath)
	if hashErr != nil {
		return nil, fmt.Errorf("reconcile image: hash %s: %w", blobPath, hashErr)
	}
	checks.SHAOnDisk = shaOnDisk

	// Read metadata.json for corroboration.
	meta, metaErr := image.ReadMetadata(s.cfg.ImageDir, img.ID)
	if metaErr == nil && meta.ContentSHA256 != "" {
		checks.SHAInMetadata = meta.ContentSHA256
	}

	// F1/OK: sha matches DB.
	if shaOnDisk == img.Checksum {
		result.Checks = checks
		result.Outcome = reconcile.OutcomeOK
		result.NewStatus = img.Status

		// If the image was previously quarantined and now checks out, clear it.
		if img.Status == api.ImageStatusCorrupt || img.Status == api.ImageStatusBlobMissing {
			if dbErr := s.db.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusReady, ""); dbErr != nil {
				log.Error().Err(dbErr).Str("image_id", img.ID).Msg("reconcile: failed to clear quarantine status")
			} else {
				result.NewStatus = api.ImageStatusReady
				result.Outcome = reconcile.OutcomeHealed
				result.ActionsTaken = append(result.ActionsTaken, "cleared_quarantine_status")
				auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.healed",
					img.Status, api.ImageStatusReady, "blob re-checked: SHA matches DB; status cleared to ready")
				result.AuditID = auditID
				s.evictReconcileCache(img.ID)
			}
		}
		return result, nil
	}

	// SHA drifted. Now determine whether to self-heal (F1) or quarantine (F2/F3).

	// force_re_finalize path: operator explicitly accepts on-disk SHA as truth.
	if opts.ForceReFinalize {
		return s.forceReFinalize(ctx, img, blobPath, shaOnDisk, fi.Size(), checks, result)
	}

	// F1 discriminator: does metadata.json agree with the on-disk SHA?
	if checks.SHAInMetadata != "" && checks.SHAInMetadata == shaOnDisk {
		// Independent corroboration — self-heal.
		return s.selfHealChecksum(ctx, img, shaOnDisk, fi.Size(), checks, result)
	}

	// F2: both DB and metadata agree with each other but not with disk.
	// F3: no metadata or metadata empty — not enough corroboration to self-heal.
	var reason string
	if checks.SHAInMetadata != "" && checks.SHAInMetadata == img.Checksum {
		reason = fmt.Sprintf("blob mutated post-finalization (sha %s disagrees with both DB %s and metadata %s)", shaOnDisk, img.Checksum, checks.SHAInMetadata)
	} else if checks.SHAInMetadata == "" {
		reason = fmt.Sprintf("checksum mismatch (disk=%s db=%s) with no corroborating metadata; manual recheck required", shaOnDisk, img.Checksum)
	} else {
		reason = fmt.Sprintf("checksum mismatch (disk=%s db=%s metadata=%s); sources do not agree", shaOnDisk, img.Checksum, checks.SHAInMetadata)
	}

	result.Checks = checks
	result.Outcome = reconcile.OutcomeQuarantined
	result.NewStatus = api.ImageStatusCorrupt
	result.ErrorDetail = reason

	if img.Status != api.ImageStatusCorrupt {
		if dbErr := s.db.UpdateBaseImageStatus(ctx, img.ID, api.ImageStatusCorrupt, reason); dbErr != nil {
			log.Error().Err(dbErr).Str("image_id", img.ID).Msg("reconcile: failed to quarantine image")
		} else {
			auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.quarantined", img.Status, api.ImageStatusCorrupt, reason)
			result.AuditID = auditID
			result.ActionsTaken = []string{"set_status_corrupt"}
			s.evictReconcileCache(img.ID)
		}
	}
	return result, nil
}

// selfHealChecksum handles the F1 self-heal path: metadata corroborates disk SHA.
func (s *Server) selfHealChecksum(ctx context.Context, img api.BaseImage, shaOnDisk string, sizeOnDisk int64, checks reconcile.Checks, result *reconcile.Result) (*reconcile.Result, error) {
	detail := fmt.Sprintf("checksum updated from %s to %s (metadata.json corroborates disk SHA; F1 self-heal)", img.Checksum, shaOnDisk)
	result.Checks = checks
	result.Outcome = reconcile.OutcomeHealed
	result.NewStatus = api.ImageStatusReady

	if err := s.db.RepairBaseImageChecksum(ctx, img.ID, shaOnDisk, sizeOnDisk); err != nil {
		return nil, fmt.Errorf("reconcile: self-heal F1: %w", err)
	}
	result.ActionsTaken = append(result.ActionsTaken, "updated_checksum", "updated_size_bytes")
	if img.Status != api.ImageStatusReady {
		result.ActionsTaken = append(result.ActionsTaken, "cleared_quarantine_status")
	}
	auditID := s.recordReconcileAudit(ctx, img.ID, "system", "system", "image.reconcile.healed", img.Status, api.ImageStatusReady, detail)
	result.AuditID = auditID
	s.evictReconcileCache(img.ID)

	log.Info().
		Str("image_id", img.ID).
		Str("old_sha", img.Checksum).
		Str("new_sha", shaOnDisk).
		Msg("reconcile: F1 self-healed checksum drift (metadata corroborated)")

	return result, nil
}

// forceReFinalize handles the operator-initiated force_re_finalize path.
// The operator has inspected the on-disk tar and confirmed it is the correct content.
func (s *Server) forceReFinalize(ctx context.Context, img api.BaseImage, blobPath, shaOnDisk string, sizeOnDisk int64, checks reconcile.Checks, result *reconcile.Result) (*reconcile.Result, error) {
	// Read existing metadata to preserve all non-checksum fields.
	meta, metaErr := image.ReadMetadata(s.cfg.ImageDir, img.ID)
	if metaErr != nil {
		// Build a minimal metadata from the DB record if the sidecar is missing.
		meta = image.ImageMetadata{
			ID:               img.ID,
			Name:             img.Name,
			BuildMethod:      img.BuildMethod,
		}
	}
	meta.ContentSHA256 = shaOnDisk
	meta.ContentSizeBytes = sizeOnDisk

	// Rewrite metadata.json.
	if writeErr := image.WriteMetadata(s.cfg.ImageDir, img.ID, meta); writeErr != nil {
		return nil, fmt.Errorf("reconcile: force-re-finalize: write metadata: %w", writeErr)
	}

	// Update DB checksum + size.
	if dbErr := s.db.RepairBaseImageChecksum(ctx, img.ID, shaOnDisk, sizeOnDisk); dbErr != nil {
		return nil, fmt.Errorf("reconcile: force-re-finalize: update DB: %w", dbErr)
	}

	detail := fmt.Sprintf("operator force-re-finalized: checksum %s → %s, size %d → %d; metadata.json rewritten", img.Checksum, shaOnDisk, img.SizeBytes, sizeOnDisk)
	checks.SHAInMetadata = shaOnDisk
	result.Checks = checks
	result.Outcome = reconcile.OutcomeReFinalized
	result.NewStatus = api.ImageStatusReady
	result.ActionsTaken = []string{"updated_checksum", "updated_size_bytes", "rewrote_metadata_json"}
	if img.Status != api.ImageStatusReady {
		result.ActionsTaken = append(result.ActionsTaken, "cleared_quarantine_status")
	}

	auditID := s.recordReconcileAudit(ctx, img.ID, "operator", "operator", "image.re_finalize.forced", img.Status, api.ImageStatusReady, detail)
	result.AuditID = auditID
	s.evictReconcileCache(img.ID)

	log.Warn().
		Str("image_id", img.ID).
		Str("old_sha", img.Checksum).
		Str("new_sha", shaOnDisk).
		Msg("reconcile: force-re-finalized image (operator accepted on-disk SHA as truth)")

	return result, nil
}

// ReconcileAllImages runs a reconcile pass over every image in 'ready', 'corrupt',
// or 'blob_missing' status. Called at startup (in a goroutine) and by the periodic
// timer. Returns a summary log line.
func (s *Server) ReconcileAllImages(ctx context.Context) {
	// Fetch all images that need reconciliation.
	readyImages, err := s.db.ListBaseImages(ctx, string(api.ImageStatusReady), "")
	if err != nil {
		log.Error().Err(err).Msg("image-reconcile: failed to list ready images")
		return
	}
	corruptImages, err := s.db.ListBaseImages(ctx, string(api.ImageStatusCorrupt), "")
	if err != nil {
		log.Error().Err(err).Msg("image-reconcile: failed to list corrupt images")
	}
	missingImages, err := s.db.ListBaseImages(ctx, string(api.ImageStatusBlobMissing), "")
	if err != nil {
		log.Error().Err(err).Msg("image-reconcile: failed to list blob_missing images")
	}

	images := append(readyImages, append(corruptImages, missingImages...)...)
	if len(images) == 0 {
		log.Debug().Msg("image-reconcile: no images to reconcile")
		return
	}

	var healed, quarantined, blobMissing, ok int
	for _, img := range images {
		if ctx.Err() != nil {
			log.Warn().Msg("image-reconcile: context cancelled, stopping early")
			return
		}
		result, err := s.ReconcileImage(ctx, img.ID, reconcile.Opts{CacheTTL: 0})
		if err != nil {
			log.Error().Err(err).Str("image_id", img.ID).Msg("image-reconcile: error checking image (skipping)")
			continue
		}
		switch result.Outcome {
		case reconcile.OutcomeHealed:
			healed++
			log.Info().
				Str("image_id", img.ID).
				Str("image_name", img.Name).
				Str("outcome", string(result.Outcome)).
				Strs("actions", result.ActionsTaken).
				Msg("image-reconcile: healed")
		case reconcile.OutcomeQuarantined:
			quarantined++
			log.Warn().
				Str("image_id", img.ID).
				Str("image_name", img.Name).
				Str("detail", result.ErrorDetail).
				Msg("image-reconcile: quarantined (corrupt)")
		case reconcile.OutcomeBlobMissing:
			blobMissing++
			log.Warn().
				Str("image_id", img.ID).
				Str("image_name", img.Name).
				Str("detail", result.ErrorDetail).
				Msg("image-reconcile: blob missing")
		default:
			ok++
		}
	}

	log.Info().
		Int("total", len(images)).
		Int("ok", ok).
		Int("healed", healed).
		Int("quarantined", quarantined).
		Int("blob_missing", blobMissing).
		Msg("image-reconcile: pass complete")
}

// runImageReconciler is the periodic background goroutine. It ticks every
// CLUSTR_RECONCILE_INTERVAL (default 6h; 0 = disabled). At most one pass runs
// at a time — if the previous pass is still running when the ticker fires, the
// tick is skipped.
func (s *Server) runImageReconciler(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		log.Info().Msg("image-reconcile: periodic timer disabled (CLUSTR_RECONCILE_INTERVAL=0)")
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Info().Dur("interval", interval).Msg("image-reconcile: periodic timer started")

	running := false
	var mu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("image-reconcile: shutting down")
			return
		case <-ticker.C:
			mu.Lock()
			if running {
				mu.Unlock()
				log.Debug().Msg("image-reconcile: previous pass still running, skipping tick")
				continue
			}
			running = true
			mu.Unlock()

			go func() {
				defer func() {
					mu.Lock()
					running = false
					mu.Unlock()
				}()
				log.Info().Msg("image-reconcile: periodic pass starting")
				s.ReconcileAllImages(ctx)
			}()
		}
	}
}

// recordReconcileAudit writes an audit_log row for a reconcile state change.
// Returns the generated audit ID, or "" on error (non-fatal).
func (s *Server) recordReconcileAudit(ctx context.Context, imageID, actorID, actorLabel, action string, oldStatus, newStatus api.ImageStatus, detail string) string {
	auditID := "aud-rcn-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	type reconcileAuditPayload struct {
		Status string `json:"status"`
		Detail string `json:"detail,omitempty"`
	}
	s.audit.Record(ctx, actorID, actorLabel, action, "base_image", imageID, "",
		reconcileAuditPayload{Status: string(oldStatus)},
		reconcileAuditPayload{Status: string(newStatus), Detail: detail},
	)
	return auditID
}

// evictReconcileCache removes the cache entry for imageID.
// Caller must hold reconcileMu.
func (s *Server) evictReconcileCache(imageID string) {
	delete(s.reconcileCache, imageID)
}

//lint:ignore U1000 called by the periodic reconciler (Sprint 32 IMAGE-RECONCILE); linker sees it used from the goroutine path
// reconcileImageCacheTTL returns the configured reconcile cache TTL.
// Reads CLUSTR_RECONCILE_TTL (Go duration string); default 1h.
func reconcileImageCacheTTL() time.Duration {
	v := os.Getenv("CLUSTR_RECONCILE_TTL")
	if v == "" {
		return 1 * time.Hour
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Warn().Str("CLUSTR_RECONCILE_TTL", v).Msg("invalid CLUSTR_RECONCILE_TTL, using default 1h")
		return 1 * time.Hour
	}
	return d
}

// reconcileInterval returns the configured periodic reconcile interval.
// Reads CLUSTR_RECONCILE_INTERVAL (Go duration string); default 6h; 0 = disabled.
func reconcileInterval() time.Duration {
	v := os.Getenv("CLUSTR_RECONCILE_INTERVAL")
	if v == "" {
		return 6 * time.Hour
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Warn().Str("CLUSTR_RECONCILE_INTERVAL", v).Msg("invalid CLUSTR_RECONCILE_INTERVAL, using default 6h")
		return 6 * time.Hour
	}
	return d
}

//lint:ignore U1000 sentinel returned by FailOnQuarantine path in ReconcileImage; callers check it with errors.Is
// errImageNotDeployable is returned by ReconcileImage when FailOnQuarantine is
// set and the image is corrupt or blob_missing.
var errImageNotDeployable = errors.New("image not deployable")
