package handlers

// changes.go — two-stage commit endpoints (#154).
//
// POST /api/v1/changes           — stage a change
// GET  /api/v1/changes           — list pending changes (optional ?kind= filter)
// POST /api/v1/changes/commit    — commit all or a subset (body: {ids:[]})
// POST /api/v1/changes/clear     — delete all or a subset (body: {ids:[]})
// GET  /api/v1/changes/count     — count of pending changes (for the badge poll)
//
// Two-stage mode toggles (server-persisted, survive restarts):
// GET  /api/v1/changes/mode              — all surface flags
// PUT  /api/v1/changes/mode/{surface}    — set flag for one surface

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ChangesDBIface is the database interface required by ChangesHandler.
// Extracted to allow fake implementations in unit tests.
type ChangesDBIface interface {
	PendingChangesInsert(ctx context.Context, c db.PendingChange) error
	PendingChangesList(ctx context.Context, kind string) ([]db.PendingChange, error)
	PendingChangesGet(ctx context.Context, id string) (db.PendingChange, error)
	PendingChangesDelete(ctx context.Context, ids []string) error
	PendingChangesDeleteAll(ctx context.Context) error
	PendingChangesCount(ctx context.Context) (int, error)
	StageModeGet(ctx context.Context, surface string) (bool, error)
	StageModeSet(ctx context.Context, surface string, enabled bool) error
	StageModeGetAll(ctx context.Context) (map[string]bool, error)
}

// ChangesCommitFn is a function that applies a staged change payload.
// Returns an error when the change could not be applied.
// The function receives the full PendingChange so it can inspect Kind/Target.
type ChangesCommitFn func(ctx context.Context, change db.PendingChange) error

// ChangesHandler handles the two-stage commit API.
type ChangesHandler struct {
	DB           ChangesDBIface
	Audit        *db.AuditService
	GetActorInfo func(r *http.Request) (id, label string)

	// CommitFns is a map from kind → apply function. If a kind has no entry
	// the commit attempt logs a warning and marks that change as "skipped".
	CommitFns map[string]ChangesCommitFn
}

// stageRequest is the body for POST /api/v1/changes.
type stageRequest struct {
	Kind    string          `json:"kind"`
	Target  string          `json:"target"`
	Payload json.RawMessage `json:"payload"`
}

// commitRequest is the body for POST /api/v1/changes/commit and /clear.
// If IDs is nil or empty, all pending changes are targeted.
type commitRequest struct {
	IDs []string `json:"ids,omitempty"`
}

// commitResult is the per-change result returned by POST /api/v1/changes/commit.
type commitResult struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// HandleStage handles POST /api/v1/changes.
func (h *ChangesHandler) HandleStage(w http.ResponseWriter, r *http.Request) {
	var req stageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "request body must be valid JSON")
		return
	}
	if req.Kind == "" {
		writeValidationError(w, "kind is required")
		return
	}
	if req.Target == "" {
		writeValidationError(w, "target is required")
		return
	}
	if len(req.Payload) == 0 || string(req.Payload) == "null" {
		writeValidationError(w, "payload is required")
		return
	}

	actorID := ""
	if h.GetActorInfo != nil {
		actorID, _ = h.GetActorInfo(r)
	}

	c := db.PendingChange{
		ID:        uuid.New().String(),
		Kind:      req.Kind,
		Target:    req.Target,
		Payload:   string(req.Payload),
		CreatedBy: actorID,
		CreatedAt: time.Now().Unix(),
	}

	if err := h.DB.PendingChangesInsert(r.Context(), c); err != nil {
		log.Error().Err(err).Str("kind", req.Kind).Str("target", req.Target).Msg("changes: stage insert failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, c)
}

// HandleList handles GET /api/v1/changes.
func (h *ChangesHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	changes, err := h.DB.PendingChangesList(r.Context(), kind)
	if err != nil {
		log.Error().Err(err).Msg("changes: list failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if changes == nil {
		changes = []db.PendingChange{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"changes": changes, "total": len(changes)})
}

// HandleCount handles GET /api/v1/changes/count.
func (h *ChangesHandler) HandleCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.DB.PendingChangesCount(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("changes: count failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// HandleCommit handles POST /api/v1/changes/commit.
// Applies each change through its kind-specific CommitFn.
// Atomic per-change: failures are logged and reported, execution continues.
func (h *ChangesHandler) HandleCommit(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional

	changes, err := h.resolveChanges(r.Context(), req.IDs)
	if err != nil {
		log.Error().Err(err).Msg("changes: commit resolve failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	results := make([]commitResult, 0, len(changes))
	for _, c := range changes {
		res := commitResult{ID: c.ID, Kind: c.Kind, Target: c.Target}

		fn, ok := h.CommitFns[c.Kind]
		if !ok {
			res.Success = false
			res.Error = "no commit handler registered for kind " + c.Kind
			log.Warn().Str("id", c.ID).Str("kind", c.Kind).Msg("changes: commit: no handler, skipping")
			results = append(results, res)
			continue
		}

		if applyErr := fn(r.Context(), c); applyErr != nil {
			res.Success = false
			res.Error = applyErr.Error()
			log.Error().Err(applyErr).Str("id", c.ID).Str("kind", c.Kind).Str("target", c.Target).Msg("changes: commit: apply failed")
		} else {
			res.Success = true
			// Remove from the pending queue on success.
			if delErr := h.DB.PendingChangesDelete(r.Context(), []string{c.ID}); delErr != nil {
				log.Error().Err(delErr).Str("id", c.ID).Msg("changes: commit: delete after apply failed")
			}
		}
		results = append(results, res)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

// HandleClear handles POST /api/v1/changes/clear.
func (h *ChangesHandler) HandleClear(w http.ResponseWriter, r *http.Request) {
	var req commitRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // body is optional

	if len(req.IDs) == 0 {
		if err := h.DB.PendingChangesDeleteAll(r.Context()); err != nil {
			log.Error().Err(err).Msg("changes: clear all failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}
	} else {
		if err := h.DB.PendingChangesDelete(r.Context(), req.IDs); err != nil {
			log.Error().Err(err).Msg("changes: clear subset failed")
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// HandleGetMode handles GET /api/v1/changes/mode.
func (h *ChangesHandler) HandleGetMode(w http.ResponseWriter, r *http.Request) {
	flags, err := h.DB.StageModeGetAll(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("changes: get mode failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	// Ensure all known surfaces are present even if no row exists yet.
	for _, s := range knownSurfaces {
		if _, ok := flags[s]; !ok {
			flags[s] = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"mode": flags})
}

// HandleSetMode handles PUT /api/v1/changes/mode/{surface}.
func (h *ChangesHandler) HandleSetMode(w http.ResponseWriter, r *http.Request) {
	surface := chi.URLParam(r, "surface")
	if !isKnownSurface(surface) {
		writeValidationError(w, "unknown surface; valid values: ldap_user, sudoers_rule, node_network")
		return
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "request body must be valid JSON with {enabled: bool}")
		return
	}

	if err := h.DB.StageModeSet(r.Context(), surface, body.Enabled); err != nil {
		log.Error().Err(err).Str("surface", surface).Msg("changes: set mode failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"surface": surface, "enabled": body.Enabled})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// knownSurfaces is the set of surfaces that support two-stage commit.
var knownSurfaces = []string{"ldap_user", "sudoers_rule", "node_network"}

func isKnownSurface(s string) bool {
	for _, k := range knownSurfaces {
		if k == s {
			return true
		}
	}
	return false
}

// resolveChanges returns the changes to act on.
// If ids is non-empty, fetch only those; otherwise return all pending.
func (h *ChangesHandler) resolveChanges(ctx context.Context, ids []string) ([]db.PendingChange, error) {
	if len(ids) == 0 {
		return h.DB.PendingChangesList(ctx, "")
	}
	out := make([]db.PendingChange, 0, len(ids))
	for _, id := range ids {
		c, err := h.DB.PendingChangesGet(ctx, id)
		if err != nil {
			log.Warn().Str("id", id).Msg("changes: resolve: id not found, skipping")
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
