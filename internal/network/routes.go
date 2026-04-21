// routes.go — HTTP route handlers for the Network module API.
// All routes are registered under /api/v1/network/ and require admin role.
//
// Phase 1: switch CRUD endpoints are fully implemented.
// All other endpoints return 501 Not Implemented — filled in by later phases.
package network

import (
	"encoding/json"
	"errors"
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
	// Switches — fully implemented in Phase 1.
	r.Get("/network/switches", mgr.handleListSwitches)
	r.Post("/network/switches", mgr.handleCreateSwitch)
	r.Put("/network/switches/{id}", mgr.handleUpdateSwitch)
	r.Delete("/network/switches/{id}", mgr.handleDeleteSwitch)

	// Profiles — Phase 2.
	r.Get("/network/profiles", notImplemented)
	r.Get("/network/profiles/{id}", notImplemented)
	r.Post("/network/profiles", notImplemented)
	r.Put("/network/profiles/{id}", notImplemented)
	r.Delete("/network/profiles/{id}", notImplemented)

	// OpenSM — Phase 3.
	r.Get("/network/opensm", notImplemented)
	r.Put("/network/opensm", notImplemented)

	// IB status — Phase 3.
	r.Get("/network/ib-status", notImplemented)

	// Group network-profile assignment — Phase 2.
	r.Get("/node-groups/{id}/network-profile", notImplemented)
	r.Put("/node-groups/{id}/network-profile", notImplemented)
	r.Delete("/node-groups/{id}/network-profile", notImplemented)
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

// ─── Stub handler ─────────────────────────────────────────────────────────────

func notImplemented(w http.ResponseWriter, r *http.Request) {
	jsonErrorCode(w, "not implemented — coming in a later phase", "not_implemented", http.StatusNotImplemented)
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
