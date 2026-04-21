// routes.go — HTTP route handlers for the Network module API.
// All routes are registered under /api/v1/network/ and require admin role.
//
// Phase 1: switch CRUD endpoints are fully implemented.
// Phase 2: profile CRUD, group assignment, OpenSM config, and IB status implemented.
package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// RegisterRoutes wires all Network module API endpoints into the given chi router
// group. All routes require admin role — the caller is responsible for applying
// the requireRole("admin") middleware before calling this function.
func RegisterRoutes(r chi.Router, mgr *Manager) {
	// Switches — fully implemented in Phase 1; discovery + config gen in v1.
	r.Get("/network/switches", mgr.handleListSwitches)
	r.Post("/network/switches", mgr.handleCreateSwitch)
	r.Put("/network/switches/{id}", mgr.handleUpdateSwitch)
	r.Delete("/network/switches/{id}", mgr.handleDeleteSwitch)
	r.Post("/network/switches/{id}/confirm", mgr.handleConfirmSwitch)
	r.Post("/network/switches/{id}/generate-config", mgr.handleGenerateSwitchConfig)

	// Network lint warnings.
	r.Get("/network/lint", mgr.handleLintNetwork)

	// Cabling plan.
	r.Get("/network/cabling-plan", mgr.handleCablingPlan)

	// Profiles — Phase 2.
	r.Get("/network/profiles", mgr.handleListProfiles)
	r.Get("/network/profiles/{id}", mgr.handleGetProfile)
	r.Post("/network/profiles", mgr.handleCreateProfile)
	r.Put("/network/profiles/{id}", mgr.handleUpdateProfile)
	r.Delete("/network/profiles/{id}", mgr.handleDeleteProfile)

	// OpenSM — Phase 2.
	r.Get("/network/opensm", mgr.handleGetOpenSM)
	r.Put("/network/opensm", mgr.handleSetOpenSM)

	// IB status — Phase 2.
	r.Get("/network/ib-status", mgr.handleGetIBStatus)

	// Group network-profile assignment — Phase 2.
	r.Get("/node-groups/{id}/network-profile", mgr.handleGetGroupProfile)
	r.Put("/node-groups/{id}/network-profile", mgr.handleAssignGroupProfile)
	r.Delete("/node-groups/{id}/network-profile", mgr.handleUnassignGroupProfile)
}

// ─── Switch handlers ──────────────────────────────────────────────────────────

func (m *Manager) handleListSwitches(w http.ResponseWriter, r *http.Request) {
	switches, err := m.ListSwitches(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("network: list switches")
		jsonError(w, "failed to list switches", http.StatusInternalServerError)
		return
	}
	if switches == nil {
		switches = []api.NetworkSwitch{}
	}
	jsonResponse(w, map[string]interface{}{"switches": switches, "total": len(switches)}, http.StatusOK)
}

func (m *Manager) handleCreateSwitch(w http.ResponseWriter, r *http.Request) {
	var s api.NetworkSwitch
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := m.CreateSwitch(r.Context(), s)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, created, http.StatusCreated)
}

func (m *Manager) handleUpdateSwitch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var s api.NetworkSwitch
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	updated, err := m.UpdateSwitch(r.Context(), id, s)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, updated, http.StatusOK)
}

func (m *Manager) handleDeleteSwitch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := m.DeleteSwitch(r.Context(), id); err != nil {
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("id", id).Msg("network: delete switch")
		jsonError(w, "failed to delete switch", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleConfirmSwitch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var s api.NetworkSwitch
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	confirmed, err := m.ConfirmSwitch(r.Context(), id, s)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, confirmed, http.StatusOK)
}

// ─── Profile handlers ─────────────────────────────────────────────────────────

func (m *Manager) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := m.ListProfiles(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("network: list profiles")
		jsonError(w, "failed to list profiles", http.StatusInternalServerError)
		return
	}
	if profiles == nil {
		profiles = []api.NetworkProfile{}
	}
	jsonResponse(w, map[string]interface{}{"profiles": profiles, "total": len(profiles)}, http.StatusOK)
}

func (m *Manager) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := m.GetProfile(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("id", id).Msg("network: get profile")
		jsonError(w, "failed to get profile", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, p, http.StatusOK)
}

func (m *Manager) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var p api.NetworkProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := m.CreateProfile(r.Context(), p)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, created, http.StatusCreated)
}

func (m *Manager) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var p api.NetworkProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	updated, err := m.UpdateProfile(r.Context(), id, p)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, updated, http.StatusOK)
}

func (m *Manager) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	groups, err := m.DeleteProfile(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrProfileInUse) {
			groupCount := len(groups)
			body := map[string]interface{}{
				"error":  fmt.Sprintf("profile is assigned to %d group(s)", groupCount),
				"code":   "profile_in_use",
				"groups": groups,
			}
			jsonResponse(w, body, http.StatusConflict)
			return
		}
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("id", id).Msg("network: delete profile")
		jsonError(w, "failed to delete profile", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Group assignment handlers ────────────────────────────────────────────────

func (m *Manager) handleGetGroupProfile(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	p, err := m.GetGroupProfile(r.Context(), groupID)
	if err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("network: get group profile")
		jsonError(w, "failed to get group profile", http.StatusInternalServerError)
		return
	}
	if p == nil {
		jsonError(w, "no network profile assigned to this group", http.StatusNotFound)
		return
	}
	jsonResponse(w, p, http.StatusOK)
}

func (m *Manager) handleAssignGroupProfile(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")

	var body struct {
		ProfileID string `json:"profile_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.ProfileID == "" {
		jsonErrorCode(w, "profile_id is required", "validation_error", http.StatusBadRequest)
		return
	}

	if err := m.AssignProfileToGroup(r.Context(), groupID, body.ProfileID); err != nil {
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("group_id", groupID).Str("profile_id", body.ProfileID).Msg("network: assign group profile")
		jsonError(w, "failed to assign profile to group", http.StatusInternalServerError)
		return
	}

	// Return the full profile so the client has the current state.
	p, err := m.GetGroupProfile(r.Context(), groupID)
	if err != nil || p == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	jsonResponse(w, p, http.StatusOK)
}

func (m *Manager) handleUnassignGroupProfile(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	if err := m.UnassignProfileFromGroup(r.Context(), groupID); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("network: unassign group profile")
		jsonError(w, "failed to unassign profile from group", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── OpenSM handlers ──────────────────────────────────────────────────────────

func (m *Manager) handleGetOpenSM(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.GetOpenSMConfig(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("network: get opensm config")
		jsonError(w, "failed to get OpenSM config", http.StatusInternalServerError)
		return
	}
	if cfg == nil {
		// Return a stub so the UI always gets a valid object.
		jsonResponse(w, api.OpenSMConfig{Enabled: false, LogPrefix: "/var/log/opensm"}, http.StatusOK)
		return
	}
	jsonResponse(w, cfg, http.StatusOK)
}

func (m *Manager) handleSetOpenSM(w http.ResponseWriter, r *http.Request) {
	var cfg api.OpenSMConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	saved, err := m.SetOpenSMConfig(r.Context(), cfg)
	if err != nil {
		jsonErrorCode(w, err.Error(), "validation_error", http.StatusBadRequest)
		return
	}
	jsonResponse(w, saved, http.StatusOK)
}

// ─── IB status handler ────────────────────────────────────────────────────────

func (m *Manager) handleGetIBStatus(w http.ResponseWriter, r *http.Request) {
	status, err := m.GetIBStatus(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("network: get ib status")
		jsonError(w, "failed to get IB status", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, status, http.StatusOK)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, body interface{}, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("network routes: encode response failed")
	}
}

func jsonError(w http.ResponseWriter, message string, code int) {
	jsonErrorCode(w, message, "", code)
}

func jsonErrorCode(w http.ResponseWriter, message, errCode string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := api.ErrorResponse{Error: message, Code: errCode}
	_ = json.NewEncoder(w).Encode(resp)
}

// isNotFound checks whether an error indicates a record was not found.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}
