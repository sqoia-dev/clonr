package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/internal/db"
	"github.com/sqoia-dev/clonr/internal/reimage"
)

// allowedRoles is the set of accepted role values for node groups.
var allowedRoles = map[string]bool{
	"compute": true,
	"login":   true,
	"storage": true,
	"gpu":     true,
	"admin":   true,
}

// NodeGroupsHandler handles all /api/v1/node-groups routes.
type NodeGroupsHandler struct {
	DB           *db.DB
	Orchestrator *reimage.Orchestrator
}

// ListNodeGroups handles GET /api/v1/node-groups.
// Returns groups with live member counts from the memberships table.
func (h *NodeGroupsHandler) ListNodeGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.DB.ListNodeGroupsWithCount(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("list node groups")
		writeError(w, err)
		return
	}
	if groups == nil {
		groups = []api.NodeGroupWithCount{}
	}
	writeJSON(w, http.StatusOK, api.ListNodeGroupsResponse{Groups: groups, Total: len(groups)})
}

// CreateNodeGroup handles POST /api/v1/node-groups.
func (h *NodeGroupsHandler) CreateNodeGroup(w http.ResponseWriter, r *http.Request) {
	var req api.CreateNodeGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if req.Role != "" && !allowedRoles[req.Role] {
		writeValidationError(w, fmt.Sprintf("role %q is not valid; allowed: compute, login, storage, gpu, admin", req.Role))
		return
	}
	for i, m := range req.ExtraMounts {
		if err := api.ValidateFstabEntry(m); err != nil {
			writeValidationError(w, fmt.Sprintf("extra_mounts[%d]: %s", i, err.Error()))
			return
		}
	}

	now := time.Now().UTC()
	g := api.NodeGroup{
		ID:                 uuid.New().String(),
		Name:               req.Name,
		Description:        req.Description,
		Role:               req.Role,
		DiskLayoutOverride: req.DiskLayoutOverride,
		ExtraMounts:        req.ExtraMounts,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if err := h.DB.CreateNodeGroupFull(r.Context(), g); err != nil {
		log.Error().Err(err).Msg("create node group")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// GetNodeGroup handles GET /api/v1/node-groups/:id.
// Returns the group detail including the member list.
func (h *NodeGroupsHandler) GetNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	g, err := h.DB.GetNodeGroupFull(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	members, err := h.DB.ListGroupMembers(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("group_id", id).Msg("list group members")
		writeError(w, err)
		return
	}
	if members == nil {
		members = []api.NodeConfig{}
	}
	writeJSON(w, http.StatusOK, api.GroupMembersResponse{Group: g, Members: members})
}

// UpdateNodeGroup handles PUT /api/v1/node-groups/:id.
func (h *NodeGroupsHandler) UpdateNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req api.UpdateNodeGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeValidationError(w, "name is required")
		return
	}
	if req.Role != "" && !allowedRoles[req.Role] {
		writeValidationError(w, fmt.Sprintf("role %q is not valid; allowed: compute, login, storage, gpu, admin", req.Role))
		return
	}

	// Confirm existence.
	existing, err := h.DB.GetNodeGroupFull(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	for i, m := range req.ExtraMounts {
		if err := api.ValidateFstabEntry(m); err != nil {
			writeValidationError(w, fmt.Sprintf("extra_mounts[%d]: %s", i, err.Error()))
			return
		}
	}

	override := req.DiskLayoutOverride
	if req.ClearLayoutOverride {
		override = nil
	}

	g := api.NodeGroup{
		ID:                 id,
		Name:               req.Name,
		Description:        req.Description,
		Role:               req.Role,
		DiskLayoutOverride: override,
		ExtraMounts:        req.ExtraMounts,
		CreatedAt:          existing.CreatedAt,
		UpdatedAt:          time.Now().UTC(),
	}
	if err := h.DB.UpdateNodeGroupFull(r.Context(), g); err != nil {
		log.Error().Err(err).Msg("update node group")
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// DeleteNodeGroup handles DELETE /api/v1/node-groups/:id.
func (h *NodeGroupsHandler) DeleteNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteNodeGroup(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddGroupMembers handles POST /api/v1/node-groups/:id/members.
// Body: {"node_ids": ["uuid1", "uuid2", ...]}.
// Idempotent — adding an already-member node is a no-op.
func (h *NodeGroupsHandler) AddGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Verify group exists.
	if _, err := h.DB.GetNodeGroupFull(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	var req api.AddGroupMembersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if len(req.NodeIDs) == 0 {
		writeValidationError(w, "node_ids must not be empty")
		return
	}

	for _, nodeID := range req.NodeIDs {
		if err := h.DB.AddGroupMember(r.Context(), id, nodeID); err != nil {
			log.Error().Err(err).Str("group_id", id).Str("node_id", nodeID).Msg("add group member")
			writeError(w, err)
			return
		}
	}

	// Return updated member list.
	g, _ := h.DB.GetNodeGroupFull(r.Context(), id)
	members, _ := h.DB.ListGroupMembers(r.Context(), id)
	if members == nil {
		members = []api.NodeConfig{}
	}
	writeJSON(w, http.StatusOK, api.GroupMembersResponse{Group: g, Members: members})
}

// RemoveGroupMember handles DELETE /api/v1/node-groups/:id/members/:node_id.
func (h *NodeGroupsHandler) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	id     := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "node_id")

	if err := h.DB.RemoveGroupMember(r.Context(), id, nodeID); err != nil {
		log.Error().Err(err).Str("group_id", id).Str("node_id", nodeID).Msg("remove group member")
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReimageGroup handles POST /api/v1/node-groups/:id/reimage.
// Kicks off a rolling group reimage and returns a job ID for polling.
func (h *NodeGroupsHandler) ReimageGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req api.GroupReimageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.ImageID == "" {
		writeValidationError(w, "image_id is required")
		return
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 5
	}
	if req.PauseOnFailurePct <= 0 {
		req.PauseOnFailurePct = 20
	}

	if h.Orchestrator == nil {
		writeError(w, fmt.Errorf("reimage orchestrator not configured"))
		return
	}

	jobID, err := h.Orchestrator.TriggerGroupReimage(r.Context(), id, req.ImageID, req.Concurrency, req.PauseOnFailurePct)
	if err != nil {
		log.Error().Err(err).Str("group_id", id).Msg("trigger group reimage")
		writeError(w, err)
		return
	}

	job, err := h.DB.GetGroupReimageJob(r.Context(), jobID)
	if err != nil {
		// Job was created but we can't read it back — return the ID at minimum.
		writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
		return
	}

	writeJSON(w, http.StatusAccepted, jobToStatus(job))
}

// GetGroupReimageJob handles GET /api/v1/reimages/jobs/:jobID.
func (h *NodeGroupsHandler) GetGroupReimageJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	job, err := h.DB.GetGroupReimageJob(r.Context(), jobID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobToStatus(job))
}

// ResumeGroupReimageJob handles POST /api/v1/reimages/jobs/:jobID/resume.
func (h *NodeGroupsHandler) ResumeGroupReimageJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := h.DB.GetGroupReimageJob(r.Context(), jobID)
	if err != nil {
		writeError(w, err)
		return
	}
	if job.Status != "paused" {
		writeValidationError(w, fmt.Sprintf("job %s is not paused (status: %s)", jobID, job.Status))
		return
	}

	if err := h.DB.ResumeGroupReimageJob(r.Context(), jobID); err != nil {
		log.Error().Err(err).Str("job_id", jobID).Msg("resume group reimage job")
		writeError(w, err)
		return
	}

	job.Status = "running"
	writeJSON(w, http.StatusOK, jobToStatus(job))
}

// jobToStatus converts a db.GroupReimageJob to an api.GroupReimageJobStatus.
func jobToStatus(j db.GroupReimageJob) api.GroupReimageJobStatus {
	return api.GroupReimageJobStatus{
		JobID:             j.ID,
		GroupID:           j.GroupID,
		ImageID:           j.ImageID,
		Status:            j.Status,
		TotalNodes:        j.TotalNodes,
		TriggeredNodes:    j.TriggeredNodes,
		SucceededNodes:    j.SucceededNodes,
		FailedNodes:       j.FailedNodes,
		Concurrency:       j.Concurrency,
		PauseOnFailurePct: j.PauseOnFailurePct,
		ErrorMessage:      j.ErrorMessage,
		CreatedAt:         j.CreatedAt,
		UpdatedAt:         j.UpdatedAt,
	}
}
