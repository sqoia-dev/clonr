// Package portal provides the HTTP handlers for the researcher portal.
//
// Routes are under /api/v1/portal/ and require at minimum viewer role.
// The portal surfaces:
//   - Researcher's LDAP account info (read-only)
//   - LDAP self-service password change (C1-3)
//   - Slurm partition status (C1-4)
//   - OnDemand portal link config (C1-7)
//   - Storage quota display (C1-8)
package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// LDAPUserInfo is the minimal LDAP account info a viewer can see about themselves.
type LDAPUserInfo struct {
	UID         string `json:"uid"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	// Groups the user is a member of (from LDAP).
	Groups []string `json:"groups"`
}

// PortalStatusResponse is the combined response for GET /api/v1/portal/status.
type PortalStatusResponse struct {
	User        *LDAPUserInfo `json:"user,omitempty"`        // nil when LDAP not configured
	OnDemandURL string        `json:"ondemand_url,omitempty"` // empty when not configured
	// LDAPEnabled reports whether the LDAP module is active.
	LDAPEnabled bool `json:"ldap_enabled"`
}

// QuotaResponse is returned by GET /api/v1/portal/me/quota.
type QuotaResponse struct {
	UsedBytes  *int64  `json:"used_bytes,omitempty"`
	LimitBytes *int64  `json:"limit_bytes,omitempty"`
	UsedRaw    string  `json:"used_raw,omitempty"`   // raw string from LDAP attr
	LimitRaw   string  `json:"limit_raw,omitempty"`  // raw string from LDAP attr
	Configured bool    `json:"configured"`            // false when attrs not mapped
}

// PartitionStatus mirrors the data returned by GET /api/v1/portal/partitions/status.
type PartitionStatus struct {
	Partition      string `json:"partition"`
	State          string `json:"state"`
	TotalNodes     int    `json:"total_nodes"`
	AvailableNodes int    `json:"available_nodes"`
}

// Handler provides HTTP handlers for the researcher portal.
type Handler struct {
	DB       *db.DB
	// GetLDAPUser fetches minimal LDAP user info for a given UID.
	// Returns nil, nil when LDAP module is not enabled.
	GetLDAPUser func(ctx context.Context, uid string) (*LDAPUserInfo, error)
	// SetLDAPPassword changes the LDAP password for uid; verifies currentPassword first.
	SetLDAPPassword func(ctx context.Context, uid, currentPassword, newPassword string) error
	// GetLDAPQuota retrieves the quota attributes for uid.
	GetLDAPQuota func(ctx context.Context, uid string) (*QuotaResponse, error)
	// GetPartitionStatus returns current Slurm partition health.
	// Returns nil when Slurm module is not enabled.
	GetPartitionStatus func(ctx context.Context) ([]PartitionStatus, error)
}

//lint:ignore U1000 wired by portal session middleware in Sprint 40 (PORTAL-SESSION); kept here so the context key and helper are co-located
// userIDFromContext pulls the authenticated user's clustr user ID from context.
// We need this to derive the LDAP username via a DB lookup.
func userIDFromContext(ctx context.Context) string {
	// The clustr user ID is stored in context by the session middleware.
	// We use a type assertion against the same unexported key type used in middleware.go.
	// Since portal is a separate package, we use the exported helper indirectly via
	// the request context. We expose it via a context value set by the portal middleware.
	v, _ := ctx.Value(ctxKeyPortalUID{}).(string)
	return v
}

// ctxKeyPortalUID is the context key set by the portal auth shim.
type ctxKeyPortalUID struct{}

// ctxKeyPortalLDAPUID is the LDAP username (uid) of the authenticated user.
type ctxKeyPortalLDAPUID struct{}

// ldapUIDFromContext pulls the LDAP UID set by the portal middleware shim.
func ldapUIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyPortalLDAPUID{}).(string)
	return v
}

// HandleStatus returns the portal status for the authenticated researcher:
// their LDAP info, OnDemand URL if configured, and LDAP module state.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Resolve OnDemand URL — prefer env var, fall back to DB config.
	ondemandURL := os.Getenv("CLUSTR_ONDEMAND_URL")
	if ondemandURL == "" {
		if cfg, err := h.DB.GetPortalConfig(ctx); err == nil {
			ondemandURL = cfg.OnDemandURL
		}
	}

	resp := PortalStatusResponse{
		OnDemandURL: ondemandURL,
	}

	// Fetch LDAP info for the authenticated user.
	ldapUID := ldapUIDFromContext(ctx)
	if ldapUID != "" && h.GetLDAPUser != nil {
		user, err := h.GetLDAPUser(ctx, ldapUID)
		if err != nil {
			log.Warn().Err(err).Str("ldap_uid", ldapUID).Msg("portal: get LDAP user info failed")
			// Non-fatal — return partial response without LDAP user info.
		} else {
			resp.User = user
			resp.LDAPEnabled = true
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleChangePassword handles POST /api/v1/portal/me/password.
// Validates current password then updates the LDAP password for the logged-in user.
func (h *Handler) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.CurrentPassword == "" {
		writeError(w, "current_password is required", http.StatusBadRequest)
		return
	}
	if body.NewPassword == "" {
		writeError(w, "new_password is required", http.StatusBadRequest)
		return
	}
	if len(body.NewPassword) < 8 {
		writeError(w, "new password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	ldapUID := ldapUIDFromContext(ctx)
	if ldapUID == "" {
		writeError(w, "cannot determine LDAP identity for your session", http.StatusUnprocessableEntity)
		return
	}

	if h.SetLDAPPassword == nil {
		writeError(w, "LDAP module is not enabled", http.StatusServiceUnavailable)
		return
	}

	if err := h.SetLDAPPassword(ctx, ldapUID, body.CurrentPassword, body.NewPassword); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "invalid credentials") || strings.Contains(msg, "Invalid credentials") {
			writeError(w, "Current password is incorrect", http.StatusUnprocessableEntity)
			return
		}
		log.Error().Err(err).Str("ldap_uid", ldapUID).Msg("portal: change password failed")
		writeError(w, "Password change failed: "+msg, http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleGetQuota handles GET /api/v1/portal/me/quota.
// Returns storage quota info from LDAP attributes if configured; configured=false otherwise.
func (h *Handler) HandleGetQuota(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ldapUID := ldapUIDFromContext(ctx)
	if ldapUID == "" || h.GetLDAPQuota == nil {
		writeJSON(w, http.StatusOK, QuotaResponse{Configured: false})
		return
	}

	quota, err := h.GetLDAPQuota(ctx, ldapUID)
	if err != nil {
		log.Warn().Err(err).Str("ldap_uid", ldapUID).Msg("portal: get quota failed")
		writeJSON(w, http.StatusOK, QuotaResponse{Configured: false})
		return
	}
	if quota == nil {
		writeJSON(w, http.StatusOK, QuotaResponse{Configured: false})
		return
	}

	writeJSON(w, http.StatusOK, quota)
}

// HandleGetPartitions handles GET /api/v1/portal/partitions/status.
// Returns Slurm partition health cards for the researcher portal.
// Also serves HTMX HTML partial when HX-Request header is set.
func (h *Handler) HandleGetPartitions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var partitions []PartitionStatus
	if h.GetPartitionStatus != nil {
		var err error
		partitions, err = h.GetPartitionStatus(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("portal: get partition status failed")
			partitions = []PartitionStatus{}
		}
	}
	if partitions == nil {
		partitions = []PartitionStatus{}
	}

	// Content negotiation: HTMX requests get an HTML fragment.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderPartitionsPartial(w, partitions)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"partitions": partitions,
		"total":      len(partitions),
	})
}

// HandleGetConfig handles GET /api/v1/portal/config (admin only).
// Returns the portal configuration (OnDemand URL, quota attributes).
func (h *Handler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.DB.GetPortalConfig(r.Context())
	if err != nil {
		writeError(w, "failed to read portal config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ondemand_url":          cfg.OnDemandURL,
		"ldap_quota_used_attr":  cfg.LDAPQuotaUsedAttr,
		"ldap_quota_limit_attr": cfg.LDAPQuotaLimitAttr,
		"updated_at":            cfg.UpdatedAt.UTC().Format(time.RFC3339),
	})
}

// HandleUpdateConfig handles PUT /api/v1/portal/config (admin only).
func (h *Handler) HandleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OnDemandURL       string `json:"ondemand_url"`
		LDAPQuotaUsedAttr  string `json:"ldap_quota_used_attr"`
		LDAPQuotaLimitAttr string `json:"ldap_quota_limit_attr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.DB.UpdatePortalConfig(r.Context(), db.PortalConfig{
		OnDemandURL:        body.OnDemandURL,
		LDAPQuotaUsedAttr:  body.LDAPQuotaUsedAttr,
		LDAPQuotaLimitAttr: body.LDAPQuotaLimitAttr,
	}); err != nil {
		writeError(w, "failed to update portal config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── HTMX partials ────────────────────────────────────────────────────────────

// renderPartitionsPartial writes an HTML fragment of partition status cards.
// This is the HTMX partial returned when HX-Request: true.
func renderPartitionsPartial(w http.ResponseWriter, partitions []PartitionStatus) {
	if len(partitions) == 0 {
		_, _ = w.Write([]byte(`<div class="empty-state"><div class="empty-state-text">No Slurm partitions found. The Slurm module may not be enabled.</div></div>`))
		return
	}

	for _, p := range partitions {
		stateClass := "badge badge-neutral"
		switch strings.ToLower(p.State) {
		case "up":
			stateClass = "badge badge-deployed"
		case "down", "drain", "drained":
			stateClass = "badge badge-error"
		case "inact":
			stateClass = "badge badge-neutral"
		}

		availRatio := ""
		if p.TotalNodes > 0 {
			availRatio = fmt.Sprintf("%d / %d nodes available", p.AvailableNodes, p.TotalNodes)
		}

		_, _ = w.Write([]byte(`<div class="card" style="margin-bottom:12px">
  <div class="card-header" style="display:flex;align-items:center;gap:10px">
    <strong>` + escapeHTML(p.Partition) + `</strong>
    <span class="` + stateClass + `">` + escapeHTML(p.State) + `</span>
  </div>
  <div class="card-body" style="color:var(--text-secondary);font-size:13px">` + escapeHTML(availRatio) + `</div>
</div>`))
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("portal: encode response failed")
	}
}

func writeError(w http.ResponseWriter, message string, code int) {
	writeJSON(w, code, map[string]string{"error": message})
}

func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&#34;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

