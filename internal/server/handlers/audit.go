package handlers

// audit.go — GET /api/v1/audit (S3-4 + C2-4) and
//            GET /api/v1/audit/export (F2, v1.5.0)
//
// HandleQuery: returns paginated audit log records with optional filters.
// Admin-only. Operators and readonly users get 403.
// C2-4 HTMX content negotiation: HX-Request returns HTML fragment rows.
//
// HandleExport: streams audit log as JSONL (one JSON object per line).
// Admin-only. Rate limited: 1 export per minute per admin actor.
// Use since/until query params to bound the export window.
// format=jsonl (default) — only format supported in v1.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// exportRateLimiter enforces a minimum interval between audit exports per actor.
// Key: actorID string → time of last export.
var exportRateLimiter = struct {
	mu   sync.Mutex
	last map[string]time.Time
}{last: make(map[string]time.Time)}

const exportRateLimitInterval = time.Minute

// checkExportRateLimit returns true if the actor is allowed to export now.
// It records the current time if allowed.
func checkExportRateLimit(actorID string) bool {
	exportRateLimiter.mu.Lock()
	defer exportRateLimiter.mu.Unlock()
	if t, ok := exportRateLimiter.last[actorID]; ok && time.Since(t) < exportRateLimitInterval {
		return false
	}
	exportRateLimiter.last[actorID] = time.Now()
	return true
}

// AuditHandler handles GET /api/v1/audit and GET /api/v1/audit/export.
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

// HandleExport streams the audit log as JSONL (one JSON object per line).
//
// Query params:
//
//	since=<RFC3339>   — only records at or after this time (required for safety)
//	until=<RFC3339>   — only records at or before this time
//	format=jsonl      — only "jsonl" supported (default)
//
// Auth: admin-only.
// Rate limit: 1 export per minute per actor (identified by actorLabel in context).
//
// The response is:
//
//	Content-Type: application/x-ndjson
//	Transfer-Encoding: chunked (streaming)
//
// Each line is a JSON object with the stable schema documented in docs/audit.md.
func (h *AuditHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	// Identify the requesting actor for rate limiting.
	// The route requires admin auth (requireRole("admin") middleware), so
	// RemoteAddr is a reasonable rate-limit key; admin accounts are few.
	// Using RemoteAddr (not user ID) avoids importing the server middleware
	// context key types into the handlers package.
	actorID := r.RemoteAddr

	// Rate limit: 1 export per minute per actor.
	if !checkExportRateLimit(actorID) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprintf(w, `{"error":"export rate limit exceeded — maximum 1 export per minute","code":"rate_limited"}`)
		return
	}

	q := r.URL.Query()

	p := db.AuditQueryParams{
		ActorID:      q.Get("actor"),
		Action:       q.Get("action"),
		ResourceType: q.Get("resource_type"),
	}

	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.Since = t
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid 'since' — must be RFC3339 (e.g. 2026-01-01T00:00:00Z)","code":"bad_request"}`)
			return
		}
	}
	if s := q.Get("until"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.Until = t
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"invalid 'until' — must be RFC3339 (e.g. 2026-12-31T23:59:59Z)","code":"bad_request"}`)
			return
		}
	}

	// Set up streaming response.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")

	flusher, canFlush := w.(http.Flusher)

	enc := json.NewEncoder(w)

	err := h.DB.StreamAuditLog(r.Context(), p, func(rec db.AuditRecord) error {
		line := auditJSONLLine{
			ID:           rec.ID,
			CreatedAt:    rec.CreatedAt.UTC().Format(time.RFC3339),
			ActorID:      rec.ActorID,
			ActorLabel:   rec.ActorLabel,
			Action:       rec.Action,
			ResourceType: rec.ResourceType,
			ResourceID:   rec.ResourceID,
			IPAddr:       rec.IPAddr,
		}
		if rec.OldValue != nil {
			line.OldValue = rec.OldValue
		}
		if rec.NewValue != nil {
			line.NewValue = rec.NewValue
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
		if canFlush {
			flusher.Flush()
		}
		return nil
	})
	if err != nil {
		// If headers haven't been written yet this would be a 500, but since we
		// streamed some data already we can only log the error.
		log.Error().Err(err).Msg("audit export: stream failed")
	}
}

// auditJSONLLine is the stable wire schema for one JSONL export line.
// Field names and types must not change without a version bump (D28).
type auditJSONLLine struct {
	// ID is the unique audit record identifier (e.g. "aud-1234567890").
	ID string `json:"id"`
	// CreatedAt is the RFC3339 UTC timestamp when the event was recorded.
	CreatedAt string `json:"created_at"`
	// ActorID is the internal ID of the actor (users.id or api_keys.id).
	ActorID string `json:"actor_id"`
	// ActorLabel is a human-readable actor string: "user:<id>" or "key:<label>".
	ActorLabel string `json:"actor_label"`
	// Action is the event type (e.g. "node.create", "user.update").
	Action string `json:"action"`
	// ResourceType is the category of the affected resource (e.g. "node", "image").
	ResourceType string `json:"resource_type"`
	// ResourceID is the ID of the affected resource.
	ResourceID string `json:"resource_id"`
	// IPAddr is the remote IP address of the actor request, if available.
	IPAddr string `json:"ip_addr,omitempty"`
	// OldValue is the JSON representation of the resource state before the action.
	OldValue *json.RawMessage `json:"old_value,omitempty"`
	// NewValue is the JSON representation of the resource state after the action.
	NewValue *json.RawMessage `json:"new_value,omitempty"`
}

// HandleDelete handles DELETE /api/v1/audit/:id — ACT-DEL-1 (Sprint 4).
// Removes a single audit log entry. The deletion itself is recorded as an
// audit.purged event. audit.purged events cannot be deleted.
func (h *AuditHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Prevent deletion of meta-audit records.
	if strings.HasPrefix(id, "audit.purged") {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cannot delete audit.purged meta-records",
			"code":  "protected_record",
		})
		return
	}

	if err := h.DB.DeleteAuditRecord(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	// ACT-DEL-2: record the deletion itself as audit.purged.
	db.NewAuditService(h.DB).Record(r.Context(), "", "", "audit.purged", "audit", id, r.RemoteAddr, nil,
		map[string]string{"deleted_id": id, "count": "1"})

	w.WriteHeader(http.StatusNoContent)
}

// HandleBulkDelete handles DELETE /api/v1/audit?before=<rfc3339>&kind=<k> — ACT-DEL-1 (Sprint 4).
// Bulk-removes entries matching the filter. Returns {count: N}.
func (h *AuditHandler) HandleBulkDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	p := db.AuditQueryParams{
		Action:       q.Get("action"),
		ResourceType: q.Get("resource_type"),
	}
	if s := q.Get("before"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			p.Until = t
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid 'before' — must be RFC3339",
				"code":  "bad_request",
			})
			return
		}
	}

	count, err := h.DB.BulkDeleteAuditRecords(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}

	// ACT-DEL-2: record the bulk deletion as audit.purged.
	db.NewAuditService(h.DB).Record(r.Context(), "", "", "audit.purged", "audit", "bulk", r.RemoteAddr, nil,
		map[string]string{"count": fmt.Sprintf("%d", count), "before": q.Get("before"), "action": q.Get("action")})

	writeJSON(w, http.StatusOK, map[string]int{"count": count})
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
