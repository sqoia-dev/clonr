package handlers

// notices.go — Sprint 42 Day 4 NOTICE-PATCH
//
// Three endpoints:
//
//   POST   /api/v1/admin/notices        (requires admin.notices)
//     Create a new global operator notice. Body: {body, severity, expires_at?}.
//     Returns the created notice.
//
//   GET    /api/v1/notices/active       (no auth — public)
//     Returns the single most-visible active notice, or null when none active.
//
//   DELETE /api/v1/admin/notices/{id}   (requires admin.notices)
//     Marks the notice dismissed.
//
// See docs/SPRINT-PLAN.md §Sprint 42, NOTICE-PATCH.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/db"
)

// NoticesDBIface is the database subset required by NoticesHandler.
type NoticesDBIface interface {
	InsertNotice(ctx context.Context, p db.CreateNoticeParams) (db.Notice, error)
	GetActiveNotice(ctx context.Context) (*db.Notice, error)
	DismissNotice(ctx context.Context, id int64) error
}

// NoticesAuditIface is the audit subset required by NoticesHandler.
type NoticesAuditIface interface {
	Record(ctx context.Context, actorID, actorLabel, action, resourceType, resourceID, ipAddr string, oldVal, newVal interface{})
}

// NoticesHandler implements the three notice endpoints.
type NoticesHandler struct {
	DB           NoticesDBIface
	Audit        NoticesAuditIface
	GetActorInfo func(r *http.Request) (string, string)
}

// ─── Request / Response types ─────────────────────────────────────────────────

type createNoticeRequest struct {
	Body      string  `json:"body"`
	Severity  string  `json:"severity"`
	ExpiresAt *string `json:"expires_at,omitempty"` // RFC3339; omit = never
}

type noticeResponse struct {
	ID          int64   `json:"id"`
	Body        string  `json:"body"`
	Severity    string  `json:"severity"`
	CreatedBy   string  `json:"created_by,omitempty"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
	DismissedAt *string `json:"dismissed_at,omitempty"`
}

// activeNoticeResponse is the GET active response. Notice is null when none active.
type activeNoticeResponse struct {
	Notice *noticeResponse `json:"notice"`
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// HandleCreate handles POST /api/v1/admin/notices.
func (h *NoticesHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req createNoticeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.Body == "" {
		http.Error(w, `{"error":"body is required"}`, http.StatusUnprocessableEntity)
		return
	}
	switch req.Severity {
	case "info", "warning", "critical":
	default:
		http.Error(w, `{"error":"severity must be info, warning, or critical"}`, http.StatusUnprocessableEntity)
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			http.Error(w, `{"error":"expires_at must be RFC3339"}`, http.StatusUnprocessableEntity)
			return
		}
		expiresAt = &t
	}

	actorID, actorLabel := "", ""
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}

	notice, err := h.DB.InsertNotice(r.Context(), db.CreateNoticeParams{
		Body:      req.Body,
		Severity:  req.Severity,
		CreatedBy: actorLabel,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionNoticeCreated, "notice", strconv.FormatInt(notice.ID, 10),
			r.RemoteAddr, nil, map[string]interface{}{
				"body":     notice.Body,
				"severity": notice.Severity,
			})
	}

	resp := toNoticeResponse(notice)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleGetActive handles GET /api/v1/notices/active (no auth).
func (h *NoticesHandler) HandleGetActive(w http.ResponseWriter, r *http.Request) {
	n, err := h.DB.GetActiveNotice(r.Context())
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	var resp activeNoticeResponse
	if n != nil {
		nr := toNoticeResponse(*n)
		resp.Notice = &nr
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleDismiss handles DELETE /api/v1/admin/notices/{id}.
func (h *NoticesHandler) HandleDismiss(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"invalid notice id"}`, http.StatusBadRequest)
		return
	}

	actorID, actorLabel := "", ""
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}

	if err := h.DB.DismissNotice(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, `{"error":"notice not found"}`, http.StatusNotFound)
			return
		}
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionNoticeDismissed, "notice", strconv.FormatInt(id, 10),
			r.RemoteAddr, nil, nil)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func toNoticeResponse(n db.Notice) noticeResponse {
	resp := noticeResponse{
		ID:        n.ID,
		Body:      n.Body,
		Severity:  n.Severity,
		CreatedBy: n.CreatedBy,
		CreatedAt: n.CreatedAt.UTC().Format(time.RFC3339),
	}
	if n.ExpiresAt != nil {
		s := n.ExpiresAt.UTC().Format(time.RFC3339)
		resp.ExpiresAt = &s
	}
	if n.DismissedAt != nil {
		s := n.DismissedAt.UTC().Format(time.RFC3339)
		resp.DismissedAt = &s
	}
	return resp
}
