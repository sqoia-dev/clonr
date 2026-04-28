package portal

// PI portal handlers — Sprint C.5
//
// Routes are under /api/v1/portal/pi/ and require the pi role (or admin).
// PI scope is strictly NodeGroup-scoped: a PI can only manage groups where
// node_groups.pi_user_id = their user ID. Admin can manage all.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications"
)

// PIHandler provides HTTP handlers for the PI portal (/api/v1/portal/pi/*).
type PIHandler struct {
	DB    *db.DB
	Audit *db.AuditService

	// AddLDAPMember adds an LDAP account to the given POSIX group.
	// Returns nil on success; returns error if LDAP is unavailable.
	// When nil, requests are queued but LDAP changes are skipped.
	AddLDAPMember func(ctx context.Context, groupName, username string) error

	// RemoveLDAPMember removes an LDAP account from the given POSIX group.
	RemoveLDAPMember func(ctx context.Context, groupName, username string) error

	// Notifier dispatches email notifications. May be nil (no emails sent).
	Notifier *notifications.Notifier
}

// ─── Context helpers ──────────────────────────────────────────────────────────

// piUserIDFromContext returns the clustr user ID of the authenticated PI,
// set by the portal middleware (same ctxKeyPortalUID as the researcher portal).
func piUserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyPortalUID{}).(string)
	return v
}

// piRoleFromContext returns the role of the authenticated user.
// Set in context by the server middleware (ctxKeyUserRole in server package).
// We read it through a typed key that the portal middleware re-sets.
type ctxKeyPIRole struct{}

// ─── Groups list ──────────────────────────────────────────────────────────────

// HandleListGroups handles GET /api/v1/portal/pi/groups.
// Returns the NodeGroups owned by the authenticated PI.
// Admin users see all groups (admin can manage all PI data).
func (h *PIHandler) HandleListGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := piUserIDFromContext(ctx)
	if userID == "" {
		writeError(w, "cannot determine PI identity", http.StatusUnprocessableEntity)
		return
	}

	role, _ := ctx.Value(ctxKeyPIRole{}).(string)
	var groups []db.NodeGroupSummary
	var err error
	if role == "admin" {
		// Admin sees all groups.
		var listErr error
		groups, listErr = h.DB.ListAllNodeGroupSummaries(ctx)
		if listErr != nil {
			log.Error().Err(listErr).Msg("pi: list all node groups failed")
			writeError(w, "failed to list groups", http.StatusInternalServerError)
			return
		}
	} else {
		groups, err = h.DB.ListNodeGroupsByPI(ctx, userID)
		if err != nil {
			log.Error().Err(err).Str("pi_user_id", userID).Msg("pi: list groups failed")
			writeError(w, "failed to list groups", http.StatusInternalServerError)
			return
		}
	}
	if groups == nil {
		groups = []db.NodeGroupSummary{}
	}

	// Marshal with a typed array (not interface{}).
	type groupResp struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Description   string    `json:"description"`
		Role          string    `json:"role,omitempty"`
		NodeCount     int       `json:"node_count"`
		DeployedCount int       `json:"deployed_count"`
		PIUserID      string    `json:"pi_user_id,omitempty"`
		PIUsername    string    `json:"pi_username,omitempty"`
		CreatedAt     time.Time `json:"created_at"`
		UpdatedAt     time.Time `json:"updated_at"`
	}
	out := make([]groupResp, len(groups))
	for i, g := range groups {
		out[i] = groupResp{
			ID:            g.ID,
			Name:          g.Name,
			Description:   g.Description,
			Role:          g.Role,
			NodeCount:     g.NodeCount,
			DeployedCount: g.DeployedCount,
			PIUserID:      g.PIUserID,
			PIUsername:    g.PIUsername,
			CreatedAt:     g.CreatedAt,
			UpdatedAt:     g.UpdatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": out, "total": len(out)})
}

// HandleGetGroupUtilization handles GET /api/v1/portal/pi/groups/{id}/utilization.
// Returns aggregated stats for the group. PI can only access their own groups.
func (h *PIHandler) HandleGetGroupUtilization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Ownership check (skip for admin).
	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	util, err := h.DB.GetPIGroupUtilization(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("pi: get utilization failed")
		writeError(w, "failed to load utilization", http.StatusInternalServerError)
		return
	}

	// HTMX partial path.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderUtilizationPartial(w, util)
		return
	}

	// Surface gaps explicitly per spec.
	partitionState := util.PartitionState
	if partitionState == "" {
		partitionState = "unavailable"
	}

	var lastDeployAt interface{} = nil
	if util.LastDeployAt != nil {
		lastDeployAt = util.LastDeployAt.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id":           util.GroupID,
		"group_name":         util.GroupName,
		"node_count":         util.NodeCount,
		"deployed_count":     util.DeployedCount,
		"undeployed_count":   util.UndeployedCount,
		"last_deploy_at":     lastDeployAt,
		"failed_deploys_30d": util.FailedDeploys30d,
		"member_count":       util.MemberCount,
		"partition_state":    partitionState,
		// Explicit gap annotation per C.5 spec.
		"data_gaps": []string{"member_count_source=pi_requests_only;may_not_reflect_ldap_state"},
	})
}

// ─── Member management ────────────────────────────────────────────────────────

// HandleAddMember handles POST /api/v1/portal/pi/groups/{id}/members.
// Body: { "ldap_username": "jsmith" }
// If CLUSTR_PI_AUTO_APPROVE=true or the DB flag is set: LDAP group add happens
// immediately. Otherwise a pending request is created for admin approval.
func (h *PIHandler) HandleAddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Ownership check.
	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	var body struct {
		LDAPUsername string `json:"ldap_username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.LDAPUsername = strings.TrimSpace(strings.ToLower(body.LDAPUsername))
	if body.LDAPUsername == "" {
		writeError(w, "ldap_username is required", http.StatusBadRequest)
		return
	}

	// Check auto-approve: env var or DB flag.
	autoApprove := os.Getenv("CLUSTR_PI_AUTO_APPROVE") == "true" || h.DB.GetPIAutoApprove(ctx)

	// Fetch group name for LDAP group membership.
	summary, err := h.DB.GetNodeGroupSummary(ctx, groupID)
	if err != nil {
		writeError(w, "group not found", http.StatusNotFound)
		return
	}

	requestID := fmt.Sprintf("pireq-%d", time.Now().UnixNano())
	req := db.PIMemberRequest{
		ID:           requestID,
		GroupID:      groupID,
		PIUserID:     userID,
		LDAPUsername: body.LDAPUsername,
		RequestedAt:  time.Now().UTC(),
	}

	if err := h.DB.CreatePIMemberRequest(ctx, req); err != nil {
		log.Error().Err(err).Msg("pi: create member request failed")
		writeError(w, "failed to create member request", http.StatusInternalServerError)
		return
	}

	// Audit log.
	h.Audit.Record(ctx, userID, "pi:"+userID, db.AuditActionGroupMemberAdd,
		"node_group", groupID, r.RemoteAddr,
		nil, map[string]string{"ldap_username": body.LDAPUsername, "mode": map[bool]string{true: "auto_approve", false: "pending"}[autoApprove]},
	)

	if autoApprove {
		// Attempt LDAP group add immediately.
		ldapErr := ""
		if h.AddLDAPMember != nil {
			if err := h.AddLDAPMember(ctx, summary.Name, body.LDAPUsername); err != nil {
				log.Warn().Err(err).Str("group", summary.Name).Str("user", body.LDAPUsername).Msg("pi: LDAP group add failed")
				ldapErr = err.Error()
			}
		}

		// Mark request as approved.
		_ = h.DB.ResolvePIMemberRequest(ctx, requestID, "approved", userID)

		// Notify the member they have been added (best-effort).
		if h.Notifier != nil {
			notifier := h.Notifier
			gName := summary.Name
			go func() {
				notifier.NotifyMemberAdded(context.Background(), body.LDAPUsername, body.LDAPUsername, gName, userID)
			}()
		}

		if ldapErr != "" {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":      "approved",
				"request_id":  requestID,
				"ldap_status": "ldap_error",
				"ldap_error":  ldapErr,
				"note":        "Request approved but LDAP group add failed — contact admin",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":     "approved",
			"request_id": requestID,
		})
		return
	}

	// Manual approval mode.
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":     "pending",
		"request_id": requestID,
		"note":       "Your request has been submitted. An admin will review it shortly.",
	})
}

// HandleRemoveMember handles DELETE /api/v1/portal/pi/groups/{id}/members/{username}.
// Removes the LDAP username from the group (deactivates if no other groups).
// Only approved requests are considered; the removal is direct (no approval step).
func (h *PIHandler) HandleRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	username := chi.URLParam(r, "username")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Ownership check.
	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	summary, err := h.DB.GetNodeGroupSummary(ctx, groupID)
	if err != nil {
		writeError(w, "group not found", http.StatusNotFound)
		return
	}

	// Remove from LDAP group.
	if h.RemoveLDAPMember != nil {
		if err := h.RemoveLDAPMember(ctx, summary.Name, username); err != nil {
			log.Warn().Err(err).Str("group", summary.Name).Str("user", username).Msg("pi: LDAP group remove failed")
			// Non-fatal: record removal in audit log but surface the LDAP error.
			h.Audit.Record(ctx, userID, "pi:"+userID, db.AuditActionGroupMemberRemove,
				"node_group", groupID, r.RemoteAddr,
				map[string]string{"ldap_username": username}, map[string]string{"ldap_error": err.Error()},
			)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"status":      "removed_with_warning",
				"ldap_status": "ldap_error",
				"ldap_error":  err.Error(),
				"note":        "Group membership record removed but LDAP group remove failed — contact admin",
			})
			return
		}
	}

	// Audit.
	h.Audit.Record(ctx, userID, "pi:"+userID, db.AuditActionGroupMemberRemove,
		"node_group", groupID, r.RemoteAddr,
		map[string]string{"ldap_username": username}, nil,
	)

	// Notify the member they have been removed (best-effort).
	if h.Notifier != nil {
		notifier := h.Notifier
		gName := summary.Name
		go func() {
			notifier.NotifyMemberRemoved(context.Background(), username, username, gName, userID)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// HandleListMembers handles GET /api/v1/portal/pi/groups/{id}/members.
// Returns the list of approved members (from pi_member_requests table).
func (h *PIHandler) HandleListMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	// Return all requests (pending + approved) for the group.
	reqs, err := h.DB.ListPIMemberRequests(ctx, groupID, "")
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("pi: list members failed")
		writeError(w, "failed to list members", http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []db.PIMemberRequest{}
	}

	type memberResp struct {
		LDAPUsername string  `json:"ldap_username"`
		Status       string  `json:"status"`
		RequestID    string  `json:"request_id"`
		RequestedAt  string  `json:"requested_at"`
		ResolvedAt   *string `json:"resolved_at,omitempty"`
	}
	out := make([]memberResp, len(reqs))
	for i, req := range reqs {
		m := memberResp{
			LDAPUsername: req.LDAPUsername,
			Status:       req.Status,
			RequestID:    req.ID,
			RequestedAt:  req.RequestedAt.UTC().Format(time.RFC3339),
		}
		if req.ResolvedAt != nil {
			s := req.ResolvedAt.UTC().Format(time.RFC3339)
			m.ResolvedAt = &s
		}
		out[i] = m
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"members": out, "total": len(out)})
}

// ─── Expansion requests ───────────────────────────────────────────────────────

// HandleRequestExpansion handles POST /api/v1/portal/pi/groups/{id}/expansion-requests.
// Body: { "justification": "..." }
func (h *PIHandler) HandleRequestExpansion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	if role != "admin" {
		owned, err := h.DB.IsNodeGroupOwnedByPI(ctx, groupID, userID)
		if err != nil {
			writeError(w, "failed to verify group ownership", http.StatusInternalServerError)
			return
		}
		if !owned {
			writeError(w, "you do not own this group", http.StatusForbidden)
			return
		}
	}

	var body struct {
		Justification string `json:"justification"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	body.Justification = strings.TrimSpace(body.Justification)
	if body.Justification == "" {
		writeError(w, "justification is required", http.StatusBadRequest)
		return
	}

	req := db.PIExpansionRequest{
		ID:            fmt.Sprintf("piexp-%d", time.Now().UnixNano()),
		GroupID:       groupID,
		PIUserID:      userID,
		Justification: body.Justification,
		RequestedAt:   time.Now().UTC(),
	}
	if err := h.DB.CreatePIExpansionRequest(ctx, req); err != nil {
		log.Error().Err(err).Msg("pi: create expansion request failed")
		writeError(w, "failed to submit expansion request", http.StatusInternalServerError)
		return
	}

	h.Audit.Record(ctx, userID, "pi:"+userID, "pi.expansion_request",
		"node_group", groupID, r.RemoteAddr,
		nil, map[string]string{"request_id": req.ID, "justification": body.Justification},
	)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":     "pending",
		"request_id": req.ID,
		"note":       "Your expansion request has been submitted. An admin will review it.",
	})
}

// ─── Admin-facing PI request management ──────────────────────────────────────

// HandleListPendingMemberRequests handles GET /api/v1/admin/pi/member-requests.
// Admin-only. Lists all pending PI member-add requests.
func (h *PIHandler) HandleListPendingMemberRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}

	reqs, err := h.DB.ListPIMemberRequests(ctx, "", status)
	if err != nil {
		log.Error().Err(err).Msg("admin: list pi member requests failed")
		writeError(w, "failed to list PI member requests", http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []db.PIMemberRequest{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"requests": reqs,
		"total":    len(reqs),
	})
}

// HandleResolveMemberRequest handles POST /api/v1/admin/pi/member-requests/{id}/resolve.
// Body: { "action": "approve" | "deny" }
func (h *PIHandler) HandleResolveMemberRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := chi.URLParam(r, "id")
	adminID, _ := ctx.Value(ctxKeyPortalUID{}).(string)

	var body struct {
		Action string `json:"action"` // "approve" or "deny"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var newStatus string
	switch body.Action {
	case "approve":
		newStatus = "approved"
	case "deny":
		newStatus = "denied"
	default:
		writeError(w, "action must be 'approve' or 'deny'", http.StatusBadRequest)
		return
	}

	req, err := h.DB.GetPIMemberRequest(ctx, requestID)
	if err != nil {
		writeError(w, "request not found", http.StatusNotFound)
		return
	}

	if err := h.DB.ResolvePIMemberRequest(ctx, requestID, newStatus, adminID); err != nil {
		log.Error().Err(err).Str("request_id", requestID).Msg("admin: resolve pi request failed")
		writeError(w, "failed to resolve request", http.StatusInternalServerError)
		return
	}

	// If approving, trigger LDAP group add.
	var groupName string
	var piUsername string
	summary, gErr := h.DB.GetNodeGroupSummary(ctx, req.GroupID)
	if gErr == nil {
		groupName = summary.Name
		piUsername = summary.PIUsername
		if newStatus == "approved" && h.AddLDAPMember != nil {
			if lErr := h.AddLDAPMember(ctx, groupName, req.LDAPUsername); lErr != nil {
				log.Warn().Err(lErr).Str("group", groupName).Str("user", req.LDAPUsername).
					Msg("admin: LDAP group add on PI approval failed")
			}
		}
	}

	h.Audit.Record(ctx, adminID, "admin:"+adminID, "pi.member_request."+body.Action,
		"pi_member_request", requestID, r.RemoteAddr,
		map[string]string{"status": "pending"}, map[string]string{"status": newStatus},
	)

	// Fire-and-forget notification to the PI whose request was resolved.
	if h.Notifier != nil && piUsername != "" {
		notifier := h.Notifier
		go func() {
			bgCtx := context.Background()
			switch newStatus {
			case "approved":
				notifier.NotifyPIRequestApproved(bgCtx, piUsername, req.LDAPUsername, groupName, adminID)
			case "denied":
				notifier.NotifyPIRequestDenied(bgCtx, piUsername, req.LDAPUsername, groupName, adminID)
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": newStatus})
}

// HandleListPendingExpansionRequests handles GET /api/v1/admin/pi/expansion-requests.
func (h *PIHandler) HandleListPendingExpansionRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	reqs, err := h.DB.ListPIExpansionRequests(ctx, "", status)
	if err != nil {
		log.Error().Err(err).Msg("admin: list pi expansion requests failed")
		writeError(w, "failed to list PI expansion requests", http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []db.PIExpansionRequest{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"requests": reqs,
		"total":    len(reqs),
	})
}

// HandleResolveExpansionRequest handles POST /api/v1/admin/pi/expansion-requests/{id}/resolve.
// Body: { "action": "acknowledge" | "dismiss" }
func (h *PIHandler) HandleResolveExpansionRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	requestID := chi.URLParam(r, "id")
	adminID, _ := ctx.Value(ctxKeyPortalUID{}).(string)

	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var newStatus string
	switch body.Action {
	case "acknowledge":
		newStatus = "acknowledged"
	case "dismiss":
		newStatus = "dismissed"
	default:
		writeError(w, "action must be 'acknowledge' or 'dismiss'", http.StatusBadRequest)
		return
	}

	if err := h.DB.ResolvePIExpansionRequest(ctx, requestID, newStatus, adminID); err != nil {
		log.Error().Err(err).Str("request_id", requestID).Msg("admin: resolve expansion request failed")
		writeError(w, "failed to resolve expansion request", http.StatusInternalServerError)
		return
	}

	h.Audit.Record(ctx, adminID, "admin:"+adminID, "pi.expansion_request."+body.Action,
		"pi_expansion_request", requestID, r.RemoteAddr,
		map[string]string{"status": "pending"}, map[string]string{"status": newStatus},
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": newStatus})
}

// ─── HTMX partial: group utilization card ────────────────────────────────────

// HandleUtilizationPartial handles GET /api/v1/portal/pi/groups/{id}/utilization
// when HX-Request header is set. Returns an HTML card fragment.
func renderUtilizationPartial(w http.ResponseWriter, util db.PIGroupUtilization) {
	partState := util.PartitionState
	if partState == "" || partState == "unavailable" {
		partState = "unavailable"
	}
	stateClass := "badge badge-neutral"
	switch strings.ToLower(partState) {
	case "up":
		stateClass = "badge badge-deployed"
	case "down", "drain", "drained":
		stateClass = "badge badge-error"
	}

	lastDeploy := "Never deployed"
	if util.LastDeployAt != nil {
		lastDeploy = util.LastDeployAt.Format("2006-01-02 15:04 UTC")
	}

	html := fmt.Sprintf(`<div class="utilization-summary">
  <div class="util-row"><span class="util-label">Total nodes</span><span class="util-value">%d</span></div>
  <div class="util-row"><span class="util-label">Deployed</span><span class="util-value">%d</span></div>
  <div class="util-row"><span class="util-label">Awaiting reimage</span><span class="util-value">%d</span></div>
  <div class="util-row"><span class="util-label">Last deploy</span><span class="util-value">%s</span></div>
  <div class="util-row"><span class="util-label">Failed deploys (30d)</span><span class="util-value">%d</span></div>
  <div class="util-row"><span class="util-label">Members</span><span class="util-value">%d</span></div>
  <div class="util-row"><span class="util-label">Partition state</span><span class="%s">%s</span></div>
</div>`,
		util.NodeCount, util.DeployedCount, util.UndeployedCount,
		escapeHTML(lastDeploy), util.FailedDeploys30d, util.MemberCount,
		stateClass, escapeHTML(partState),
	)
	_, _ = w.Write([]byte(html))
}
