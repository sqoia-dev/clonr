package portal

// Per-attribute visibility handlers — Sprint E (E3, CF-39).
//
// PI+Admin routes:
//   GET   /api/v1/portal/pi/groups/{id}/attribute-visibility      — list effective visibility for a group
//   PATCH /api/v1/portal/pi/groups/{id}/attribute-visibility      — PI/admin sets override
//   DELETE /api/v1/portal/pi/groups/{id}/attribute-visibility/{attr} — revert to global default
//
// Admin-only routes:
//   GET  /api/v1/admin/attribute-visibility-defaults              — list global defaults
//   PUT  /api/v1/admin/attribute-visibility-defaults/{attr}       — update global default

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// AttributeVisibilityHandler provides HTTP handlers for visibility policy management.
type AttributeVisibilityHandler struct {
	DB    *db.DB
	Audit *db.AuditService
}

// HandleListGroupVisibility handles GET /api/v1/portal/pi/groups/{id}/attribute-visibility.
// Returns merged view: global defaults + any project-specific overrides.
func (h *AttributeVisibilityHandler) HandleListGroupVisibility(w http.ResponseWriter, r *http.Request) {
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

	// Load global defaults.
	defaults, err := h.DB.ListAttributeVisibilityDefaults(ctx)
	if err != nil {
		log.Error().Err(err).Msg("visibility: list defaults failed")
		writeError(w, "failed to load visibility defaults", http.StatusInternalServerError)
		return
	}

	// Load project-specific overrides.
	overrides, err := h.DB.ListProjectVisibilityOverrides(ctx, groupID)
	if err != nil {
		log.Error().Err(err).Msg("visibility: list overrides failed")
		writeError(w, "failed to load visibility overrides", http.StatusInternalServerError)
		return
	}

	// Build override map for quick lookup.
	overrideMap := make(map[string]string, len(overrides))
	for _, o := range overrides {
		overrideMap[o.AttributeName] = string(o.Visibility)
	}

	// Merge: for each default, check if there's a project override.
	type row struct {
		AttributeName  string `json:"attribute_name"`
		DefaultVis     string `json:"default_visibility"`
		EffectiveVis   string `json:"effective_visibility"`
		IsOverridden   bool   `json:"is_overridden"`
		Description    string `json:"description"`
	}
	rows := make([]row, len(defaults))
	for i, d := range defaults {
		r := row{
			AttributeName: d.AttributeName,
			DefaultVis:    string(d.Visibility),
			EffectiveVis:  string(d.Visibility),
			Description:   d.Description,
		}
		if ov, ok := overrideMap[d.AttributeName]; ok {
			r.EffectiveVis = ov
			r.IsOverridden = true
		}
		rows[i] = r
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"project_id": groupID,
		"attributes": rows,
	})
}

// HandleSetGroupVisibility handles PATCH /api/v1/portal/pi/groups/{id}/attribute-visibility.
// Body: {"attribute_name": "grant_amount", "visibility": "public"}
func (h *AttributeVisibilityHandler) HandleSetGroupVisibility(w http.ResponseWriter, r *http.Request) {
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
		AttributeName string `json:"attribute_name"`
		Visibility    string `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.AttributeName == "" {
		writeError(w, "attribute_name is required", http.StatusBadRequest)
		return
	}
	validVis := map[string]bool{
		"admin_only": true, "pi": true, "member": true, "public": true,
	}
	if !validVis[body.Visibility] {
		writeError(w, "visibility must be one of: admin_only, pi, member, public", http.StatusBadRequest)
		return
	}

	if err := h.DB.SetProjectVisibilityOverride(ctx, groupID, body.AttributeName,
		db.AttributeVisibilityLevel(body.Visibility), userID); err != nil {
		log.Error().Err(err).Msg("visibility: set override failed")
		writeError(w, "failed to set visibility override", http.StatusInternalServerError)
		return
	}

	h.Audit.Log(ctx, userID, "attribute_visibility.set", "node_group", groupID,
		`{"attribute":"`+body.AttributeName+`","visibility":"`+body.Visibility+`"}`)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"project_id":     groupID,
		"attribute_name": body.AttributeName,
		"visibility":     body.Visibility,
	})
}

// HandleDeleteGroupVisibility handles DELETE /api/v1/portal/pi/groups/{id}/attribute-visibility/{attr}.
func (h *AttributeVisibilityHandler) HandleDeleteGroupVisibility(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")
	attrName := chi.URLParam(r, "attr")
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

	if err := h.DB.DeleteProjectVisibilityOverride(ctx, groupID, attrName); err != nil {
		log.Error().Err(err).Msg("visibility: delete override failed")
		writeError(w, "failed to delete visibility override", http.StatusInternalServerError)
		return
	}

	h.Audit.Log(ctx, userID, "attribute_visibility.reset", "node_group", groupID,
		`{"attribute":"`+attrName+`"}`)
	w.WriteHeader(http.StatusNoContent)
}

// HandleListVisibilityDefaults handles GET /api/v1/admin/attribute-visibility-defaults.
func (h *AttributeVisibilityHandler) HandleListVisibilityDefaults(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	defaults, err := h.DB.ListAttributeVisibilityDefaults(ctx)
	if err != nil {
		log.Error().Err(err).Msg("visibility: list defaults failed")
		writeError(w, "failed to load defaults", http.StatusInternalServerError)
		return
	}
	if defaults == nil {
		defaults = []db.AttributeVisibilityDefault{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"defaults": defaults})
}

// HandleUpdateVisibilityDefault handles PUT /api/v1/admin/attribute-visibility-defaults/{attr}.
func (h *AttributeVisibilityHandler) HandleUpdateVisibilityDefault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	attrName := chi.URLParam(r, "attr")

	type ctxKeyUserID struct{}
	actorID, _ := ctx.Value(ctxKeyUserID{}).(string)

	var body struct {
		Visibility  string `json:"visibility"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	validVis := map[string]bool{
		"admin_only": true, "pi": true, "member": true, "public": true,
	}
	if !validVis[body.Visibility] {
		writeError(w, "visibility must be one of: admin_only, pi, member, public", http.StatusBadRequest)
		return
	}

	if err := h.DB.SetAttributeVisibilityDefault(ctx, attrName,
		db.AttributeVisibilityLevel(body.Visibility), body.Description); err != nil {
		log.Error().Err(err).Msg("visibility: update default failed")
		writeError(w, "failed to update default", http.StatusInternalServerError)
		return
	}

	h.Audit.Log(ctx, actorID, "attribute_visibility.default_updated", "system", attrName,
		`{"visibility":"`+body.Visibility+`"}`)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"attribute_name": attrName,
		"visibility":     body.Visibility,
	})
}
