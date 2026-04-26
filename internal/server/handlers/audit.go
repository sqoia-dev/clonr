package handlers

// audit.go — GET /api/v1/audit (S3-4)
//
// Returns paginated audit log records with optional filters.
// Admin-only. Operators and readonly users get 403.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// AuditHandler handles GET /api/v1/audit.
type AuditHandler struct {
	DB *db.DB
}

// auditRecordResponse is the JSON wire type for a single audit log entry.
type auditRecordResponse struct {
	ID           string           `json:"id"`
	ActorID      string           `json:"actor_id"`
	ActorLabel   string           `json:"actor_label"`
	Action       string           `json:"action"`
	ResourceType string           `json:"resource_type"`
	ResourceID   string           `json:"resource_id"`
	OldValue     *json.RawMessage `json:"old_value,omitempty"`
	NewValue     *json.RawMessage `json:"new_value,omitempty"`
	IPAddr       string           `json:"ip_addr,omitempty"`
	CreatedAt    string           `json:"created_at"`
}

// HandleQuery handles GET /api/v1/audit.
//
// Query params:
//
//	since=<RFC3339>       — only records at or after this time
//	until=<RFC3339>       — only records at or before this time
//	actor=<actor_id>      — filter by actor_id
//	action=<action>       — filter by action string
//	resource_type=<type>  — filter by resource_type
//	limit=<n>             — max records per page (1-500, default 100)
//	offset=<n>            — pagination offset (default 0)
func (h *AuditHandler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	p := db.AuditQueryParams{
		ActorID:      q.Get("actor"),
		Action:       q.Get("action"),
		ResourceType: q.Get("resource_type"),
	}

	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.Since = t
		}
	}
	if s := q.Get("until"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.Until = t
		}
	}
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			p.Limit = n
		}
	}
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			p.Offset = n
		}
	}

	records, total, err := h.DB.QueryAuditLog(r.Context(), p)
	if err != nil {
		log.Error().Err(err).Msg("audit: query failed")
		writeError(w, err)
		return
	}

	out := make([]auditRecordResponse, len(records))
	for i, rec := range records {
		out[i] = auditRecordResponse{
			ID:           rec.ID,
			ActorID:      rec.ActorID,
			ActorLabel:   rec.ActorLabel,
			Action:       rec.Action,
			ResourceType: rec.ResourceType,
			ResourceID:   rec.ResourceID,
			OldValue:     rec.OldValue,
			NewValue:     rec.NewValue,
			IPAddr:       rec.IPAddr,
			CreatedAt:    rec.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	if out == nil {
		out = []auditRecordResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": out,
		"total":   total,
		"limit":   p.Limit,
		"offset":  p.Offset,
	})
}
