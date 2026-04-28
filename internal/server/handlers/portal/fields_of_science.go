package portal

// Field of Science handlers — Sprint E (E2, CF-16).
//
// Public (authenticated) routes:
//   GET /api/v1/fields-of-science                          — list enabled FOS (for PI picker)
//
// PI routes:
//   PATCH /api/v1/portal/pi/groups/{id}/field-of-science   — set FOS on owned group
//
// Admin routes:
//   GET   /api/v1/admin/fields-of-science                  — list all (inc disabled)
//   POST  /api/v1/admin/fields-of-science                  — create new entry
//   PUT   /api/v1/admin/fields-of-science/{fosID}          — update entry
//
// Director routes:
//   GET /api/v1/portal/director/fos-utilization            — breakdown by FOS

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// FOSHandler provides HTTP handlers for the Field of Science taxonomy.
type FOSHandler struct {
	DB    *db.DB
	Audit *db.AuditService
}

// HandleListFOS handles GET /api/v1/fields-of-science (public authenticated).
// Returns enabled FOS entries for use in dropdown pickers.
func (h *FOSHandler) HandleListFOS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, err := h.DB.ListFieldsOfScience(ctx)
	if err != nil {
		log.Error().Err(err).Msg("fos: list failed")
		writeError(w, "failed to list fields of science", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []db.FieldOfScience{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"fields_of_science": entries, "total": len(entries)})
}

// HandleAdminListFOS handles GET /api/v1/admin/fields-of-science.
// Returns all entries including disabled.
func (h *FOSHandler) HandleAdminListFOS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, err := h.DB.ListAllFieldsOfScience(ctx)
	if err != nil {
		log.Error().Err(err).Msg("fos: admin list failed")
		writeError(w, "failed to list fields of science", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []db.FieldOfScience{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"fields_of_science": entries, "total": len(entries)})
}

// HandleAdminCreateFOS handles POST /api/v1/admin/fields-of-science.
func (h *FOSHandler) HandleAdminCreateFOS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type ctxKeyUserID struct{}
	actorID, _ := ctx.Value(ctxKeyUserID{}).(string)

	var body struct {
		Name      string `json:"name"`
		ParentID  string `json:"parent_id"`
		NSFCode   string `json:"nsf_code"`
		SortOrder int    `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}

	f := &db.FieldOfScience{
		ID:        uuid.NewString(),
		Name:      body.Name,
		ParentID:  body.ParentID,
		NSFCode:   body.NSFCode,
		SortOrder: body.SortOrder,
		Enabled:   true,
	}
	if err := h.DB.CreateFieldOfScience(ctx, f); err != nil {
		log.Error().Err(err).Msg("fos: create failed")
		writeError(w, "failed to create field of science", http.StatusInternalServerError)
		return
	}

	h.Audit.Record(ctx, actorID, "admin:"+actorID, "field_of_science.created",
		"field_of_science", f.ID, "", nil, map[string]string{"name": f.Name})
	writeJSON(w, http.StatusCreated, f)
}

// HandleAdminUpdateFOS handles PUT /api/v1/admin/fields-of-science/{fosID}.
func (h *FOSHandler) HandleAdminUpdateFOS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fosID := chi.URLParam(r, "fosID")

	type ctxKeyUserID struct{}
	actorID, _ := ctx.Value(ctxKeyUserID{}).(string)

	existing, err := h.DB.GetFieldOfScience(ctx, fosID)
	if err != nil {
		writeError(w, "field of science not found", http.StatusNotFound)
		return
	}

	var body struct {
		Name      string `json:"name"`
		NSFCode   string `json:"nsf_code"`
		Enabled   *bool  `json:"enabled"`
		SortOrder *int   `json:"sort_order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Name != "" {
		existing.Name = body.Name
	}
	if body.NSFCode != "" {
		existing.NSFCode = body.NSFCode
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if body.SortOrder != nil {
		existing.SortOrder = *body.SortOrder
	}

	if err := h.DB.UpdateFieldOfScience(ctx, existing); err != nil {
		log.Error().Err(err).Str("id", fosID).Msg("fos: update failed")
		writeError(w, "failed to update field of science", http.StatusInternalServerError)
		return
	}

	h.Audit.Record(ctx, actorID, "admin:"+actorID, "field_of_science.updated",
		"field_of_science", fosID, "", nil, map[string]string{"name": existing.Name})
	writeJSON(w, http.StatusOK, existing)
}

// HandleSetGroupFOS handles PATCH /api/v1/portal/pi/groups/{id}/field-of-science.
// PI can set the FOS on their owned group.
func (h *FOSHandler) HandleSetGroupFOS(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		FieldOfScienceID string `json:"field_of_science_id"` // empty string = clear
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate FOS exists if non-empty.
	if body.FieldOfScienceID != "" {
		if _, err := h.DB.GetFieldOfScience(ctx, body.FieldOfScienceID); err != nil {
			writeError(w, "field of science not found", http.StatusBadRequest)
			return
		}
	}

	if err := h.DB.SetNodeGroupFOS(ctx, groupID, body.FieldOfScienceID); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("fos: set group FOS failed")
		writeError(w, "failed to update group", http.StatusInternalServerError)
		return
	}

	h.Audit.Record(ctx, userID, "pi:"+userID, "node_group.fos_set",
		"node_group", groupID, "",
		nil, map[string]string{"field_of_science_id": body.FieldOfScienceID})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"group_id":           groupID,
		"field_of_science_id": body.FieldOfScienceID,
	})
}

// HandleDirectorFOSUtilization handles GET /api/v1/portal/director/fos-utilization.
func (h *FOSHandler) HandleDirectorFOSUtilization(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	summary, err := h.DB.GetFOSUtilizationSummary(ctx)
	if err != nil {
		log.Error().Err(err).Msg("fos: director utilization summary failed")
		writeError(w, "failed to load FOS utilization", http.StatusInternalServerError)
		return
	}
	if summary == nil {
		summary = []db.NodeGroupFOSSummary{}
	}
	// Separate unclassified groups from the classified breakdown.
	var classified []db.NodeGroupFOSSummary
	unclassified := 0
	for _, s := range summary {
		if s.FOSID == "unclassified" {
			unclassified = s.GroupCount
		} else {
			classified = append(classified, s)
		}
	}
	if classified == nil {
		classified = []db.NodeGroupFOSSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"summary":      classified,
		"unclassified": unclassified,
	})
}
