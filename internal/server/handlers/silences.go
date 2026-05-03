// Package handlers — silences.go implements Sprint 24 #155 alert silence endpoints.
//
// Routes:
//
//	GET    /api/v1/alerts/silences         — list all active silences
//	POST   /api/v1/alerts/silences         — create a silence
//	DELETE /api/v1/alerts/silences/{id}    — delete (unsilence) a silence
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/alerts"
)

// SilenceStoreIface is the subset of *alerts.SilenceStore used by SilencesHandler.
type SilenceStoreIface interface {
	ListActive(ctx context.Context) ([]alerts.Silence, error)
	Create(ctx context.Context, sil alerts.Silence) error
	Delete(ctx context.Context, id string) error
}

// SilencesHandler handles alert silence endpoints.
type SilencesHandler struct {
	Store SilenceStoreIface
}

// HandleList handles GET /api/v1/alerts/silences.
// Returns currently active (non-expired) silences.
func (h *SilencesHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	silences, err := h.Store.ListActive(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("silences: list")
		writeError(w, err)
		return
	}
	if silences == nil {
		silences = []alerts.Silence{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"silences": silences})
}

// HandleCreate handles POST /api/v1/alerts/silences.
//
// Body:
//
//	{
//	  "rule_name": "disk-percent",
//	  "node_id":   "...",        // optional; omit for global silence
//	  "duration":  "1h"          // or "4h", "24h", "forever"
//	}
func (h *SilencesHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RuleName string  `json:"rule_name"`
		NodeID   *string `json:"node_id,omitempty"`
		Duration string  `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.RuleName == "" {
		writeValidationError(w, "rule_name is required")
		return
	}
	if req.Duration == "" {
		writeValidationError(w, "duration is required (1h, 4h, 24h, or forever)")
		return
	}

	var expiresAt int64
	switch strings.ToLower(strings.TrimSpace(req.Duration)) {
	case "forever":
		expiresAt = -1
	case "1h":
		expiresAt = time.Now().Add(1 * time.Hour).Unix()
	case "4h":
		expiresAt = time.Now().Add(4 * time.Hour).Unix()
	case "24h":
		expiresAt = time.Now().Add(24 * time.Hour).Unix()
	default:
		writeValidationError(w, "duration must be one of: 1h, 4h, 24h, forever")
		return
	}

	// Extract requesting user from context (best-effort).
	createdBy := ""
	if u, ok := r.Context().Value("username").(string); ok {
		createdBy = u
	}

	sil := alerts.Silence{
		ID:        uuid.New().String(),
		RuleName:  req.RuleName,
		NodeID:    req.NodeID,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().Unix(),
		CreatedBy: createdBy,
	}
	if err := h.Store.Create(r.Context(), sil); err != nil {
		log.Error().Err(err).Str("rule", req.RuleName).Msg("silences: create")
		writeError(w, err)
		return
	}
	log.Info().Str("id", sil.ID).Str("rule", sil.RuleName).Msg("silences: created")
	writeJSON(w, http.StatusCreated, map[string]any{"silence": sil})
}

// HandleDelete handles DELETE /api/v1/alerts/silences/{id}.
func (h *SilencesHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.Delete(r.Context(), id); err != nil {
		log.Error().Err(err).Str("id", id).Msg("silences: delete")
		writeError(w, err)
		return
	}
	log.Info().Str("id", id).Msg("silences: deleted")
	w.WriteHeader(http.StatusNoContent)
}
