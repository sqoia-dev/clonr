package handlers

// node_sudoers.go — per-node sudoer assignment endpoints (Sprint 7, NODE-SUDO-1..3).
//
// GET    /api/v1/nodes/{id}/sudoers           — list sudoers for a node
// POST   /api/v1/nodes/{id}/sudoers           — add a sudoer
// DELETE /api/v1/nodes/{id}/sudoers/{uid}     — remove a sudoer
// POST   /api/v1/nodes/{id}/sudoers/sync      — sync sudoers to node (no-op for now)

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// NodeSudoersHandler handles per-node sudoer management.
type NodeSudoersHandler struct {
	DB           *db.DB
	Audit        *db.AuditService
	GetActorInfo func(r *http.Request) (id, label string)
}

type nodeSudoerRequest struct {
	UserIdentifier string `json:"user_identifier"`
	Source         string `json:"source"`   // "ldap" | "local"
	Commands       string `json:"commands"` // default "ALL"
}

// HandleList handles GET /api/v1/nodes/{id}/sudoers.
func (h *NodeSudoersHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	sudoers, err := h.DB.NodeSudoersListByNode(r.Context(), nodeID)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("node_sudoers: list failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if sudoers == nil {
		sudoers = []db.NodeSudoer{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sudoers": sudoers, "total": len(sudoers)})
}

// HandleAdd handles POST /api/v1/nodes/{id}/sudoers.
func (h *NodeSudoersHandler) HandleAdd(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")

	var req nodeSudoerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserIdentifier == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_identifier is required"})
		return
	}
	if req.Source == "" {
		req.Source = "local"
	}
	if req.Source != "ldap" && req.Source != "local" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source must be 'ldap' or 'local'"})
		return
	}
	if req.Commands == "" {
		req.Commands = "ALL"
	}

	actorID := ""
	if h.GetActorInfo != nil {
		actorID, _ = h.GetActorInfo(r)
	}

	s := db.NodeSudoer{
		NodeID:         nodeID,
		UserIdentifier: req.UserIdentifier,
		Source:         req.Source,
		Commands:       req.Commands,
		AssignedBy:     actorID,
	}
	if err := h.DB.NodeSudoersAdd(r.Context(), s); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("node_sudoers: add failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	if h.Audit != nil {
		aLabel := ""
		if h.GetActorInfo != nil {
			_, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), actorID, aLabel, db.AuditActionNodeUpdate, "node", nodeID,
			r.RemoteAddr, nil, map[string]string{"sudoer_added": req.UserIdentifier})
	}

	writeJSON(w, http.StatusCreated, s)
}

// HandleRemove handles DELETE /api/v1/nodes/{id}/sudoers/{uid}.
func (h *NodeSudoersHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	userIdentifier, err := url.PathUnescape(chi.URLParam(r, "uid"))
	if err != nil {
		userIdentifier = chi.URLParam(r, "uid")
	}

	if err := h.DB.NodeSudoersRemove(r.Context(), nodeID, userIdentifier); err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("node_sudoers: remove failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	if h.Audit != nil {
		actorID, aLabel := "", ""
		if h.GetActorInfo != nil {
			actorID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), actorID, aLabel, db.AuditActionNodeUpdate, "node", nodeID,
			r.RemoteAddr, map[string]string{"sudoer_removed": userIdentifier}, nil)
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleSync handles POST /api/v1/nodes/{id}/sudoers/sync.
// The node will receive the current sudoers state on next reimage; this endpoint
// is a no-op placeholder for future live-push via clientd.
func (h *NodeSudoersHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	sudoers, err := h.DB.NodeSudoersListByNode(r.Context(), nodeID)
	if err != nil {
		log.Error().Err(err).Str("node_id", nodeID).Msg("node_sudoers: sync list failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "queued",
		"count":   len(sudoers),
		"message": "Sudoers will be applied on next deploy. Live push via clientd available in a future release.",
	})
}
