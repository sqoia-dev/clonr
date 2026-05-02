// Package handlers — racks.go implements the rack model endpoints (#149).
//
// Routes:
//
//	GET    /api/v1/racks                              — list racks (?include=positions)
//	POST   /api/v1/racks                              — create rack
//	GET    /api/v1/racks/{id}                         — fetch one with positions
//	PUT    /api/v1/racks/{id}                         — edit name / height_u
//	DELETE /api/v1/racks/{id}                         — delete (cascades positions)
//	PUT    /api/v1/racks/{id}/positions/{node_id}     — set node position
//	DELETE /api/v1/racks/{id}/positions/{node_id}     — remove node from rack
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
	"github.com/sqoia-dev/clustr/pkg/api"
)

// RacksDBIface is the subset of *db.DB used by RacksHandler.
type RacksDBIface interface {
	CreateRack(ctx context.Context, r api.Rack) error
	GetRack(ctx context.Context, id string) (api.Rack, error)
	ListRacks(ctx context.Context) ([]api.Rack, error)
	UpdateRack(ctx context.Context, id, name string, heightU int) error
	DeleteRack(ctx context.Context, id string) error
	SetNodeRackPosition(ctx context.Context, pos api.NodeRackPosition) error
	DeleteNodeRackPosition(ctx context.Context, nodeID string) error
	ListPositionsByRack(ctx context.Context, rackID string) ([]api.NodeRackPosition, error)
}

// RacksHandler handles /api/v1/racks endpoints.
type RacksHandler struct {
	DB RacksDBIface
}

// ─── GET /api/v1/racks ────────────────────────────────────────────────────────

// ListRacks returns all racks. With ?include=positions each rack has its
// node_rack_position rows embedded.
func (h *RacksHandler) ListRacks(w http.ResponseWriter, r *http.Request) {
	racks, err := h.DB.ListRacks(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("racks: list")
		writeError(w, err)
		return
	}
	if racks == nil {
		racks = []api.Rack{}
	}

	if strings.Contains(r.URL.Query().Get("include"), "positions") {
		for i := range racks {
			positions, pErr := h.DB.ListPositionsByRack(r.Context(), racks[i].ID)
			if pErr != nil {
				log.Error().Err(pErr).Str("rack_id", racks[i].ID).Msg("racks: list positions")
				writeError(w, pErr)
				return
			}
			if positions == nil {
				positions = []api.NodeRackPosition{}
			}
			racks[i].Positions = positions
		}
	}

	writeJSON(w, http.StatusOK, api.ListRacksResponse{Racks: racks, Total: len(racks)})
}

// ─── POST /api/v1/racks ───────────────────────────────────────────────────────

// CreateRack creates a new rack.
//
// Body: { "name": "rack-a", "height_u"?: 42 }
func (h *RacksHandler) CreateRack(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		HeightU int    `json:"height_u"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if req.HeightU <= 0 {
		req.HeightU = 42 // standard rack default
	}

	now := time.Now().UTC()
	rack := api.Rack{
		ID:        uuid.New().String(),
		Name:      req.Name,
		HeightU:   req.HeightU,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.DB.CreateRack(r.Context(), rack); err != nil {
		log.Error().Err(err).Str("name", req.Name).Msg("racks: create")
		writeError(w, err)
		return
	}
	log.Info().Str("id", rack.ID).Str("name", rack.Name).Msg("racks: created")
	writeJSON(w, http.StatusCreated, map[string]any{"rack": rack})
}

// ─── GET /api/v1/racks/{id} ───────────────────────────────────────────────────

// GetRack fetches a single rack with its positions embedded.
func (h *RacksHandler) GetRack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rack, err := h.DB.GetRack(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	positions, err := h.DB.ListPositionsByRack(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("rack_id", id).Msg("racks: list positions")
		writeError(w, err)
		return
	}
	if positions == nil {
		positions = []api.NodeRackPosition{}
	}
	rack.Positions = positions

	writeJSON(w, http.StatusOK, map[string]any{"rack": rack})
}

// ─── PUT /api/v1/racks/{id} ───────────────────────────────────────────────────

// UpdateRack edits the name and/or height_u of an existing rack.
//
// Body: { "name"?: "new-name", "height_u"?: 48 }
func (h *RacksHandler) UpdateRack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Fetch current to apply partial update.
	current, err := h.DB.GetRack(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var req struct {
		Name    string `json:"name"`
		HeightU int    `json:"height_u"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	newName := current.Name
	if req.Name != "" {
		newName = req.Name
	}
	newHeightU := current.HeightU
	if req.HeightU > 0 {
		newHeightU = req.HeightU
	}

	if err := h.DB.UpdateRack(r.Context(), id, newName, newHeightU); err != nil {
		log.Error().Err(err).Str("id", id).Msg("racks: update")
		writeError(w, err)
		return
	}

	updated, err := h.DB.GetRack(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rack": updated})
}

// ─── DELETE /api/v1/racks/{id} ────────────────────────────────────────────────

// DeleteRack removes a rack. Cascades to node_rack_position via FK constraint.
func (h *RacksHandler) DeleteRack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteRack(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("id", id).Msg("racks: deleted")
	w.WriteHeader(http.StatusNoContent)
}

// ─── PUT /api/v1/racks/{id}/positions/{node_id} ───────────────────────────────

// SetPosition assigns or updates a node's U-slot within a rack.
//
// Body: { "slot_u": 3, "height_u"?: 2 }
//
// If a node is already in a different rack, this call moves it.  To check for
// overlap (two nodes occupying the same U range), pass the response through the
// overlap warning emitted in the body.
func (h *RacksHandler) SetPosition(w http.ResponseWriter, r *http.Request) {
	rackID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "node_id")

	// Confirm rack exists.
	if _, err := h.DB.GetRack(r.Context(), rackID); err != nil {
		writeError(w, err)
		return
	}

	var req struct {
		SlotU   int `json:"slot_u"`
		HeightU int `json:"height_u"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.SlotU <= 0 {
		writeValidationError(w, "slot_u must be >= 1")
		return
	}
	if req.HeightU <= 0 {
		req.HeightU = 1
	}

	pos := api.NodeRackPosition{
		NodeID:  nodeID,
		RackID:  rackID,
		SlotU:   req.SlotU,
		HeightU: req.HeightU,
	}
	if err := h.DB.SetNodeRackPosition(r.Context(), pos); err != nil {
		log.Error().Err(err).Str("rack_id", rackID).Str("node_id", nodeID).Msg("racks: set position")
		writeError(w, err)
		return
	}

	// Overlap detection: warn (don't reject) if another node occupies a U in
	// [slot_u, slot_u + height_u - 1]. This is a read-after-write advisory.
	resp := map[string]any{"position": pos}
	if overlap := h.detectOverlap(r.Context(), rackID, nodeID, req.SlotU, req.HeightU); overlap != "" {
		resp["warning"] = overlap
	}

	log.Info().Str("rack_id", rackID).Str("node_id", nodeID).
		Int("slot_u", req.SlotU).Msg("racks: position set")
	writeJSON(w, http.StatusOK, resp)
}

// detectOverlap returns a human-readable warning if any other node in the rack
// occupies a U slot that overlaps with [slotU, slotU+heightU-1].
// Returns empty string when there is no overlap or on any query error.
func (h *RacksHandler) detectOverlap(ctx context.Context, rackID, excludeNodeID string, slotU, heightU int) string {
	positions, err := h.DB.ListPositionsByRack(ctx, rackID)
	if err != nil {
		return "" // non-fatal, skip overlap check
	}
	end := slotU + heightU - 1
	for _, p := range positions {
		if p.NodeID == excludeNodeID {
			continue
		}
		pEnd := p.SlotU + p.HeightU - 1
		// Overlap when ranges intersect: start1 <= end2 AND start2 <= end1.
		if slotU <= pEnd && p.SlotU <= end {
			return "slot overlap detected with node " + p.NodeID + " at U" + itoa(p.SlotU)
		}
	}
	return ""
}

// itoa is a tiny helper to avoid importing strconv just for Itoa in the
// overlap warning string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n >= 10 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	pos--
	buf[pos] = byte('0' + n)
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ─── DELETE /api/v1/racks/{id}/positions/{node_id} ───────────────────────────

// DeletePosition removes a node from its rack assignment.
func (h *RacksHandler) DeletePosition(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if err := h.DB.DeleteNodeRackPosition(r.Context(), nodeID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
