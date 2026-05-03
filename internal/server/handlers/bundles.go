package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// BundlesHandler exposes the cluster's slurm RPM catalog (the slurm_builds table).
// The Bundles tab is the view of slurm_builds ONLY. Binary slurm metadata is
// exposed via `clustr-serverd version`; on-disk repo state is exposed via
// `clustr-serverd bundle list`. Do NOT synthesize Bundle entries from any
// other source — see docs/SPRINT-EMBED-SLURM-REMOVAL.md for context.
type BundlesHandler struct {
	DB *db.DB
	// Audit is optional; when set, delete operations are recorded.
	Audit *db.AuditService
	// GetActorInfo returns (actorID, actorLabel) from a request context.
	GetActorInfo func(r *http.Request) (id, label string)
}

// ListBundles handles GET /api/v1/bundles.
//
// Returns all slurm builds from the slurm_builds table as bundle entries,
// enriched with nodes_using count and last_deployed_at. Returns an empty
// array when no builds exist.
//
// This is the canonical source of truth shown in the Bundles tab. It is the
// same underlying data as GET /api/v1/slurm/builds — no separate table, no
// separate pipeline.
func (h *BundlesHandler) ListBundles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Load all slurm builds from the DB.
	rows, err := h.DB.SlurmListBuilds(ctx)
	if err != nil {
		log.Error().Err(err).Msg("bundles: list builds failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to list builds", Code: "internal_error",
		})
		return
	}

	activeBuildID, _ := h.DB.SlurmGetActiveBuildID(ctx)

	// Load per-node version data to compute nodes_using and last_deployed_at.
	nodeVersions, err := h.DB.SlurmListNodeVersions(ctx)
	if err != nil {
		// Non-fatal — enrichment fields will be zero/empty.
		log.Warn().Err(err).Msg("bundles: list node versions failed (non-fatal, enrichment skipped)")
		nodeVersions = nil
	}

	// Build lookup: buildID → nodesUsing count + max installedAt.
	type enrichment struct {
		nodesUsing     int
		lastDeployedAt int64
	}
	enrichMap := make(map[string]*enrichment)
	for _, nv := range nodeVersions {
		if nv.BuildID == "" {
			continue
		}
		e := enrichMap[nv.BuildID]
		if e == nil {
			e = &enrichment{}
			enrichMap[nv.BuildID] = e
		}
		e.nodesUsing++
		if nv.InstalledAt > e.lastDeployedAt {
			e.lastDeployedAt = nv.InstalledAt
		}
	}

	// Convert DB rows to API bundles.
	bundles := make([]api.Bundle, 0, len(rows))

	for _, row := range rows {
		enrich := enrichMap[row.ID]
		var nodesUsing int
		var lastDeployedAt int64
		if enrich != nil {
			nodesUsing = enrich.nodesUsing
			lastDeployedAt = enrich.lastDeployedAt
		}

		sigStatus := signatureStatus(row)

		bundle := api.Bundle{
			ID:             row.ID,
			Name:           bundleName(row.Version, row.Arch),
			SlurmVersion:   row.Version,
			BundleVersion:  row.Version,
			SHA256:         row.ArtifactChecksum,
			Kind:           "build",
			Source:         "clustr-build-pipeline",
			Status:         row.Status,
			IsActive:       row.ID == activeBuildID,
			NodesUsing:     nodesUsing,
			LastDeployedAt: lastDeployedAt,
			SigStatus:      sigStatus,
			StartedAt:      row.StartedAt,
			CompletedAt:    row.CompletedAt,
		}
		bundles = append(bundles, bundle)
	}

	writeJSON(w, http.StatusOK, api.ListBundlesResponse{
		Bundles: bundles,
		Total:   len(bundles),
	})
}

// DeleteBundle handles DELETE /api/v1/bundles/{id}.
//
// Guards:
//   - Cannot delete a build that is currently the active build (HTTP 409).
//   - Cannot delete a build used by enrolled nodes unless ?force=true (HTTP 409).
//
// On success: removes the DB row, deletes the artifact file, removes RPMs from
// the internal repo, and writes an audit log entry.
func (h *BundlesHandler) DeleteBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"

	// Fetch the build record first so we have the artifact path and version.
	row, err := h.DB.SlurmGetBuild(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, api.ErrorResponse{
				Error: "bundle not found", Code: "not_found",
			})
			return
		}
		log.Error().Err(err).Str("id", id).Msg("bundles: delete: fetch build")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to fetch bundle", Code: "internal_error",
		})
		return
	}

	// In-use guard: find nodes that have this build installed.
	nodeVersions, err := h.DB.SlurmListNodeVersions(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("bundles: delete: list node versions failed")
		nodeVersions = nil
	}
	var affectedNodes []string
	for _, nv := range nodeVersions {
		if nv.BuildID == id {
			affectedNodes = append(affectedNodes, nv.NodeID)
		}
	}

	if len(affectedNodes) > 0 && !force {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":          fmt.Sprintf("bundle is in use by %d node(s); use ?force=true to delete anyway", len(affectedNodes)),
			"code":           "bundle_in_use",
			"affected_nodes": affectedNodes,
			"nodes_using":    len(affectedNodes),
		})
		return
	}

	// SlurmDeleteBuild rejects deletion of the active build.
	if err := h.DB.SlurmDeleteBuild(ctx, id); err != nil {
		log.Error().Err(err).Str("id", id).Msg("bundles: delete: db delete")
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: err.Error(), Code: "conflict",
		})
		return
	}

	// Best-effort artifact file + repo RPM cleanup.
	if row.ArtifactPath != "" {
		if rmErr := os.Remove(row.ArtifactPath); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn().Err(rmErr).Str("path", row.ArtifactPath).
				Msg("bundles: delete: artifact file removal failed (non-fatal)")
		}
		// Also remove the signed RPMs for this build from the internal repo.
		repoRefreshForBuild(row.ArtifactPath)
	}

	// Audit.
	if h.Audit != nil && h.GetActorInfo != nil {
		actorID, actorLabel := h.GetActorInfo(r)
		extras := map[string]any{
			"slurm_version":  row.Version,
			"artifact_path":  row.ArtifactPath,
			"force":          force,
			"affected_nodes": len(affectedNodes),
		}
		h.Audit.Record(ctx, actorID, actorLabel,
			db.AuditActionSlurmBuildDelete, "slurm_build", id,
			r.RemoteAddr, nil, extras,
		)
	}

	log.Info().
		Str("id", id).
		Str("version", row.Version).
		Int("affected_nodes", len(affectedNodes)).
		Bool("force", force).
		Msg("bundles: deleted")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "deleted",
		"id":             id,
		"slurm_version":  row.Version,
		"affected_nodes": len(affectedNodes),
	})
}

// bundleName returns the display name for a build-pipeline bundle.
// e.g. "slurm-25.11.5-x86_64"
func bundleName(version, arch string) string {
	if arch == "" {
		return "slurm-" + version
	}
	return fmt.Sprintf("slurm-%s-%s", version, arch)
}

// signatureStatus returns "signed", "unsigned", or "unknown" based on the
// presence of an artifact checksum (a completed RPM-signed build always has one).
func signatureStatus(row db.SlurmBuildRow) string {
	if row.Status != "completed" {
		return "unknown"
	}
	if row.ArtifactChecksum != "" {
		return "signed"
	}
	return "unsigned"
}

// repoRefreshForBuild performs best-effort cleanup of RPMs in the internal repo
// that belong to the given build artifact. Errors are logged but not returned.
func repoRefreshForBuild(artifactPath string) {
	if artifactPath == "" {
		return
	}
	repoBase := "/var/lib/clustr/repo/clustr-internal-repo"
	// Extract the version from the artifact filename, e.g. "slurm-25.11.5-x86_64.tar.gz"
	base := filepath.Base(artifactPath)
	parts := strings.SplitN(base, "-", 3)
	if len(parts) < 2 {
		return
	}
	version := parts[1]
	if version == "" {
		return
	}

	entries, err := filepath.Glob(filepath.Join(repoBase, "*", "*"))
	if err != nil || len(entries) == 0 {
		return
	}
	for _, dir := range entries {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		// Only act on dirs that have repodata (i.e. are real repo dirs).
		if _, err := os.Stat(filepath.Join(dir, "repodata")); err != nil {
			continue
		}
		rpmGlob := filepath.Join(dir, fmt.Sprintf("*-%s-*.rpm", version))
		rpms, _ := filepath.Glob(rpmGlob)
		for _, rpm := range rpms {
			if err := os.Remove(rpm); err != nil && !os.IsNotExist(err) {
				log.Warn().Err(err).Str("rpm", rpm).Msg("bundles: delete: rpm removal failed")
			} else if err == nil {
				log.Info().Str("rpm", rpm).Msg("bundles: delete: removed rpm from repo")
			}
		}
	}
}
