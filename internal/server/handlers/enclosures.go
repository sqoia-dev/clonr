// Package handlers — enclosures.go implements the chassis enclosure endpoints
// introduced in Sprint 31 (#231).
//
// Routes:
//
//	GET    /api/v1/enclosure-types                           — list canned types
//	GET    /api/v1/racks/{rack_id}/enclosures                — list enclosures in rack
//	POST   /api/v1/racks/{rack_id}/enclosures                — create enclosure
//	GET    /api/v1/enclosures/{id}                           — get enclosure + slots
//	PUT    /api/v1/enclosures/{id}                           — update label / rack_slot_u
//	DELETE /api/v1/enclosures/{id}                           — delete (cascades slot occupancy)
//	POST   /api/v1/enclosures/{id}/slots/{slot_index}        — assign node to slot
//	DELETE /api/v1/enclosures/{id}/slots/{slot_index}        — clear slot
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/enclosures"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// EnclosuresDBIface is the subset of *db.DB used by EnclosuresHandler.
type EnclosuresDBIface interface {
	GetRack(ctx context.Context, id string) (api.Rack, error)
	CreateEnclosure(ctx context.Context, e api.Enclosure) error
	GetEnclosure(ctx context.Context, id string) (api.Enclosure, error)
	ListEnclosuresByRack(ctx context.Context, rackID string) ([]api.Enclosure, error)
	UpdateEnclosure(ctx context.Context, id, label string, rackSlotU int) error
	DeleteEnclosure(ctx context.Context, id string) error
	SetSlotOccupancy(ctx context.Context, enclosureID string, slotIndex int, nodeID string) error
	ClearSlotOccupancy(ctx context.Context, enclosureID string, slotIndex int) error
}

// EnclosuresHandler handles /api/v1/enclosure-types and /api/v1/enclosures endpoints.
type EnclosuresHandler struct {
	DB EnclosuresDBIface
}

// ─── GET /api/v1/enclosure-types ─────────────────────────────────────────────

// ListEnclosureTypes returns the canned enclosure type catalog.
func (h *EnclosuresHandler) ListEnclosureTypes(w http.ResponseWriter, r *http.Request) {
	types := make([]api.EnclosureType, 0, len(enclosures.Catalog))
	for _, et := range enclosures.Catalog {
		types = append(types, api.EnclosureType{
			ID:          et.ID,
			DisplayName: et.DisplayName,
			HeightU:     et.HeightU,
			SlotCount:   et.SlotCount,
			Orientation: et.Orientation,
			Description: et.Description,
		})
	}
	// Deterministic order by ID.
	sort.Slice(types, func(i, j int) bool { return types[i].ID < types[j].ID })
	writeJSON(w, http.StatusOK, api.ListEnclosureTypesResponse{Types: types, Total: len(types)})
}

// ─── GET /api/v1/racks/{rack_id}/enclosures ──────────────────────────────────

// ListEnclosuresForRack returns all enclosures placed in a rack.
func (h *EnclosuresHandler) ListEnclosuresForRack(w http.ResponseWriter, r *http.Request) {
	rackID := chi.URLParam(r, "rack_id")
	if _, err := h.DB.GetRack(r.Context(), rackID); err != nil {
		writeError(w, err)
		return
	}
	encs, err := h.DB.ListEnclosuresByRack(r.Context(), rackID)
	if err != nil {
		log.Error().Err(err).Str("rack_id", rackID).Msg("enclosures: list")
		writeError(w, err)
		return
	}
	if encs == nil {
		encs = []api.Enclosure{}
	}
	writeJSON(w, http.StatusOK, api.ListEnclosuresResponse{Enclosures: encs, Total: len(encs)})
}

// ─── POST /api/v1/racks/{rack_id}/enclosures ─────────────────────────────────

// CreateEnclosure places a new chassis in a rack.
//
// Body: { "type_id": "blade-2u-4slot", "rack_slot_u": 3, "label"?: "blade-01" }
func (h *EnclosuresHandler) CreateEnclosure(w http.ResponseWriter, r *http.Request) {
	rackID := chi.URLParam(r, "rack_id")
	if _, err := h.DB.GetRack(r.Context(), rackID); err != nil {
		writeError(w, err)
		return
	}

	var req struct {
		TypeID    string `json:"type_id"`
		RackSlotU int    `json:"rack_slot_u"`
		Label     string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.TypeID == "" {
		writeValidationError(w, "type_id is required")
		return
	}
	if _, ok := enclosures.Get(req.TypeID); !ok {
		writeValidationError(w, "unknown type_id — see GET /api/v1/enclosure-types for valid values")
		return
	}
	if req.RackSlotU <= 0 {
		writeValidationError(w, "rack_slot_u must be >= 1")
		return
	}

	now := time.Now().UTC()
	enc := api.Enclosure{
		ID:        uuid.New().String(),
		RackID:    rackID,
		RackSlotU: req.RackSlotU,
		TypeID:    req.TypeID,
		Label:     req.Label,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.DB.CreateEnclosure(r.Context(), enc); err != nil {
		log.Error().Err(err).Str("rack_id", rackID).Msg("enclosures: create")
		writeError(w, err)
		return
	}

	// Fetch the full enclosure (with slots) to return in the response.
	created, err := h.DB.GetEnclosure(r.Context(), enc.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("id", enc.ID).Str("rack_id", rackID).Str("type", req.TypeID).Msg("enclosures: created")
	writeJSON(w, http.StatusCreated, map[string]any{"enclosure": created})
}

// ─── GET /api/v1/enclosures/{id} ─────────────────────────────────────────────

// GetEnclosure fetches a single enclosure with its slot occupancy.
func (h *EnclosuresHandler) GetEnclosure(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	enc, err := h.DB.GetEnclosure(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enclosure": enc})
}

// ─── PUT /api/v1/enclosures/{id} ─────────────────────────────────────────────

// UpdateEnclosure updates the label and/or rack_slot_u.
//
// Body: { "label"?: "blade-chassis-01", "rack_slot_u"?: 5 }
func (h *EnclosuresHandler) UpdateEnclosure(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Label     string `json:"label"`
		RackSlotU int    `json:"rack_slot_u"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	if err := h.DB.UpdateEnclosure(r.Context(), id, req.Label, req.RackSlotU); err != nil {
		log.Error().Err(err).Str("id", id).Msg("enclosures: update")
		writeError(w, err)
		return
	}

	updated, err := h.DB.GetEnclosure(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enclosure": updated})
}

// ─── DELETE /api/v1/enclosures/{id} ──────────────────────────────────────────

// DeleteEnclosure removes a chassis. ON DELETE CASCADE clears all slot rows.
func (h *EnclosuresHandler) DeleteEnclosure(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteEnclosure(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("id", id).Msg("enclosures: deleted")
	w.WriteHeader(http.StatusNoContent)
}

// ─── POST /api/v1/enclosures/{id}/slots/{slot_index} ─────────────────────────

// SetSlot assigns a node to a specific slot in an enclosure.
//
// Body: { "node_id": "<uuid>" }
func (h *EnclosuresHandler) SetSlot(w http.ResponseWriter, r *http.Request) {
	encID := chi.URLParam(r, "id")
	slotIndexStr := chi.URLParam(r, "slot_index")
	slotIndex, err := strconv.Atoi(slotIndexStr)
	if err != nil || slotIndex < 1 {
		writeValidationError(w, "slot_index must be a positive integer")
		return
	}

	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.NodeID == "" {
		writeValidationError(w, "node_id is required")
		return
	}

	if err := h.DB.SetSlotOccupancy(r.Context(), encID, slotIndex, req.NodeID); err != nil {
		log.Error().Err(err).Str("enclosure_id", encID).Int("slot", slotIndex).Msg("enclosures: set slot")
		writeError(w, err)
		return
	}

	enc, err := h.DB.GetEnclosure(r.Context(), encID)
	if err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("enclosure_id", encID).Int("slot", slotIndex).Str("node_id", req.NodeID).Msg("enclosures: slot assigned")
	writeJSON(w, http.StatusOK, map[string]any{"enclosure": enc})
}

// ─── DELETE /api/v1/enclosures/{id}/slots/{slot_index} ───────────────────────

// ClearSlot removes the node from a specific enclosure slot.
func (h *EnclosuresHandler) ClearSlot(w http.ResponseWriter, r *http.Request) {
	encID := chi.URLParam(r, "id")
	slotIndexStr := chi.URLParam(r, "slot_index")
	slotIndex, err := strconv.Atoi(slotIndexStr)
	if err != nil || slotIndex < 1 {
		writeValidationError(w, "slot_index must be a positive integer")
		return
	}

	if err := h.DB.ClearSlotOccupancy(r.Context(), encID, slotIndex); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
