// routes.go — HTTP route handlers for the System Accounts module API.
// All routes are registered under /api/v1/system/ and require admin role.
package sysaccounts

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// RegisterRoutes wires all System Accounts API endpoints into the given chi router
// group. All routes require admin role — the caller is responsible for applying
// the requireRole("admin") middleware before calling this function.
func RegisterRoutes(r chi.Router, mgr *Manager) {
	// Groups
	r.Get("/system/groups", mgr.handleListGroups)
	r.Post("/system/groups", mgr.handleCreateGroup)
	r.Put("/system/groups/{id}", mgr.handleUpdateGroup)
	r.Delete("/system/groups/{id}", mgr.handleDeleteGroup)

	// Accounts
	r.Get("/system/accounts", mgr.handleListAccounts)
	r.Post("/system/accounts", mgr.handleCreateAccount)
	r.Put("/system/accounts/{id}", mgr.handleUpdateAccount)
	r.Delete("/system/accounts/{id}", mgr.handleDeleteAccount)
}

// ─── Groups ───────────────────────────────────────────────────────────────────

func (m *Manager) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := m.Groups(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("sysaccounts: list groups")
		jsonError(w, "failed to list system groups", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []api.SystemGroup{}
	}
	jsonResponse(w, map[string]interface{}{"groups": groups, "total": len(groups)}, http.StatusOK)
}

func (m *Manager) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var g api.SystemGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := m.CreateGroup(r.Context(), g)
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

func (m *Manager) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var g api.SystemGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	updated, err := m.UpdateGroup(r.Context(), id, g)
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

func (m *Manager) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := m.DeleteGroup(r.Context(), id); err != nil {
		if errors.Is(err, ErrConflict) {
			jsonErrorCode(w, err.Error(), "conflict", http.StatusConflict)
			return
		}
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("id", id).Msg("sysaccounts: delete group")
		jsonError(w, "failed to delete group", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Accounts ─────────────────────────────────────────────────────────────────

func (m *Manager) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := m.Accounts(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("sysaccounts: list accounts")
		jsonError(w, "failed to list system accounts", http.StatusInternalServerError)
		return
	}
	if accounts == nil {
		accounts = []api.SystemAccount{}
	}
	jsonResponse(w, map[string]interface{}{"accounts": accounts, "total": len(accounts)}, http.StatusOK)
}

func (m *Manager) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	// Decode into a helper struct with a pointer for system_account so we can
	// distinguish "not sent" (nil) from "sent false".
	var raw struct {
		Username      string  `json:"username"`
		UID           int     `json:"uid"`
		PrimaryGID    int     `json:"primary_gid"`
		Shell         string  `json:"shell"`
		HomeDir       string  `json:"home_dir"`
		CreateHome    bool    `json:"create_home"`
		SystemAccount *bool   `json:"system_account"`
		Comment       string  `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	a := api.SystemAccount{
		Username:   raw.Username,
		UID:        raw.UID,
		PrimaryGID: raw.PrimaryGID,
		Shell:      raw.Shell,
		HomeDir:    raw.HomeDir,
		CreateHome: raw.CreateHome,
		Comment:    raw.Comment,
	}
	// system_account defaults to true (convention for service accounts).
	if raw.SystemAccount != nil {
		a.SystemAccount = *raw.SystemAccount
	} else {
		a.SystemAccount = true
	}

	EnsureDefaults(&a)

	created, err := m.CreateAccount(r.Context(), a)
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

func (m *Manager) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var a api.SystemAccount
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	EnsureDefaults(&a)

	updated, err := m.UpdateAccount(r.Context(), id, a)
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

func (m *Manager) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := m.DeleteAccount(r.Context(), id); err != nil {
		if isNotFound(err) {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		log.Error().Err(err).Str("id", id).Msg("sysaccounts: delete account")
		jsonError(w, "failed to delete account", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, body interface{}, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("sysaccounts routes: encode response failed")
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
