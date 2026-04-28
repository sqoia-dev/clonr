package portal

// Annual review cycle handlers — Sprint D (D4-1/D4-2, CF-11 lite).
//
// Admin creates a review cycle; all PI-owned NodeGroups get response rows.
// PIs respond: affirmed (group is active) or archive_requested.
// Admin views aggregate results. Director views read-only.
//
// Routes:
//   POST /api/v1/admin/review-cycles                          (admin)
//   GET  /api/v1/admin/review-cycles                          (admin/director)
//   GET  /api/v1/admin/review-cycles/{id}                     (admin/director)
//   GET  /api/v1/portal/pi/review-cycles                      (pi — own groups)
//   POST /api/v1/portal/pi/review-cycles/{cycleID}/groups/{groupID}/respond  (pi)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ReviewCycleRequest is the body for creating a review cycle.
type ReviewCycleRequest struct {
	Name     string `json:"name"`
	Deadline string `json:"deadline"` // ISO-8601 date: "2026-12-31"
}

// ReviewResponseRequest is the body for a PI's review response.
type ReviewResponseRequest struct {
	Status string `json:"status"` // affirmed | archive_requested
	Notes  string `json:"notes"`
}

// HandleCreateReviewCycle handles POST /api/v1/admin/review-cycles.
// Admin creates a cycle; pending response rows are created for all PI-owned groups.
func HandleCreateReviewCycle(dbConn *db.DB, audit *db.AuditService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req ReviewCycleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Deadline == "" {
			writeError(w, "deadline is required", http.StatusBadRequest)
			return
		}

		deadline, err := time.Parse("2006-01-02", req.Deadline)
		if err != nil {
			writeError(w, "deadline must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}

		name := req.Name
		if name == "" {
			name = fmt.Sprintf("Annual Review %d", time.Now().Year())
		}

		cycle := db.ReviewCycle{
			ID:       fmt.Sprintf("rc-%d", time.Now().UnixNano()),
			Name:     name,
			Deadline: deadline,
			// CreatedBy will be populated from context in production.
		}

		if err := dbConn.CreateReviewCycle(ctx, cycle); err != nil {
			log.Error().Err(err).Msg("review-cycles: create failed")
			writeError(w, "failed to create review cycle", http.StatusInternalServerError)
			return
		}

		count, err := dbConn.CreateReviewResponses(ctx, cycle.ID)
		if err != nil {
			log.Warn().Err(err).Str("cycle_id", cycle.ID).Msg("review-cycles: create responses partial failure")
		}

		if audit != nil {
			audit.Record(ctx, "admin", "admin", db.AuditActionReviewCycleCreate,
				"review_cycle", cycle.ID, r.RemoteAddr, nil,
				map[string]interface{}{"name": cycle.Name, "deadline": req.Deadline, "groups_notified": count})
		}

		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"cycle":           cycle,
			"responses_created": count,
		})
	}
}

// HandleListReviewCycles handles GET /api/v1/admin/review-cycles.
func HandleListReviewCycles(dbConn *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycles, err := dbConn.ListReviewCycles(r.Context())
		if err != nil {
			log.Error().Err(err).Msg("review-cycles: list failed")
			writeError(w, "failed to list review cycles", http.StatusInternalServerError)
			return
		}
		if cycles == nil {
			cycles = []db.ReviewCycle{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"cycles": cycles})
	}
}

// HandleGetReviewCycle handles GET /api/v1/admin/review-cycles/{id}.
func HandleGetReviewCycle(dbConn *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID := chi.URLParam(r, "id")
		ctx := r.Context()

		cycle, err := dbConn.GetReviewCycle(ctx, cycleID)
		if err != nil {
			writeError(w, "review cycle not found", http.StatusNotFound)
			return
		}

		responses, err := dbConn.ListReviewResponsesByCycle(ctx, cycleID)
		if err != nil {
			log.Error().Err(err).Str("cycle_id", cycleID).Msg("review-cycles: list responses failed")
			writeError(w, "failed to list responses", http.StatusInternalServerError)
			return
		}
		if responses == nil {
			responses = []db.ReviewResponse{}
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"cycle":     cycle,
			"responses": responses,
		})
	}
}

// HandleListPIReviewCycles handles GET /api/v1/portal/pi/review-cycles.
// Returns pending review cycles relevant to the authenticated PI's groups.
func (h *PIHandler) HandleListPIReviewCycles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := piUserIDFromContext(ctx)

	responses, err := h.DB.ListReviewResponsesByPI(ctx, userID)
	if err != nil {
		log.Error().Err(err).Str("pi_user_id", userID).Msg("review-cycles: list pi responses failed")
		writeError(w, "failed to list review cycles", http.StatusInternalServerError)
		return
	}
	if responses == nil {
		responses = []db.ReviewResponse{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"responses": responses})
}

// HandleSubmitReviewResponse handles POST /api/v1/portal/pi/review-cycles/{cycleID}/groups/{groupID}/respond.
// PI affirms their group is active or requests archival.
func (h *PIHandler) HandleSubmitReviewResponse(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cycleID := chi.URLParam(r, "cycleID")
	groupID := chi.URLParam(r, "groupID")
	userID := piUserIDFromContext(ctx)
	role, _ := ctx.Value(ctxKeyPIRole{}).(string)

	// Ownership check (admin bypass).
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

	var req ReviewResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Status != "affirmed" && req.Status != "archive_requested" {
		writeError(w, "status must be 'affirmed' or 'archive_requested'", http.StatusBadRequest)
		return
	}

	if err := h.DB.SubmitReviewResponse(ctx, cycleID, groupID, req.Status, req.Notes); err != nil {
		log.Error().Err(err).Str("cycle_id", cycleID).Str("group_id", groupID).Msg("review response submit failed")
		writeError(w, "failed to submit review response", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionReviewResponseSubmit,
			"review_response", fmt.Sprintf("%s/%s", cycleID, groupID), r.RemoteAddr, nil,
			map[string]interface{}{"status": req.Status, "group_id": groupID})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": req.Status})
}
