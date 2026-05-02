package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/reimage"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// allowedRoles is the set of accepted role values for node groups.
var allowedRoles = map[string]bool{
	"compute": true,
	"login":   true,
	"storage": true,
	"gpu":     true,
	"admin":   true,
}

// GroupReimageEventStoreIface is the subset of GroupReimageEventStore used by
// NodeGroupsHandler. Defined as an interface to avoid a circular import.
type GroupReimageEventStoreIface interface {
	Publish(event api.GroupReimageEvent)
	Subscribe() (ch <-chan api.GroupReimageEvent, cancel func())
}

// NodeGroupsHandler handles all /api/v1/node-groups routes.
type NodeGroupsHandler struct {
	DB           *db.DB
	Orchestrator *reimage.Orchestrator
	Audit        *db.AuditService
	// GetActorInfo returns (actorID, actorLabel) for audit records.
	GetActorInfo func(r *http.Request) (id, label string)
	// GroupReimageEvents, when non-nil, receives per-node and job-level events
	// for SSE fan-out on GET /api/v1/node-groups/{id}/reimage/events.
	GroupReimageEvents GroupReimageEventStoreIface
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
	if h.Audit != nil {
		aID, aLabel := "", ""
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel, db.AuditActionGroupCreate, "node_group", g.ID,
			r.RemoteAddr, nil, map[string]string{"name": g.Name})
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
	setNodeConfigSunsetHeader(w) // S6-7: "groups" deprecated in NodeConfig, removed in v1.1
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
	if h.Audit != nil {
		aID, aLabel := "", ""
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel, db.AuditActionGroupUpdate, "node_group", id,
			r.RemoteAddr, map[string]string{"name": existing.Name}, map[string]string{"name": g.Name})
	}
	writeJSON(w, http.StatusOK, g)
}

// DeleteNodeGroup handles DELETE /api/v1/node-groups/:id.
func (h *NodeGroupsHandler) DeleteNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, _ := h.DB.GetNodeGroupFull(r.Context(), id)
	if err := h.DB.DeleteNodeGroup(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	if h.Audit != nil {
		aID, aLabel := "", ""
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel, db.AuditActionGroupDelete, "node_group", id,
			r.RemoteAddr, map[string]string{"name": existing.Name}, nil)
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
	setNodeConfigSunsetHeader(w) // S6-7: "groups" deprecated in NodeConfig, removed in v1.1
	writeJSON(w, http.StatusOK, api.GroupMembersResponse{Group: g, Members: members})
}

// RemoveGroupMember handles DELETE /api/v1/node-groups/:id/members/:node_id.
func (h *NodeGroupsHandler) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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

	if h.Audit != nil {
		aID, aLabel := "", ""
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel, db.AuditActionGroupReimage, "node_group", id,
			r.RemoteAddr, nil, map[string]interface{}{
				"job_id":   jobID,
				"image_id": req.ImageID,
			})
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

// SetNodeGroupPI handles PUT /api/v1/node-groups/{id}/pi.
// Admin-only. Assigns or clears the PI user for a NodeGroup.
// Body: { "pi_user_id": "<user-id>" } — pass "" to clear the PI.
func (h *NodeGroupsHandler) SetNodeGroupPI(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")

	var body struct {
		PIUserID string `json:"pi_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "invalid request body", Code: "bad_request"})
		return
	}

	// Verify the group exists.
	if _, err := h.DB.GetNodeGroup(r.Context(), groupID); err != nil {
		writeError(w, fmt.Errorf("%w: node group not found", api.ErrNotFound))
		return
	}

	// If a PI user ID is provided, verify the user exists and has pi role.
	if body.PIUserID != "" {
		user, err := h.DB.GetUser(r.Context(), body.PIUserID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "user not found", Code: "bad_request"})
			return
		}
		if user.Role != db.UserRolePI && user.Role != db.UserRoleAdmin {
			writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
				Error: "user must have pi or admin role to be assigned as PI",
				Code:  "bad_request",
			})
			return
		}
	}

	if err := h.DB.SetNodeGroupPI(r.Context(), groupID, body.PIUserID); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Str("pi_user_id", body.PIUserID).Msg("node_groups: set PI failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "failed to set group PI", Code: "internal_error"})
		return
	}

	// Audit.
	h.Audit.Record(r.Context(), "", "admin", "node_group.pi_assigned",
		"node_group", groupID, r.RemoteAddr,
		nil, map[string]string{"pi_user_id": body.PIUserID},
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleSetExpiration handles PUT /api/v1/node-groups/{id}/expiration.
// Body: {"expires_at": "2026-12-31T00:00:00Z"} (RFC3339, required).
// Admin or PI of the group can set expiration (PI check is enforced at the router
// level via role middleware; this handler only needs admin or pi role).
// Sprint F (v1.5.0): F3 allocation expiration.
func (h *NodeGroupsHandler) HandleSetExpiration(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")

	var body struct {
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if body.ExpiresAt == "" {
		writeValidationError(w, "expires_at is required")
		return
	}
	t, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		writeValidationError(w, "expires_at must be RFC3339 (e.g. 2026-12-31T00:00:00Z)")
		return
	}
	if t.Before(time.Now()) {
		writeValidationError(w, "expires_at must be in the future")
		return
	}
	t = t.UTC()

	// Verify group exists.
	old, err := h.DB.GetNodeGroupFull(r.Context(), groupID)
	if err != nil {
		writeError(w, err)
		return
	}

	if err := h.DB.SetNodeGroupExpiration(r.Context(), groupID, &t); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("node_groups: set expiration failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "failed to set expiration", Code: "internal_error"})
		return
	}

	actorID, actorLabel := "", "admin"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	oldExpStr := ""
	if old.ExpiresAt != nil {
		oldExpStr = old.ExpiresAt.UTC().Format(time.RFC3339)
	}
	h.Audit.Record(r.Context(), actorID, actorLabel,
		db.AuditActionGroupExpirationSet,
		"node_group", groupID, r.RemoteAddr,
		map[string]string{"expires_at": oldExpStr},
		map[string]string{"expires_at": t.Format(time.RFC3339)},
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "expires_at": t.Format(time.RFC3339)})
}

// HandleClearExpiration handles DELETE /api/v1/node-groups/{id}/expiration.
// Removes the expiration date from a node group.
// Sprint F (v1.5.0): F3 allocation expiration.
func (h *NodeGroupsHandler) HandleClearExpiration(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")

	// Verify group exists.
	old, err := h.DB.GetNodeGroupFull(r.Context(), groupID)
	if err != nil {
		writeError(w, err)
		return
	}
	if old.ExpiresAt == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	if err := h.DB.SetNodeGroupExpiration(r.Context(), groupID, nil); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("node_groups: clear expiration failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "failed to clear expiration", Code: "internal_error"})
		return
	}

	actorID, actorLabel := "", "admin"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	h.Audit.Record(r.Context(), actorID, actorLabel,
		db.AuditActionGroupExpirationCleared,
		"node_group", groupID, r.RemoteAddr,
		map[string]string{"expires_at": old.ExpiresAt.UTC().Format(time.RFC3339)},
		nil,
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── G2 — NodeGroup LDAP group access restrictions (Sprint G / CF-40) ─────────

// GetNodeGroupRestrictions handles GET /api/v1/node-groups/{id}/ldap-restrictions.
func (h *NodeGroupsHandler) GetNodeGroupRestrictions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	groups, err := h.DB.GetNodeGroupAllowedLDAPGroups(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id":            id,
		"allowed_ldap_groups": groups,
	})
}

// SetNodeGroupRestrictions handles PUT /api/v1/node-groups/{id}/ldap-restrictions.
// Replaces the allowed_ldap_groups list. Pass [] to clear (open access).
func (h *NodeGroupsHandler) SetNodeGroupRestrictions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		AllowedLDAPGroups []string `json:"allowed_ldap_groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	if _, err := h.DB.GetNodeGroupFull(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	if err := h.DB.SetNodeGroupAllowedLDAPGroups(r.Context(), id, req.AllowedLDAPGroups); err != nil {
		log.Error().Err(err).Str("group_id", id).Msg("node_groups: set ldap restrictions failed")
		writeError(w, err)
		return
	}

	if h.Audit != nil {
		aID, aLabel := "", "admin"
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(r.Context(), aID, aLabel,
			"node_group.ldap_restrictions.set",
			"node_group", id, r.RemoteAddr,
			nil,
			map[string]interface{}{"allowed_ldap_groups": req.AllowedLDAPGroups},
		)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id":            id,
		"allowed_ldap_groups": req.AllowedLDAPGroups,
	})
}

// StreamGroupReimageEvents handles
// GET /api/v1/node-groups/{id}/reimage/events?job_id=<jid>
//
// Streams api.GroupReimageEvent as Server-Sent Events for the given job.
// The stream terminates after receiving a reimage.completed event for the
// requested job, or when the client disconnects.
func (h *NodeGroupsHandler) StreamGroupReimageEvents(w http.ResponseWriter, r *http.Request) {
	if h.GroupReimageEvents == nil {
		http.Error(w, "group reimage events not configured", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		writeValidationError(w, "job_id query parameter is required")
		return
	}

	ch, cancel := h.GroupReimageEvents.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, open := <-ch:
			if !open {
				return
			}
			// Filter: only forward events for this job.
			if event.JobID != jobID {
				continue
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", string(event.Kind), data)
			flusher.Flush()
			// Terminal event — close the stream.
			if event.Kind == api.GroupReimageEventCompleted {
				return
			}
		}
	}
}
