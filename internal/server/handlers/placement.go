// Package handlers — placement.go implements the unified node placement endpoint
// introduced in Sprint 31 (#231).
//
// Routes:
//
//	PUT    /api/v1/nodes/{node_id}/placement  — set placement (rack-direct or enclosure-slot)
//	DELETE /api/v1/nodes/{node_id}/placement  — remove from any placement
//
// The legacy PUT/DELETE /api/v1/racks/{id}/positions/{node_id} endpoints stay
// for one release (v0.11.0) and forward to these calls internally. They are
// scheduled for removal in v0.12.0 (Sunset: 2026-12-01).
package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/pkg/api"
)

//lint:ignore U1000 re-applied when legacy rack-position endpoints are re-enabled with their Sunset header (PLACEMENT-SUNSET)
// placementSunsetDate is the RFC 1123 date after which the deprecated
// PUT/DELETE /api/v1/racks/{id}/positions/{node_id} endpoints will be removed.
const placementSunsetDate = "Mon, 01 Dec 2026 00:00:00 GMT"

// PlacementDBIface is the subset of *db.DB used by PlacementHandler.
type PlacementDBIface interface {
	GetRack(ctx context.Context, id string) (api.Rack, error)
	GetEnclosure(ctx context.Context, id string) (api.Enclosure, error)
	SetNodePlacementRack(ctx context.Context, nodeID, rackID string, slotU, heightU int) error
	SetNodePlacementEnclosure(ctx context.Context, nodeID, enclosureID string, slotIndex int) error
	ClearNodePlacement(ctx context.Context, nodeID string) error
}

// PlacementHandler handles /api/v1/nodes/{node_id}/placement endpoints.
type PlacementHandler struct {
	DB PlacementDBIface
}

// ─── PUT /api/v1/nodes/{node_id}/placement ────────────────────────────────────

// SetPlacement atomically sets a node's physical placement.
//
// Body (tagged union on "kind"):
//
//	{ "kind": "rack_u",         "rack_id": "…", "slot_u": 3, "height_u": 2 }
//	{ "kind": "enclosure_slot", "enclosure_id": "…", "slot_index": 2 }
//	{ "kind": "unassigned" }
func (h *PlacementHandler) SetPlacement(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")

	var req api.PlacementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	switch req.Kind {
	case "rack_u":
		if req.RackID == "" {
			writeValidationError(w, "rack_id is required for kind=rack_u")
			return
		}
		if req.SlotU <= 0 {
			writeValidationError(w, "slot_u must be >= 1 for kind=rack_u")
			return
		}
		if req.HeightU <= 0 {
			req.HeightU = 1
		}
		// Confirm rack exists.
		if _, err := h.DB.GetRack(r.Context(), req.RackID); err != nil {
			writeError(w, err)
			return
		}
		if err := h.DB.SetNodePlacementRack(r.Context(), nodeID, req.RackID, req.SlotU, req.HeightU); err != nil {
			log.Error().Err(err).Str("node_id", nodeID).Msg("placement: set rack_u")
			writeError(w, err)
			return
		}
		log.Info().Str("node_id", nodeID).Str("rack_id", req.RackID).
			Int("slot_u", req.SlotU).Msg("placement: rack_u set")
		writeJSON(w, http.StatusOK, map[string]any{
			"placement": map[string]any{
				"kind":     "rack_u",
				"node_id":  nodeID,
				"rack_id":  req.RackID,
				"slot_u":   req.SlotU,
				"height_u": req.HeightU,
			},
		})

	case "enclosure_slot":
		if req.EnclosureID == "" {
			writeValidationError(w, "enclosure_id is required for kind=enclosure_slot")
			return
		}
		if req.SlotIndex <= 0 {
			writeValidationError(w, "slot_index must be >= 1 for kind=enclosure_slot")
			return
		}
		if err := h.DB.SetNodePlacementEnclosure(r.Context(), nodeID, req.EnclosureID, req.SlotIndex); err != nil {
			log.Error().Err(err).Str("node_id", nodeID).Msg("placement: set enclosure_slot")
			writeError(w, err)
			return
		}
		log.Info().Str("node_id", nodeID).Str("enclosure_id", req.EnclosureID).
			Int("slot_index", req.SlotIndex).Msg("placement: enclosure_slot set")
		writeJSON(w, http.StatusOK, map[string]any{
			"placement": map[string]any{
				"kind":         "enclosure_slot",
				"node_id":      nodeID,
				"enclosure_id": req.EnclosureID,
				"slot_index":   req.SlotIndex,
			},
		})

	case "unassigned":
		if err := h.DB.ClearNodePlacement(r.Context(), nodeID); err != nil {
			writeError(w, err)
			return
		}
		log.Info().Str("node_id", nodeID).Msg("placement: cleared")
		writeJSON(w, http.StatusOK, map[string]any{
			"placement": map[string]any{
				"kind":    "unassigned",
				"node_id": nodeID,
			},
		})

	default:
		writeValidationError(w, `kind must be one of: "rack_u", "enclosure_slot", "unassigned"`)
	}
}

// ─── DELETE /api/v1/nodes/{node_id}/placement ────────────────────────────────

// DeletePlacement removes a node from any placement. Returns 404 if not placed.
func (h *PlacementHandler) DeletePlacement(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if err := h.DB.ClearNodePlacement(r.Context(), nodeID); err != nil {
		writeError(w, err)
		return
	}
	log.Info().Str("node_id", nodeID).Msg("placement: deleted")
	w.WriteHeader(http.StatusNoContent)
}

// ─── Sunset header helper ─────────────────────────────────────────────────────

//lint:ignore U1000 called by legacy rack-position endpoints when re-enabled with Sunset signalling (PLACEMENT-SUNSET)
// setPlacementSunsetHeader adds the RFC 8594 Sunset header to legacy rack-position
// endpoint responses, signalling the deprecation of those endpoints.
func setPlacementSunsetHeader(w http.ResponseWriter) {
	w.Header().Set("Sunset", placementSunsetDate)
	w.Header().Set("Deprecation", `true; rel="deprecation"`)
	w.Header().Set("Link", `</api/v1/nodes/{node_id}/placement>; rel="successor-version"`)
}
