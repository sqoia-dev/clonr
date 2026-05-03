package handlers

// identity_groups.go — supplementary group overlay + specialty group endpoints (Sprint 7).
//
// Supplementary overlay (on LDAP groups):
//   GET    /api/v1/groups/{group_dn}/supplementary-members
//   POST   /api/v1/groups/{group_dn}/supplementary-members
//   DELETE /api/v1/groups/{group_dn}/supplementary-members/{user_identifier}
//
// Specialty groups (clustr-only):
//   GET    /api/v1/groups/specialty
//   POST   /api/v1/groups/specialty
//   PATCH  /api/v1/groups/specialty/{id}
//   DELETE /api/v1/groups/specialty/{id}
//   POST   /api/v1/groups/specialty/{id}/members
//   DELETE /api/v1/groups/specialty/{id}/members/{uid}

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// IdentityGroupsHandler handles identity group endpoints.
type IdentityGroupsHandler struct {
	DB *db.DB
}

// ─── Supplementary overlay ────────────────────────────────────────────────────

func (h *IdentityGroupsHandler) HandleListOverlay(w http.ResponseWriter, r *http.Request) {
	groupDN, err := url.PathUnescape(chi.URLParam(r, "group_dn"))
	if err != nil {
		groupDN = chi.URLParam(r, "group_dn")
	}
	members, err := h.DB.GroupOverlayListByGroup(r.Context(), groupDN)
	if err != nil {
		log.Error().Err(err).Str("group_dn", groupDN).Msg("identity_groups: list overlay failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if members == nil {
		members = []db.GroupOverlay{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"members": members, "total": len(members)})
}

func (h *IdentityGroupsHandler) HandleAddOverlay(w http.ResponseWriter, r *http.Request) {
	groupDN, err := url.PathUnescape(chi.URLParam(r, "group_dn"))
	if err != nil {
		groupDN = chi.URLParam(r, "group_dn")
	}

	var body struct {
		UserIdentifier string `json:"user_identifier"`
		Source         string `json:"source"` // "ldap" | "local"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.UserIdentifier == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_identifier is required"})
		return
	}
	if body.Source == "" {
		body.Source = "local"
	}

	o := db.GroupOverlay{
		GroupDN:        groupDN,
		UserIdentifier: body.UserIdentifier,
		Source:         body.Source,
	}
	if err := h.DB.GroupOverlayAdd(r.Context(), o); err != nil {
		log.Error().Err(err).Msg("identity_groups: add overlay failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

func (h *IdentityGroupsHandler) HandleRemoveOverlay(w http.ResponseWriter, r *http.Request) {
	groupDN, err := url.PathUnescape(chi.URLParam(r, "group_dn"))
	if err != nil {
		groupDN = chi.URLParam(r, "group_dn")
	}
	userIdentifier, err := url.PathUnescape(chi.URLParam(r, "user_identifier"))
	if err != nil {
		userIdentifier = chi.URLParam(r, "user_identifier")
	}

	if err := h.DB.GroupOverlayRemove(r.Context(), groupDN, userIdentifier); err != nil {
		log.Error().Err(err).Msg("identity_groups: remove overlay failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Specialty groups ─────────────────────────────────────────────────────────

func (h *IdentityGroupsHandler) HandleListSpecialty(w http.ResponseWriter, r *http.Request) {
	groups, err := h.DB.SpecialtyGroupListAll(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("identity_groups: list specialty failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if groups == nil {
		groups = []db.SpecialtyGroup{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": groups, "total": len(groups)})
}

func (h *IdentityGroupsHandler) HandleCreateSpecialty(w http.ResponseWriter, r *http.Request) {
	var g db.SpecialtyGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if g.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if g.GIDNumber <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gid_number must be a positive integer"})
		return
	}
	g.Name = strings.TrimSpace(g.Name)

	created, err := h.DB.SpecialtyGroupCreate(r.Context(), g)
	if err != nil {
		if isSQLiteUniqueErr(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "name or gid_number already in use"})
			return
		}
		log.Error().Err(err).Msg("identity_groups: create specialty failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	// Set initial members if provided.
	for _, uid := range g.Members {
		if uid == "" {
			continue
		}
		_ = h.DB.SpecialtyGroupAddMember(r.Context(), created.ID, uid, "local")
	}

	// Reload with members.
	created, _ = h.DB.SpecialtyGroupGet(r.Context(), created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (h *IdentityGroupsHandler) HandleUpdateSpecialty(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var g db.SpecialtyGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if g.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if g.GIDNumber <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gid_number must be positive"})
		return
	}

	updated, err := h.DB.SpecialtyGroupUpdate(r.Context(), id, g)
	if err != nil {
		if isSQLiteUniqueErr(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "name or gid_number already in use"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *IdentityGroupsHandler) HandleDeleteSpecialty(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.SpecialtyGroupDelete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *IdentityGroupsHandler) HandleAddSpecialtyMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		UserIdentifier string `json:"user_identifier"`
		Source         string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserIdentifier == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_identifier is required"})
		return
	}
	if body.Source == "" {
		body.Source = "local"
	}
	if err := h.DB.SpecialtyGroupAddMember(r.Context(), id, body.UserIdentifier, body.Source); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	g, _ := h.DB.SpecialtyGroupGet(r.Context(), id)
	writeJSON(w, http.StatusOK, g)
}

func (h *IdentityGroupsHandler) HandleRemoveSpecialtyMember(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	uid, err := url.PathUnescape(chi.URLParam(r, "uid"))
	if err != nil {
		uid = chi.URLParam(r, "uid")
	}
	if err := h.DB.SpecialtyGroupRemoveMember(r.Context(), id, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
