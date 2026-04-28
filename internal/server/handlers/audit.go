package handlers

// audit.go — GET /api/v1/audit (S3-4 + C2-4)
//
// Returns paginated audit log records with optional filters.
// Admin-only. Operators and readonly users get 403.
//
// C2-4 HTMX content negotiation: when HX-Request: true the handler returns
// an HTML fragment (<tr>…</tr> rows) suitable for HTMX swap into the audit
// table body. When HX-Request is absent (or for API consumers with
// Accept: application/json) the existing JSON response is returned unchanged.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

	// C2-4: HTMX content negotiation.
	// When HX-Request is present return an HTML fragment (<tr>…</tr> rows) so
	// HTMX can swap them directly into the audit table body.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(out) == 0 {
			fmt.Fprint(w, `<tr><td colspan="7" class="text-center text-muted py-3">No records found.</td></tr>`)
			return
		}
		var sb strings.Builder
		for _, rec := range out {
			// Truncate old/new value JSON for display — show first 60 chars.
			oldStr := ""
			if rec.OldValue != nil {
				s := string(*rec.OldValue)
				if len(s) > 60 {
					s = s[:60] + "…"
				}
				oldStr = s
			}
			newStr := ""
			if rec.NewValue != nil {
				s := string(*rec.NewValue)
				if len(s) > 60 {
					s = s[:60] + "…"
				}
				newStr = s
			}
			sb.WriteString("<tr>")
			sb.WriteString(fmt.Sprintf(`<td class="text-nowrap">%s</td>`, escapeHTMLAudit(rec.CreatedAt)))
			sb.WriteString(fmt.Sprintf(`<td>%s</td>`, escapeHTMLAudit(rec.ActorLabel)))
			sb.WriteString(fmt.Sprintf(`<td><code>%s</code></td>`, escapeHTMLAudit(rec.Action)))
			sb.WriteString(fmt.Sprintf(`<td>%s</td>`, escapeHTMLAudit(rec.ResourceType)))
			sb.WriteString(fmt.Sprintf(`<td><code>%s</code></td>`, escapeHTMLAudit(rec.ResourceID)))
			sb.WriteString(fmt.Sprintf(`<td class="text-truncate" style="max-width:160px" title="%s"><code>%s</code></td>`,
				escapeHTMLAudit(oldStr), escapeHTMLAudit(oldStr)))
			sb.WriteString(fmt.Sprintf(`<td class="text-truncate" style="max-width:160px" title="%s"><code>%s</code></td>`,
				escapeHTMLAudit(newStr), escapeHTMLAudit(newStr)))
			sb.WriteString("</tr>")
		}
		fmt.Fprint(w, sb.String())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": out,
		"total":   total,
		"limit":   p.Limit,
		"offset":  p.Offset,
	})
}

// escapeHTMLAudit escapes characters that are special in HTML attribute/text
// contexts to prevent XSS in the audit log HTMX partial.
func escapeHTMLAudit(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&#34;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}
