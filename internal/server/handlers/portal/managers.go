// managers.go — PI manager delegation handlers (Sprint G / CF-09 manager scope).
//
// A PI can delegate management rights on their NodeGroup to other users.
// Delegated managers have the same per-project rights as the PI:
//   - view utilization, manage members, submit allocation requests, set expiration
//
// They are NOT the project owner and cannot:
//   - delete the NodeGroup, change PI ownership, change visibility defaults
//
// Routes are under /api/v1/portal/pi/groups/{id}/managers.
// The middleware already restricts these to pi role (or admin).
package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications"
)

// ManagerDelegationHandler provides HTTP handlers for PI manager delegation.
type ManagerDelegationHandler struct {
	DB       *db.DB
	Audit    *db.AuditService
	Notifier *notifications.Notifier

	// GetActorInfo returns (actorID, actorLabel) for audit records.
	GetActorInfo func(r *http.Request) (id, label string)
}

// HandleListManagers handles GET /api/v1/portal/pi/groups/{id}/managers.
// Returns all delegated managers for a NodeGroup.
// PI (owner) and admin can call this.
func (h *ManagerDelegationHandler) HandleListManagers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// PI can only manage their own groups; admin can manage all.
	if role != "admin" {
		ok, err := h.DB.IsProjectManagerOrPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to check access", http.StatusInternalServerError)
			return
		}
		if !ok {
			writeError(w, "not authorized", http.StatusForbidden)
			return
		}
	}

	managers, err := h.DB.ListProjectManagersForGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("managers: list failed")
		writeError(w, "failed to list managers", http.StatusInternalServerError)
		return
	}
	if managers == nil {
		managers = []db.ProjectManager{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id": groupID,
		"managers": managers,
		"total":    len(managers),
	})
}

// HandleAddManager handles POST /api/v1/portal/pi/groups/{id}/managers.
// PI (or admin) can delegate management to a clustr user.
// Body: {"user_id": "<uuid>"}
func (h *ManagerDelegationHandler) HandleAddManager(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Only the PI owner or admin can add managers.
	if role != "admin" {
		// Check they are the PI owner (not just a delegated manager).
		ng, err := h.DB.GetNodeGroupSummary(ctx, groupID)
		if err != nil {
			writeError(w, "group not found", http.StatusNotFound)
			return
		}
		if ng.PIUserID != userID {
			writeError(w, "only the group PI can delegate managers", http.StatusForbidden)
			return
		}
	}

	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" {
		writeError(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Prevent a PI from delegating themselves (they already have full access).
	if req.UserID == userID && role != "admin" {
		writeError(w, "you are already the PI — self-delegation is not permitted", http.StatusBadRequest)
		return
	}

	pm, err := h.DB.AddProjectManager(ctx, groupID, req.UserID, userID)
	if err != nil {
		// SQLite UNIQUE violation means already delegated — return the existing state.
		existing, listErr := h.DB.ListProjectManagersForGroup(ctx, groupID)
		if listErr == nil {
			for _, m := range existing {
				if m.UserID == req.UserID {
					writeJSON(w, http.StatusOK, m)
					return
				}
			}
		}
		log.Error().Err(err).Str("group_id", groupID).Str("user_id", req.UserID).
			Msg("managers: add failed")
		writeError(w, "failed to add manager", http.StatusInternalServerError)
		return
	}

	// Audit.
	if h.Audit != nil {
		aID, aLabel := userID, "pi"
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(ctx, aID, aLabel,
			"pi.manager.grant",
			"node_group", groupID, r.RemoteAddr,
			nil,
			map[string]string{"delegated_user_id": req.UserID},
		)
	}

	// Notification: tell the delegated user (best-effort, never blocks).
	if h.Notifier != nil {
		go h.Notifier.NotifyManagerGranted(context.Background(), pm.Username, pm.Username, pm.NodeGroupName, pm.GrantedByName)
	}

	writeJSON(w, http.StatusCreated, pm)
}

// HandleRemoveManager handles DELETE /api/v1/portal/pi/groups/{id}/managers/{userID}.
// Revokes a manager delegation.
func (h *ManagerDelegationHandler) HandleRemoveManager(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	targetUserID := chi.URLParam(r, "userID")

	callerUserID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Only the PI owner or admin can revoke managers.
	if role != "admin" {
		ng, err := h.DB.GetNodeGroupSummary(ctx, groupID)
		if err != nil {
			writeError(w, "group not found", http.StatusNotFound)
			return
		}
		if ng.PIUserID != callerUserID {
			writeError(w, "only the group PI can revoke manager delegation", http.StatusForbidden)
			return
		}
	}

	if err := h.DB.RemoveProjectManager(ctx, groupID, targetUserID); err != nil {
		if errors.Is(err, db.ErrManagerNotFound) {
			writeError(w, "manager delegation not found", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("group_id", groupID).Str("target_user_id", targetUserID).
			Msg("managers: remove failed")
		writeError(w, "failed to remove manager", http.StatusInternalServerError)
		return
	}

	// Audit.
	if h.Audit != nil {
		aID, aLabel := callerUserID, "pi"
		if h.GetActorInfo != nil {
			aID, aLabel = h.GetActorInfo(r)
		}
		h.Audit.Record(ctx, aID, aLabel,
			"pi.manager.revoke",
			"node_group", groupID, r.RemoteAddr,
			map[string]string{"revoked_user_id": targetUserID},
			nil,
		)
	}

	// Notification: tell the PI when an admin revokes a delegation on their behalf.
	if h.Notifier != nil && role == "admin" {
		ng, ngErr := h.DB.GetNodeGroupSummary(ctx, groupID)
		if ngErr == nil && ng.PIUsername != "" {
			go h.Notifier.NotifyManagerRevoked(context.Background(), ng.PIUsername, ng.Name, "admin", targetUserID)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleListManagedGroups handles GET /api/v1/portal/pi/managed-groups.
// Returns all NodeGroups where the authenticated user is a delegated manager.
// This allows managers to see the groups they can act on.
func (h *ManagerDelegationHandler) HandleListManagedGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := piUserIDFromContext(ctx)
	if userID == "" {
		writeError(w, "cannot determine user identity", http.StatusUnprocessableEntity)
		return
	}

	groups, err := h.DB.ListManagedGroupsForUser(ctx, userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("managers: list managed groups failed")
		writeError(w, "failed to list managed groups", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []db.NodeGroupSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"groups": groups,
		"total":  len(groups),
	})
}
