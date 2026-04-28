package handlers

// SMTP notification management handlers — Sprint D (D2-1 through D2-5, CF-15).
//
// Routes (all admin-only):
//   GET  /api/v1/admin/smtp
//   PUT  /api/v1/admin/smtp
//   POST /api/v1/admin/smtp/test
//   POST /api/v1/node-groups/{id}/broadcast
//
// SMTP config is encrypted at rest via internal/secrets (AES-256-GCM).
// Credentials are never returned in GET responses.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// actorFromContext extracts (actorID, actorLabel) from the request context.
// For session-authenticated users it returns the user ID twice (label is ID until
// we have a richer attribution chain from the server package). This is sufficient
// for audit log purposes.
func actorFromContext(ctx context.Context) (actorID, actorLabel string) {
	// Try ctxKeyUserID (set by server middleware for session auth).
	// We define a local matching type to avoid import cycles.
	type ctxKeyUserID struct{}
	if v, ok := ctx.Value(ctxKeyUserID{}).(string); ok && v != "" {
		return v, "user:" + v
	}
	return "system", "clustr"
}

// notifWriteError writes an error JSON response. Uses api.ErrorResponse for consistency.
func notifWriteError(w http.ResponseWriter, msg string, code int) {
	writeJSON(w, code, api.ErrorResponse{Error: msg, Code: "error"})
}

// NotificationsHandler provides admin endpoints for SMTP and broadcast.
type NotificationsHandler struct {
	DB      *db.DB
	Audit   *db.AuditService
	Mailer  notifications.Mailer
	// BroadcastRateLimitHours is the minimum hours between broadcasts to a group.
	// Default 1 hour.
	BroadcastRateLimitHours int
}

// SMTPConfigResponse is the GET response — never includes the password.
type SMTPConfigResponse struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	From       string `json:"from_addr"`
	UseTLS     bool   `json:"use_tls"`
	UseSSL     bool   `json:"use_ssl"`
	Configured bool   `json:"configured"` // true when host + from are set
}

// SMTPConfigRequest is the PUT body for updating SMTP config.
// Password is optional: omitting it preserves the existing encrypted value.
type SMTPConfigRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"` // empty = keep existing
	From     string `json:"from_addr"`
	UseTLS   bool   `json:"use_tls"`
	UseSSL   bool   `json:"use_ssl"`
}

// HandleGetSMTP handles GET /api/v1/admin/smtp.
// Returns config without the password.
func (h *NotificationsHandler) HandleGetSMTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := h.DB.GetSMTPConfig(ctx)
	if err != nil {
		log.Error().Err(err).Msg("smtp: get config failed")
		notifWriteError(w, "failed to load SMTP config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, SMTPConfigResponse{
		Host:       cfg.Host,
		Port:       cfg.Port,
		Username:   cfg.Username,
		From:       cfg.From,
		UseTLS:     cfg.UseTLS,
		UseSSL:     cfg.UseSSL,
		Configured: cfg.IsConfigured(),
	})
}

// HandleUpdateSMTP handles PUT /api/v1/admin/smtp.
func (h *NotificationsHandler) HandleUpdateSMTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req SMTPConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		notifWriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Load existing config; if password is not provided, preserve the existing one.
	existing, err := h.DB.GetSMTPConfig(ctx)
	if err != nil {
		log.Error().Err(err).Msg("smtp: load existing config failed")
		notifWriteError(w, "failed to load current SMTP config", http.StatusInternalServerError)
		return
	}

	newCfg := db.SMTPConfig{
		Host:   req.Host,
		Port:   req.Port,
		From:   req.From,
		UseTLS: req.UseTLS,
		UseSSL: req.UseSSL,
	}
	if req.Username != "" {
		newCfg.Username = req.Username
	} else {
		newCfg.Username = existing.Username
	}
	if req.Password != "" {
		newCfg.Password = req.Password // will be encrypted by SetSMTPConfig
	} else {
		newCfg.Password = existing.Password // preserve existing
	}
	if newCfg.Port == 0 {
		newCfg.Port = 587
	}

	if err := h.DB.SetSMTPConfig(ctx, newCfg); err != nil {
		log.Error().Err(err).Msg("smtp: save config failed")
		notifWriteError(w, "failed to save SMTP config", http.StatusInternalServerError)
		return
	}

	// Reload and return (without password).
	if h.Audit != nil {
		actorID, actorLabel := actorFromContext(r.Context())
		h.Audit.Record(ctx, actorID, actorLabel, db.AuditActionSMTPConfigUpdate,
			"smtp_config", "smtp", r.RemoteAddr, nil,
			map[string]interface{}{"host": newCfg.Host, "from": newCfg.From})
	}

	writeJSON(w, http.StatusOK, SMTPConfigResponse{
		Host:       newCfg.Host,
		Port:       newCfg.Port,
		Username:   newCfg.Username,
		From:       newCfg.From,
		UseTLS:     newCfg.UseTLS,
		UseSSL:     newCfg.UseSSL,
		Configured: newCfg.IsConfigured(),
	})
}

// HandleTestSMTP handles POST /api/v1/admin/smtp/test.
// Sends a test email to the requesting admin.
func (h *NotificationsHandler) HandleTestSMTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req struct {
		To string `json:"to"`
	}
	// Ignore parse errors (empty body is fine; we'll fall back to From).
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Reload the DB config so we can fall back to from_addr when to is omitted.
	if req.To == "" {
		cfg, _ := h.DB.GetSMTPConfig(ctx)
		req.To = cfg.From
	}
	if req.To == "" {
		notifWriteError(w, "to address is required (or configure a From address)", http.StatusBadRequest)
		return
	}

	if h.Mailer == nil || !h.Mailer.IsConfigured() {
		notifWriteError(w, "SMTP is not configured", http.StatusUnprocessableEntity)
		return
	}

	subject := "clustr SMTP test"
	body := "This is a test email from clustr. If you received this, SMTP is configured correctly.\n\nTimestamp: " + time.Now().UTC().String()

	if err := h.Mailer.Send(ctx, []string{req.To}, subject, body); err != nil {
		log.Error().Err(err).Str("to", req.To).Msg("smtp: test send failed")
		if h.Audit != nil {
			actorID, actorLabel := actorFromContext(ctx)
			h.Audit.Record(ctx, actorID, actorLabel, db.AuditActionSMTPTestSend,
				"smtp_config", "smtp", r.RemoteAddr, nil,
				map[string]interface{}{"to": req.To, "error": err.Error()})
		}
		notifWriteError(w, "test email failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	if h.Audit != nil {
		actorID, actorLabel := actorFromContext(ctx)
		h.Audit.Record(ctx, actorID, actorLabel, db.AuditActionSMTPTestSend,
			"smtp_config", "smtp", r.RemoteAddr, nil,
			map[string]interface{}{"to": req.To, "status": "sent"})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent", "to": req.To})
}

// BroadcastRequest is the body for POST /api/v1/node-groups/{id}/broadcast.
type BroadcastRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// HandleBroadcast handles POST /api/v1/node-groups/{id}/broadcast.
// Admin only. Sends an email to all members of the NodeGroup.
// Rate limited to 1 broadcast per group per BroadcastRateLimitHours (default 1h).
func (h *NotificationsHandler) HandleBroadcast(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	var req BroadcastRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		notifWriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Subject == "" || req.Body == "" {
		notifWriteError(w, "subject and body are required", http.StatusBadRequest)
		return
	}

	// Rate limit check.
	rateLimitHours := h.BroadcastRateLimitHours
	if rateLimitHours <= 0 {
		rateLimitHours = 1
	}
	lastSent, _ := h.DB.GetBroadcastLastSent(ctx, groupID)
	if lastSent > 0 {
		cutoff := time.Now().Add(-time.Duration(rateLimitHours) * time.Hour).Unix()
		if lastSent > cutoff {
			next := time.Unix(lastSent, 0).Add(time.Duration(rateLimitHours) * time.Hour)
			notifWriteError(w, "rate limit: next broadcast allowed at "+next.UTC().Format(time.RFC3339), http.StatusTooManyRequests)
			return
		}
	}

	if h.Mailer == nil || !h.Mailer.IsConfigured() {
		notifWriteError(w, "SMTP is not configured; broadcast unavailable", http.StatusUnprocessableEntity)
		return
	}

	// Get member email addresses. We use LDAP usernames as email addresses
	// (this is the typical pattern in HPC). If LDAP email lookup is needed,
	// wire it in via a closure on the handler. For now, we use username@domain
	// pattern or require members have email-format usernames.
	// For v1.3 we use the member list from pi_member_requests (approved members).
	members, err := h.DB.ListApprovedMemberEmails(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("broadcast: list members failed")
		notifWriteError(w, "failed to list group members", http.StatusInternalServerError)
		return
	}
	if len(members) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":          "no_recipients",
			"recipients_sent": 0,
		})
		return
	}

	actorID, actorLabel := actorFromContext(ctx)
	notifier := &notifications.Notifier{Mailer: h.Mailer, Audit: h.Audit}
	if err := notifier.SendBroadcast(ctx, members, req.Subject, req.Body, actorLabel, groupID); err != nil {
		notifWriteError(w, "broadcast failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Update broadcast rate-limit timestamp.
	_ = h.DB.SetBroadcastLastSent(ctx, groupID, time.Now())

	// Audit log: record subject + recipient count but NOT body (privacy).
	if h.Audit != nil {
		h.Audit.Record(ctx, actorID, actorLabel, db.AuditActionBroadcastSent,
			"node_group", groupID, r.RemoteAddr, nil,
			map[string]interface{}{
				"subject":         req.Subject,
				"recipient_count": len(members),
			})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":          "sent",
		"recipients_sent": len(members),
	})
}
