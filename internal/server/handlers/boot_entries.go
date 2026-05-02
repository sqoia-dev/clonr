// Package handlers — boot_entries.go implements the Boot Menu endpoints (#160).
//
// Routes:
//
//	GET    /api/v1/boot-entries         — list (optional ?enabled=true filter)
//	POST   /api/v1/boot-entries         — create
//	GET    /api/v1/boot-entries/{id}    — fetch one
//	PUT    /api/v1/boot-entries/{id}    — update
//	DELETE /api/v1/boot-entries/{id}    — delete
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// BootEntriesDBIface is the subset of *db.DB used by BootEntriesHandler.
type BootEntriesDBIface interface {
	CreateBootEntry(ctx context.Context, e api.BootEntry) error
	GetBootEntry(ctx context.Context, id string) (api.BootEntry, error)
	ListBootEntries(ctx context.Context, enabledOnly bool) ([]api.BootEntry, error)
	UpdateBootEntry(ctx context.Context, e api.BootEntry) error
	DeleteBootEntry(ctx context.Context, id string) error
}

// BootEntriesHandler handles /api/v1/boot-entries endpoints.
type BootEntriesHandler struct {
	DB BootEntriesDBIface
}

// ─── GET /api/v1/boot-entries ────────────────────────────────────────────────

// ListBootEntries returns all boot entries. Pass ?enabled=true to filter
// to only enabled entries (used by the iPXE renderer).
func (h *BootEntriesHandler) ListBootEntries(w http.ResponseWriter, r *http.Request) {
	enabledOnly := r.URL.Query().Get("enabled") == "true"

	entries, err := h.DB.ListBootEntries(r.Context(), enabledOnly)
	if err != nil {
		log.Error().Err(err).Msg("boot-entries: list")
		writeError(w, err)
		return
	}
	if entries == nil {
		entries = []api.BootEntry{}
	}
	writeJSON(w, http.StatusOK, api.ListBootEntriesResponse{
		Entries: entries,
		Total:   len(entries),
	})
}

// ─── POST /api/v1/boot-entries ───────────────────────────────────────────────

// CreateBootEntry creates a new boot entry.
func (h *BootEntriesHandler) CreateBootEntry(w http.ResponseWriter, r *http.Request) {
	var req api.CreateBootEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if !validBootEntryKind(req.Kind) {
		writeValidationError(w, "kind must be one of: kernel, iso, rescue, memtest")
		return
	}
	if req.KernelURL == "" {
		writeValidationError(w, "kernel_url is required")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	now := time.Now().UTC()
	e := api.BootEntry{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Kind:      req.Kind,
		KernelURL: req.KernelURL,
		InitrdURL: req.InitrdURL,
		Cmdline:   req.Cmdline,
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.DB.CreateBootEntry(r.Context(), e); err != nil {
		log.Error().Err(err).Str("name", e.Name).Msg("boot-entries: create")
		writeError(w, err)
		return
	}
	log.Info().Str("id", e.ID).Str("name", e.Name).Str("kind", e.Kind).
		Msg("boot-entries: created")
	writeJSON(w, http.StatusCreated, map[string]any{"boot_entry": e})
}

// ─── GET /api/v1/boot-entries/{id} ───────────────────────────────────────────

// GetBootEntry returns a single boot entry by ID.
func (h *BootEntriesHandler) GetBootEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	e, err := h.DB.GetBootEntry(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"boot_entry": e})
}

// ─── PUT /api/v1/boot-entries/{id} ───────────────────────────────────────────

// UpdateBootEntry applies a partial update to an existing boot entry.
// All supplied non-zero fields replace the stored values; omitted fields keep
// their current values.
func (h *BootEntriesHandler) UpdateBootEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	current, err := h.DB.GetBootEntry(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var req api.UpdateBootEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	if req.Name != "" {
		current.Name = req.Name
	}
	if req.Kind != "" {
		if !validBootEntryKind(req.Kind) {
			writeValidationError(w, "kind must be one of: kernel, iso, rescue, memtest")
			return
		}
		current.Kind = req.Kind
	}
	if req.KernelURL != "" {
		current.KernelURL = req.KernelURL
	}
	// InitrdURL and Cmdline: empty string in the request means "clear the field".
	// Use a pointer in the request type to distinguish "omitted" from "cleared".
	// For now, only update when the request field is non-empty; clearing requires
	// the caller to send the full update with an explicit empty string — the PUT
	// semantics here are patch-style (only non-zero strings update the field).
	// The Enabled flag is a pointer so nil means "don't change".
	if req.InitrdURL != "" {
		current.InitrdURL = req.InitrdURL
	}
	if req.Cmdline != "" {
		current.Cmdline = req.Cmdline
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}

	if err := h.DB.UpdateBootEntry(r.Context(), current); err != nil {
		log.Error().Err(err).Str("id", id).Msg("boot-entries: update")
		writeError(w, err)
		return
	}

	updated, err := h.DB.GetBootEntry(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("id", id).Str("name", updated.Name).Bool("enabled", updated.Enabled).
		Msg("boot-entries: updated")
	writeJSON(w, http.StatusOK, map[string]any{"boot_entry": updated})
}

// ─── DELETE /api/v1/boot-entries/{id} ────────────────────────────────────────

// DeleteBootEntry removes a boot entry.
func (h *BootEntriesHandler) DeleteBootEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteBootEntry(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("id", id).Msg("boot-entries: deleted")
	w.WriteHeader(http.StatusNoContent)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

var validBootEntryKinds = map[string]bool{
	string(api.BootEntryKindKernel):  true,
	string(api.BootEntryKindISO):     true,
	string(api.BootEntryKindRescue):  true,
	string(api.BootEntryKindMemtest): true,
}

func validBootEntryKind(k string) bool {
	return validBootEntryKinds[k]
}
