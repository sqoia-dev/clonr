package portal

// Allocation change request handlers — Sprint E (E1, CF-20).
//
// PI routes:
//   GET  /api/v1/portal/pi/groups/{id}/change-requests          — list requests for a group
//   POST /api/v1/portal/pi/groups/{id}/change-requests          — submit new request
//   POST /api/v1/portal/pi/change-requests/{reqID}/withdraw     — PI withdraws pending request
//
// Admin routes:
//   GET  /api/v1/admin/change-requests                          — queue view (all pending)
//   POST /api/v1/admin/change-requests/{id}/review              — approve or deny

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications"
)

// AllocationChangeRequestHandler provides PI + admin endpoints for change requests.
type AllocationChangeRequestHandler struct {
	DB           *db.DB
	Audit        *db.AuditService
	Notifier     *notifications.Notifier
	// GetActorInfo extracts (actorID, actorLabel) for admin routes.
	// Injected by server.go to avoid import cycles.
	GetActorInfo func(r *http.Request) (string, string)
}

// adminIDFromRequest returns the admin user ID from the injected GetActorInfo or falls back to "admin".
func (h *AllocationChangeRequestHandler) adminIDFromRequest(r *http.Request) string {
	if h.GetActorInfo != nil {
		id, _ := h.GetActorInfo(r)
		if id != "" {
			return id
		}
	}
	return "admin"
}

// ─── PI: list requests for a group ───────────────────────────────────────────

// HandleListGroupChangeRequests handles GET /api/v1/portal/pi/groups/{id}/change-requests.
func (h *AllocationChangeRequestHandler) HandleListGroupChangeRequests(w http.ResponseWriter, r *http.Request) {
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

	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}

	reqs, err := h.DB.ListAllocationChangeRequests(ctx, groupID, status, limit, offset)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("acr: list change requests failed")
		writeError(w, "failed to list change requests", http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []db.AllocationChangeRequest{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"requests": reqs, "total": len(reqs)})
}

// ─── PI: submit new request ───────────────────────────────────────────────────

type changeRequestBody struct {
	RequestType   string          `json:"request_type"`
	Payload       json.RawMessage `json:"payload"`
	Justification string          `json:"justification"`
}

// HandleCreateChangeRequest handles POST /api/v1/portal/pi/groups/{id}/change-requests.
func (h *AllocationChangeRequestHandler) HandleCreateChangeRequest(w http.ResponseWriter, r *http.Request) {
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

	var body changeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	validTypes := map[string]bool{
		"add_member": true, "remove_member": true,
		"increase_resources": true, "extend_duration": true, "archive_project": true,
	}
	if !validTypes[body.RequestType] {
		writeError(w, "invalid request_type; must be one of: add_member, remove_member, increase_resources, extend_duration, archive_project", http.StatusBadRequest)
		return
	}

	// Normalise payload to valid JSON.
	payload := `{}`
	if len(body.Payload) > 0 {
		payload = string(body.Payload)
	}

	acr := &db.AllocationChangeRequest{
		ID:              uuid.NewString(),
		ProjectID:       groupID,
		RequesterUserID: userID,
		RequestType:     body.RequestType,
		Payload:         payload,
		Justification:   body.Justification,
	}
	if err := h.DB.CreateAllocationChangeRequest(ctx, acr); err != nil {
		log.Error().Err(err).Msg("acr: create change request failed")
		writeError(w, "failed to create change request", http.StatusInternalServerError)
		return
	}

	h.Audit.Log(ctx, userID, "allocation_change_request.created", "allocation_change_request", acr.ID,
		`{"request_type":"`+body.RequestType+`","group_id":"`+groupID+`"}`)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      acr.ID,
		"status":  "pending",
		"message": "Change request submitted. An admin will review and respond.",
	})
}

// ─── PI: withdraw ─────────────────────────────────────────────────────────────

// HandleWithdrawChangeRequest handles POST /api/v1/portal/pi/change-requests/{reqID}/withdraw.
func (h *AllocationChangeRequestHandler) HandleWithdrawChangeRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqID := chi.URLParam(r, "reqID")
	userID := piUserIDFromContext(ctx)

	if err := h.DB.WithdrawAllocationChangeRequest(ctx, reqID, userID); err != nil {
		writeError(w, "failed to withdraw change request: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	h.Audit.Log(ctx, userID, "allocation_change_request.withdrawn", "allocation_change_request", reqID, `{}`)
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": reqID, "status": "withdrawn"})
}

// ─── Admin: queue view ────────────────────────────────────────────────────────

// HandleAdminListChangeRequests handles GET /api/v1/admin/change-requests.
// Defaults to pending requests; accepts ?status= and ?project_id= filters.
func (h *AllocationChangeRequestHandler) HandleAdminListChangeRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	projectID := r.URL.Query().Get("project_id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	reqs, err := h.DB.ListAllocationChangeRequests(ctx, projectID, status, limit, offset)
	if err != nil {
		log.Error().Err(err).Msg("acr: admin list change requests failed")
		writeError(w, "failed to list change requests", http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []db.AllocationChangeRequest{}
	}

	pendingCount, _ := h.DB.CountPendingAllocationChangeRequests(ctx, "")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"requests":      reqs,
		"total":         len(reqs),
		"pending_total": pendingCount,
	})
}

// ─── Admin: review (approve / deny) ──────────────────────────────────────────

type reviewBody struct {
	Status string `json:"status"` // approved | denied
	Notes  string `json:"notes"`
}

// HandleAdminReviewChangeRequest handles POST /api/v1/admin/change-requests/{id}/review.
func (h *AllocationChangeRequestHandler) HandleAdminReviewChangeRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqID := chi.URLParam(r, "id")

	// Derive admin user ID from the injected GetActorInfo closure.
	adminID := h.adminIDFromRequest(r)

	var body reviewBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Status != "approved" && body.Status != "denied" {
		writeError(w, "status must be 'approved' or 'denied'", http.StatusBadRequest)
		return
	}

	// Load the request before mutating so we can send notification.
	acr, err := h.DB.GetAllocationChangeRequest(ctx, reqID)
	if err != nil {
		writeError(w, "change request not found", http.StatusNotFound)
		return
	}

	if err := h.DB.ReviewAllocationChangeRequest(ctx, reqID, adminID, body.Status, body.Notes); err != nil {
		log.Error().Err(err).Str("id", reqID).Msg("acr: review failed")
		writeError(w, "failed to review change request: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	h.Audit.Log(ctx, adminID, "allocation_change_request."+body.Status,
		"allocation_change_request", reqID,
		`{"status":"`+body.Status+`","request_type":"`+acr.RequestType+`"}`)

	// Fire-and-forget email to PI.
	if h.Notifier != nil {
		go func() {
			piEmail := acr.RequesterUserID // clustr uses username as email addr
			if body.Status == "approved" {
				h.Notifier.NotifyAllocationChangeDecision(ctx, piEmail, acr.RequesterName, acr.ProjectName, acr.RequestType, "approved", body.Notes, time.Now())
			} else {
				h.Notifier.NotifyAllocationChangeDecision(ctx, piEmail, acr.RequesterName, acr.ProjectName, acr.RequestType, "denied", body.Notes, time.Now())
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     reqID,
		"status": body.Status,
	})
}
