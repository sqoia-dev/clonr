// pi.go — PI portal handler skeleton after PI-CODE-WIPE (Sprint 43-prime Day 2).
//
// The PI workflow handlers (member management, expansion requests, utilization)
// were removed when pi_member_requests, pi_expansion_requests, and
// portal_config.pi_auto_approve were dropped in migration 119.
//
// This file retains:
//   - PIHandler struct  — still used as the receiver for grants, publications,
//     review_cycles, managers, ACR, FOS, attribute-visibility handlers.
//   - ctxKeyPIRole      — still set by PI portal middleware in server.go.
//   - piUserIDFromContext — shared helper consumed by all PI sub-handlers.
package portal

import (
	"context"
	"net/http"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications"
)

// PIHandler provides HTTP handlers for the PI portal (/api/v1/portal/pi/*).
type PIHandler struct {
	DB    *db.DB
	Audit *db.AuditService

	// AddLDAPMember adds an LDAP account to the given POSIX group.
	// Returns nil on success; returns error if LDAP is unavailable.
	// When nil, LDAP changes are skipped.
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

// ctxKeyPIRole is the context key for the authenticated user's role,
// re-set by the PI portal middleware. Still consumed by grants, publications,
// review_cycles, managers, ACR, FOS, and attribute-visibility handlers.
type ctxKeyPIRole struct{}

// ─── Groups list ──────────────────────────────────────────────────────────────

// HandleListGroups handles GET /api/v1/portal/pi/groups.
// Returns the NodeGroups managed by the authenticated PI (via project_managers).
// Admin users see all groups.
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
		groups, err = h.DB.ListAllNodeGroupSummaries(ctx)
	} else {
		groups, err = h.DB.ListNodeGroupsByPI(ctx, userID)
	}
	if err != nil {
		writeError(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []db.NodeGroupSummary{}
	}

	type groupResp struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description,omitempty"`
		Role          string `json:"role,omitempty"`
		NodeCount     int    `json:"node_count"`
		DeployedCount int    `json:"deployed_count"`
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
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": out, "total": len(out)})
}
