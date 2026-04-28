package portal

// Grant handlers for the PI portal — Sprint D (D3-1, CF-12).
//
// PI can CRUD grants on their owned NodeGroups.
// Admin can CRUD grants on all NodeGroups.
//
// Routes (registered under PI middleware group):
//   GET    /api/v1/portal/pi/groups/{id}/grants
//   POST   /api/v1/portal/pi/groups/{id}/grants
//   PUT    /api/v1/portal/pi/groups/{id}/grants/{grantID}
//   DELETE /api/v1/portal/pi/groups/{id}/grants/{grantID}

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// GrantRequest is the request body for create/update grant.
// Amount is a float64 from the JSON (JS sends parseFloat); we store it as string.
type GrantRequest struct {
	Title         string  `json:"title"`
	FundingAgency string  `json:"funding_agency"`
	GrantNumber   string  `json:"grant_number"`
	Amount        float64 `json:"amount"`
	StartDate     string  `json:"start_date"`
	EndDate       string  `json:"end_date"`
	Status        string  `json:"status"`
	Notes         string  `json:"notes"`
}

// HandleListGrants handles GET /api/v1/portal/pi/groups/{id}/grants.
func (h *PIHandler) HandleListGrants(w http.ResponseWriter, r *http.Request) {
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

	grants, err := h.DB.ListGrantsByGroup(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("grants: list failed")
		writeError(w, "failed to list grants", http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []db.Grant{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"grants": grants})
}

// HandleCreateGrant handles POST /api/v1/portal/pi/groups/{id}/grants.
func (h *PIHandler) HandleCreateGrant(w http.ResponseWriter, r *http.Request) {
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

	var req GrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}

	status := req.Status
	if status == "" {
		status = "active"
	}

	amountStr := ""
	if req.Amount != 0 {
		amountStr = strconv.FormatFloat(req.Amount, 'f', -1, 64)
	}
	g := db.Grant{
		ID:              fmt.Sprintf("grant-%d", time.Now().UnixNano()),
		NodeGroupID:     groupID,
		Title:           req.Title,
		FundingAgency:   req.FundingAgency,
		GrantNumber:     req.GrantNumber,
		Amount:          amountStr,
		StartDate:       req.StartDate,
		EndDate:         req.EndDate,
		Status:          status,
		Notes:           req.Notes,
		CreatedByUserID: userID,
	}

	if err := h.DB.CreateGrant(ctx, g); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("grants: create failed")
		writeError(w, "failed to create grant", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionGrantCreate,
			"grant", g.ID, r.RemoteAddr, nil,
			map[string]interface{}{"title": g.Title, "group_id": groupID})
	}
	writeJSON(w, http.StatusCreated, g)
}

// HandleUpdateGrant handles PUT /api/v1/portal/pi/groups/{id}/grants/{grantID}.
func (h *PIHandler) HandleUpdateGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	grantID := chi.URLParam(r, "grantID")
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

	existing, err := h.DB.GetGrant(ctx, grantID)
	if err != nil {
		writeError(w, "grant not found", http.StatusNotFound)
		return
	}
	if existing.NodeGroupID != groupID {
		writeError(w, "grant does not belong to this group", http.StatusForbidden)
		return
	}

	var req GrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Title == "" {
		writeError(w, "title is required", http.StatusBadRequest)
		return
	}

	existing.Title = req.Title
	existing.FundingAgency = req.FundingAgency
	existing.GrantNumber = req.GrantNumber
	if req.Amount != 0 {
		existing.Amount = strconv.FormatFloat(req.Amount, 'f', -1, 64)
	}
	existing.StartDate = req.StartDate
	existing.EndDate = req.EndDate
	existing.Notes = req.Notes
	if req.Status != "" {
		existing.Status = req.Status
	}

	if err := h.DB.UpdateGrant(ctx, existing); err != nil {
		log.Error().Err(err).Str("grant_id", grantID).Msg("grants: update failed")
		writeError(w, "failed to update grant", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionGrantUpdate,
			"grant", grantID, r.RemoteAddr, nil,
			map[string]interface{}{"title": existing.Title})
	}
	writeJSON(w, http.StatusOK, existing)
}

// HandleDeleteGrant handles DELETE /api/v1/portal/pi/groups/{id}/grants/{grantID}.
func (h *PIHandler) HandleDeleteGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	grantID := chi.URLParam(r, "grantID")
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

	existing, err := h.DB.GetGrant(ctx, grantID)
	if err != nil {
		writeError(w, "grant not found", http.StatusNotFound)
		return
	}
	if existing.NodeGroupID != groupID {
		writeError(w, "grant does not belong to this group", http.StatusForbidden)
		return
	}

	if err := h.DB.DeleteGrant(ctx, grantID); err != nil {
		log.Error().Err(err).Str("grant_id", grantID).Msg("grants: delete failed")
		writeError(w, "failed to delete grant", http.StatusInternalServerError)
		return
	}

	if h.Audit != nil {
		h.Audit.Record(ctx, userID, userID, db.AuditActionGrantDelete,
			"grant", grantID, r.RemoteAddr, nil, nil)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
