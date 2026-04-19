// routes.go — HTTP route handlers for the LDAP module API.
// All routes are registered under /api/v1/ldap/ and require admin role.
package ldap

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// RegisterRoutes wires all LDAP API endpoints into the given chi router group.
// All routes require admin role — the caller is responsible for applying the
// requireRole("admin") middleware before calling this function.
func RegisterRoutes(r chi.Router, mgr *Manager) {
	r.Get("/ldap/status", mgr.handleStatus)
	r.Post("/ldap/enable", mgr.handleEnable)
	r.Post("/ldap/disable", mgr.handleDisable)
	r.Post("/ldap/backup", mgr.handleBackup)

	// Users
	r.Get("/ldap/users", mgr.handleListUsers)
	r.Post("/ldap/users", mgr.handleCreateUser)
	r.Put("/ldap/users/{uid}", mgr.handleUpdateUser)
	r.Delete("/ldap/users/{uid}", mgr.handleDeleteUser)
	r.Post("/ldap/users/{uid}/password", mgr.handleSetPassword)
	r.Post("/ldap/users/{uid}/lock", mgr.handleLockUser)
	r.Post("/ldap/users/{uid}/unlock", mgr.handleUnlockUser)

	// Logs
	r.Get("/ldap/logs", mgr.handleLogs)
	r.Get("/ldap/logs/stream", mgr.handleLogsStream)

	// Groups
	r.Get("/ldap/groups", mgr.handleListGroups)
	r.Post("/ldap/groups", mgr.handleCreateGroup)
	r.Delete("/ldap/groups/{cn}", mgr.handleDeleteGroup)
	r.Post("/ldap/groups/{cn}/members", mgr.handleAddGroupMember)
	r.Delete("/ldap/groups/{cn}/members/{uid}", mgr.handleRemoveGroupMember)
}

// ─── Status ───────────────────────────────────────────────────────────────────

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := m.Status(r.Context())
	if err != nil {
		jsonError(w, "failed to read LDAP status", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, status, http.StatusOK)
}

// ─── Enable ───────────────────────────────────────────────────────────────────

func (m *Manager) handleEnable(w http.ResponseWriter, r *http.Request) {
	var req EnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := m.Enable(r.Context(), req); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Location", "/api/v1/ldap/status")
	jsonResponse(w, map[string]string{
		"status":     "provisioning",
		"polling_url": "/api/v1/ldap/status",
	}, http.StatusAccepted)
}

// ─── Disable ──────────────────────────────────────────────────────────────────

func (m *Manager) handleDisable(w http.ResponseWriter, r *http.Request) {
	var req DisableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := m.Disable(r.Context(), req); err != nil {
		var affectedErr *AffectedNodesError
		if errors.As(err, &affectedErr) {
			jsonResponse(w, map[string]interface{}{
				"error":         "nodes are configured with LDAP; set nodes_acknowledged=true to proceed",
				"code":          "affected_nodes",
				"affected_nodes": affectedErr.NodeIDs,
			}, http.StatusConflict)
			return
		}
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	jsonResponse(w, map[string]string{"status": "disabled"}, http.StatusOK)
}

// ─── Backup ───────────────────────────────────────────────────────────────────

func (m *Manager) handleBackup(w http.ResponseWriter, r *http.Request) {
	backupDir := m.cfg.LDAPDataDir + "/backups"
	filename, err := SlapcatBackup(r.Context(), backupDir)
	if err != nil {
		log.Error().Err(err).Msg("ldap: backup failed")
		jsonError(w, "backup failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"filename": filename}, http.StatusOK)
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (m *Manager) handleListUsers(w http.ResponseWriter, r *http.Request) {
	// Read-only: use node-reader bind so this page never hits the admin-password-cache
	// class of error and the admin bind's blast radius is narrowed to writes only.
	dit, err := m.ReaderDIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	users, err := dit.ListUsers()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"users": users, "total": len(users)}, http.StatusOK)
}

func (m *Manager) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := dit.CreateUser(req); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user, err := dit.GetUser(req.UID)
	if err != nil {
		jsonResponse(w, map[string]string{"uid": req.UID}, http.StatusCreated)
		return
	}
	jsonResponse(w, user, http.StatusCreated)
}

func (m *Manager) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := dit.UpdateUser(uid, req); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user, err := dit.GetUser(uid)
	if err != nil {
		jsonResponse(w, map[string]string{"uid": uid}, http.StatusOK)
		return
	}
	jsonResponse(w, user, http.StatusOK)
}

func (m *Manager) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.DeleteUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		jsonError(w, "password is required", http.StatusBadRequest)
		return
	}

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.SetPassword(uid, body.Password); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleLockUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.LockUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "locked"}, http.StatusOK)
}

func (m *Manager) handleUnlockUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.UnlockUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "unlocked"}, http.StatusOK)
}

// ─── Groups ───────────────────────────────────────────────────────────────────

func (m *Manager) handleListGroups(w http.ResponseWriter, r *http.Request) {
	// Read-only: use node-reader bind (same rationale as handleListUsers above).
	dit, err := m.ReaderDIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	groups, err := dit.ListGroups()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"groups": groups, "total": len(groups)}, http.StatusOK)
}

func (m *Manager) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CN        string `json:"cn"`
		GIDNumber int    `json:"gid_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.CN == "" {
		jsonError(w, "cn is required", http.StatusBadRequest)
		return
	}

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.CreateGroup(body.CN, body.GIDNumber); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, LDAPGroup{CN: body.CN, GIDNumber: body.GIDNumber, MemberUIDs: []string{}}, http.StatusCreated)
}

func (m *Manager) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.DeleteGroup(cn); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleAddGroupMember(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	var body struct {
		UID string `json:"uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UID == "" {
		jsonError(w, "uid is required", http.StatusBadRequest)
		return
	}

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.AddGroupMember(cn, body.UID); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	uid := chi.URLParam(r, "uid")

	dit, err := m.DIT(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.RemoveGroupMember(cn, uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func jsonResponse(w http.ResponseWriter, body interface{}, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error().Err(err).Msg("ldap routes: encode response failed")
	}
}

func jsonError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
