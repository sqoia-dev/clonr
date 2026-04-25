package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/image"
)

// ResumeHandler handles POST /api/v1/images/{id}/resume.
// Reads resume_from_phase, re-enters the factory state machine at that phase,
// and returns 202 with the updated image record.
type ResumeHandler struct {
	DB       *db.DB
	ImageDir string
	Factory  *image.Factory
}

// ResumeImageBuild handles POST /api/v1/images/{id}/resume (admin-scope only).
func (h *ResumeHandler) ResumeImageBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	// Fetch image.
	img, err := h.DB.GetBaseImage(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}

	// Confirm it is in an interrupted/resumable state.
	if img.Status != api.ImageStatusInterrupted && img.Status != api.ImageStatusError {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: fmt.Sprintf("image is in status %q — only interrupted or error images can be resumed", img.Status),
			Code:  "not_resumable",
		})
		return
	}

	phase, resumable, err := h.DB.GetImageResumePhase(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !resumable {
		writeJSON(w, http.StatusConflict, api.ErrorResponse{
			Error: "image is not marked resumable — delete and rebuild",
			Code:  "not_resumable",
		})
		return
	}

	// Mark as building again.
	if err := h.DB.UpdateBaseImageStatus(ctx, id, api.ImageStatusBuilding, ""); err != nil {
		log.Error().Err(err).Str("image_id", id).Msg("resume: update status to building")
		writeError(w, err)
		return
	}
	if err := h.DB.ClearImageResumable(ctx, id); err != nil {
		log.Warn().Err(err).Str("image_id", id).Msg("resume: clear resumable flag (non-fatal)")
	}

	// Audit log.
	log.Info().
		Str("image_id", id).
		Str("phase", phase).
		Str("triggered_by", extractKeyPrefix(r)).
		Msg("resume: re-entering factory at phase")

	// Re-enter the factory state machine.
	go h.Factory.ResumeFromPhase(id, img, phase)

	// Return updated image.
	updated, err := h.DB.GetBaseImage(ctx, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, updated)
}

// ResumeFromPhase re-enters the build pipeline at the given phase.
// This is the factory method wired from the resume handler.
// It is defined here to avoid circular imports — we need access to image.Factory.
func resumeFromPhase(f *image.Factory, imageID string, img api.BaseImage, phase string) {
	// Delegate to factory.
	f.ResumeFromPhase(imageID, img, phase)
}

// inspectResumePhase reads the on-disk build-state.json for an image and
// returns the last recorded phase. Used by the UI to annotate the resume button.
func inspectResumePhase(imageDir, imageID string) string {
	data, err := os.ReadFile(filepath.Join(imageDir, imageID, "build-state.json"))
	if err != nil {
		return ""
	}
	var state struct {
		Phase string `json:"phase"`
	}
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return state.Phase
}
