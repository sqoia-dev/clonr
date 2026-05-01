// routes.go — HTTP route handlers for the LDAP module API.
// All routes are registered under /api/v1/ldap/ and require admin role.
package ldap

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/posixid"
)

// RegisterRoutes wires all LDAP API endpoints into the given chi router group.
// All routes require admin role — the caller is responsible for applying the
// requireRole("admin") middleware before calling this function.
func RegisterRoutes(r chi.Router, mgr *Manager) {
	r.Get("/ldap/status", mgr.handleStatus)
	r.Post("/ldap/enable", mgr.handleEnable)
	r.Post("/ldap/disable", mgr.handleDisable)
	r.Post("/ldap/backup", mgr.handleBackup)
	r.Post("/ldap/admin/repair", mgr.handleAdminRepair)

	// LDAP-1..2: config read/write and connection test (Sprint 7)
	r.Get("/ldap/config", mgr.handleGetConfig)
	r.Put("/ldap/config", mgr.handlePutConfig)
	r.Post("/ldap/test", mgr.handleTestConnection)

	// LDAP user search (Sprint 7 USERS-3)
	r.Get("/ldap/users/search", mgr.handleSearchUsers)

	// Users
	r.Get("/ldap/users", mgr.handleListUsers)
	r.Post("/ldap/users", mgr.handleCreateUser)
	r.Put("/ldap/users/{uid}", mgr.handleUpdateUser)
	r.Patch("/ldap/users/{uid}", mgr.handlePatchUser) // #95: full edit Sheet
	r.Delete("/ldap/users/{uid}", mgr.handleDeleteUser)
	r.Post("/ldap/users/{uid}/password", mgr.handleSetPassword)
	r.Post("/ldap/users/{uid}/lock", mgr.handleLockUser)
	r.Post("/ldap/users/{uid}/unlock", mgr.handleUnlockUser)

	// Sprint 8 — WRITE-USER-4: admin-driven password reset (generates temp pwd)
	r.Post("/ldap/users/{uid}/reset-password", mgr.handleResetPassword)

	// Logs
	r.Get("/ldap/logs", mgr.handleLogs)
	r.Get("/ldap/logs/stream", mgr.handleLogsStream)

	// Groups
	r.Get("/ldap/groups", mgr.handleListGroups)
	r.Post("/ldap/groups", mgr.handleCreateGroup)
	r.Put("/ldap/groups/{cn}", mgr.handleUpdateGroup)
	r.Delete("/ldap/groups/{cn}", mgr.handleDeleteGroup)
	r.Post("/ldap/groups/{cn}/members", mgr.handleAddGroupMember)
	r.Delete("/ldap/groups/{cn}/members/{uid}", mgr.handleRemoveGroupMember)

	// Sprint 8 — WRITE-GRP-4: per-group direct-write mode toggle
	r.Put("/ldap/groups/{cn}/mode", mgr.handleSetGroupMode)
	r.Get("/ldap/groups/{cn}/mode", mgr.handleGetGroupMode)

	// Sprint 8 — WRITE-CFG-1..3: write-bind config + probe
	r.Put("/ldap/write-bind", mgr.handlePutWriteBind)
	r.Get("/ldap/write-capable", mgr.handleGetWriteCapable)

	// Sudoers
	r.Get("/ldap/sudoers/status", handleSudoersStatus(mgr))
	r.Post("/ldap/sudoers/enable", handleEnableSudoers(mgr))
	r.Post("/ldap/sudoers/disable", handleDisableSudoers(mgr))
	r.Get("/ldap/sudoers/members", handleSudoersMembers(mgr))
	r.Post("/ldap/sudoers/members", handleGrantSudo(mgr))
	r.Delete("/ldap/sudoers/members/{uid}", handleRevokeSudo(mgr))

	// Sprint 9 — internal slapd auto-deploy + source-mode toggle.
	mgr.registerInternalRoutes(r)
	r.Get("/ldap/internal/admin-password", mgr.handleInternalAdminPassword)
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
		"status":      "provisioning",
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
				"error":          "nodes are configured with LDAP; set nodes_acknowledged=true to proceed",
				"code":           "affected_nodes",
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

// ─── Admin repair ─────────────────────────────────────────────────────────────

func (m *Manager) handleAdminRepair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AdminPassword string `json:"admin_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AdminPassword == "" {
		jsonError(w, "admin_password is required", http.StatusBadRequest)
		return
	}

	result, err := m.AdminRepair(r.Context(), body.AdminPassword)
	if err != nil {
		msg := err.Error()
		// Return 422 for password mismatch — 401 would cause api.js to redirect
		// to the login page, which is wrong here. 422 surfaces the error inline.
		if msg == "password does not match the one set on Enable" {
			jsonError(w, "Password does not match the one set on Enable.", http.StatusUnprocessableEntity)
			return
		}
		log.Error().Err(err).Msg("ldap: admin repair failed")
		jsonError(w, msg, http.StatusInternalServerError)
		return
	}
	jsonResponse(w, result, http.StatusOK)
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
	if req.UID == "" {
		jsonError(w, "uid is required", http.StatusBadRequest)
		return
	}

	// UID auto-allocation / validation (#93, #113: use RoleLDAPUser range 10000-60000).
	if req.UIDNumber == 0 {
		uid, err := m.allocator.AllocateUID(r.Context(), posixid.RoleLDAPUser)
		if err != nil {
			jsonErrorPosixID(w, "uid_number", err)
			return
		}
		req.UIDNumber = uid
	} else {
		if err := m.allocator.Validate(r.Context(), req.UIDNumber, posixid.KindUID, posixid.RoleLDAPUser); err != nil {
			jsonErrorPosixID(w, "uid_number", err)
			return
		}
	}

	// GID auto-allocation / validation (#93, #113: use RoleLDAPUser range 10000-60000).
	if req.GIDNumber == 0 {
		gid, err := m.allocator.AllocateGID(r.Context(), posixid.RoleLDAPUser)
		if err != nil {
			jsonErrorPosixID(w, "gid_number", err)
			return
		}
		req.GIDNumber = gid
	} else {
		if err := m.allocator.Validate(r.Context(), req.GIDNumber, posixid.KindGID, posixid.RoleLDAPUser); err != nil {
			jsonErrorPosixID(w, "gid_number", err)
			return
		}
	}

	// #100: if no initial password supplied, auto-generate a strong temp password.
	// The generated value is returned once in the response under "temp_password".
	// forceChange is set so the user must change on first login.
	var tempPwd string
	var autoGenPwd bool
	if req.Password == "" {
		generated, err := generateRandomPassword(20)
		if err != nil {
			jsonError(w, "failed to generate temporary password", http.StatusInternalServerError)
			return
		}
		req.Password = generated
		tempPwd = generated
		autoGenPwd = true
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := dit.CreateUser(req); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// When password was auto-generated, mark it as requiring a change on first login.
	if autoGenPwd {
		if err := dit.SetPassword(req.UID, req.Password, true); err != nil {
			// Non-fatal: user was created, just force_change flag couldn't be set.
			log.Warn().Err(err).Str("uid", req.UID).Msg("ldap: create user: could not set force_change on auto-generated password (non-fatal)")
		}
	}

	// Audit — hash password value, include other attrs as-is.
	attrMap := map[string]string{
		"uid":       req.UID,
		"cn":        req.CN,
		"uidNumber": strings.TrimSpace(fmt.Sprintf("%d", req.UIDNumber)),
		"gidNumber": strings.TrimSpace(fmt.Sprintf("%d", req.GIDNumber)),
	}
	attrMap["userPassword"] = req.Password // always hashed by directoryWriteAudit
	if req.Mail != "" {
		attrMap["mail"] = req.Mail
	}
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserCreated, "ldap_user", req.UID, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(req.UID), "add", attrMap))

	user, err := dit.GetUser(req.UID)
	if autoGenPwd {
		// Always return temp_password when auto-generated, regardless of GetUser success.
		if err != nil {
			jsonResponse(w, map[string]interface{}{
				"uid":          req.UID,
				"temp_password": tempPwd,
				"force_change":  true,
				"note":          "Show this to the operator once. It will not be returned again.",
			}, http.StatusCreated)
			return
		}
		// Merge temp_password into the user response.
		jsonResponse(w, map[string]interface{}{
			"uid":           user.UID,
			"uid_number":    user.UIDNumber,
			"gid_number":    user.GIDNumber,
			"cn":            user.CN,
			"sn":            user.SN,
			"given_name":    user.GivenName,
			"mail":          user.Mail,
			"home_directory": user.HomeDirectory,
			"login_shell":   user.LoginShell,
			"locked":        user.Locked,
			"temp_password": tempPwd,
			"force_change":  true,
			"note":          "Show this to the operator once. It will not be returned again.",
		}, http.StatusCreated)
		return
	}
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

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := dit.UpdateUser(uid, req); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build attr map for audit (only changed attributes).
	attrMap := map[string]string{}
	if req.CN != "" { attrMap["cn"] = req.CN }
	if req.SN != "" { attrMap["sn"] = req.SN }
	if req.HomeDirectory != "" { attrMap["homeDirectory"] = req.HomeDirectory }
	if req.LoginShell != "" { attrMap["loginShell"] = req.LoginShell }
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserUpdated, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(uid), "modify", attrMap))

	user, err := dit.GetUser(uid)
	if err != nil {
		jsonResponse(w, map[string]string{"uid": uid}, http.StatusOK)
		return
	}
	jsonResponse(w, user, http.StatusOK)
}

// handlePatchUser handles PATCH /api/v1/ldap/users/{uid} — Sprint 8 WRITE-USER-2 / Sprint 13 #95.
// Supports full attribute editing: cn/sn/givenName/mail/gidNumber/sshPublicKeys/homeDirectory/loginShell.
// Supplementary group changes are expressed via add_groups/remove_groups on the group entries.
func (m *Manager) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")

	var body struct {
		UpdateUserRequest
		AddGroups    []string `json:"add_groups"`    // groups to add this user to (memberUid)
		RemoveGroups []string `json:"remove_groups"` // groups to remove this user from
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Validate GID if explicitly provided (#113: use RoleLDAPUser range 10000-60000).
	if body.GIDNumber != nil {
		if err := m.allocator.Validate(r.Context(), *body.GIDNumber, posixid.KindGID, posixid.RoleLDAPUser); err != nil {
			jsonErrorPosixID(w, "gid_number", err)
			return
		}
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if err := dit.UpdateUser(uid, body.UpdateUserRequest); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply supplementary group changes — modify the group entries (not the user entry).
	for _, grp := range body.AddGroups {
		if addErr := dit.AddGroupMember(grp, uid); addErr != nil {
			log.Warn().Err(addErr).Str("uid", uid).Str("group", grp).Msg("ldap: patch user: add to group failed")
		}
	}
	for _, grp := range body.RemoveGroups {
		if rmErr := dit.RemoveGroupMember(grp, uid); rmErr != nil {
			log.Warn().Err(rmErr).Str("uid", uid).Str("group", grp).Msg("ldap: patch user: remove from group failed")
		}
	}

	// Build attr map for audit.
	attrMap := map[string]string{"uid": uid}
	if body.CN != "" { attrMap["cn"] = body.CN }
	if body.SN != "" { attrMap["sn"] = body.SN }
	if body.GivenName != "" { attrMap["givenName"] = body.GivenName }
	if body.Mail != "" { attrMap["mail"] = body.Mail }
	if body.GIDNumber != nil { attrMap["gidNumber"] = fmt.Sprintf("%d", *body.GIDNumber) }

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserUpdated, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(uid), "patch", attrMap))

	user, err := dit.GetUser(uid)
	if err != nil {
		jsonResponse(w, map[string]string{"uid": uid}, http.StatusOK)
		return
	}
	jsonResponse(w, user, http.StatusOK)
}

func (m *Manager) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")

	// Safety: check group membership count before deleting.
	readerDIT, rErr := m.ReaderDIT(r.Context())
	if rErr == nil {
		groups, gErr := readerDIT.ListGroups()
		if gErr == nil {
			memberCount := 0
			for _, g := range groups {
				for _, m := range g.MemberUIDs {
					if m == uid {
						memberCount++
						break
					}
				}
			}
			// WRITE-USER-3: 409 if removing would orphan memberships beyond threshold (5).
			const orphanThreshold = 5
			if memberCount > orphanThreshold {
				jsonResponse(w, map[string]interface{}{
					"error":         fmt.Sprintf("user is a member of %d groups; set force=true to proceed", memberCount),
					"code":          "group_member_safety",
					"member_of_count": memberCount,
				}, http.StatusConflict)
				return
			}
		}
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	dn := dit.userDN(uid)
	if err := dit.DeleteUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserDeleted, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dn, "delete", map[string]string{"uid": uid}))

	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	var body struct {
		Password    string `json:"password"`
		ForceChange bool   `json:"force_change"` // default false — backward compat
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		jsonError(w, "password is required", http.StatusBadRequest)
		return
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.SetPassword(uid, body.Password, body.ForceChange); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPPasswordReset, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(uid), "password_set", map[string]string{
			"userPassword": body.Password, // value is hashed inside directoryWriteAudit
		}))

	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleLockUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.LockUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserUpdated, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(uid), "lock", map[string]string{"shadowExpire": "1"}))
	jsonResponse(w, map[string]string{"status": "locked"}, http.StatusOK)
}

func (m *Manager) handleUnlockUser(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")
	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.UnlockUser(uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPUserUpdated, "ldap_user", uid, r.RemoteAddr,
		nil, directoryWriteAudit(dit.userDN(uid), "unlock", map[string]string{"shadowExpire": "deleted"}))
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

// handleCreateGroup handles POST /api/v1/ldap/groups.
// Accepts optional initial_members list for WRITE-GRP-1.
func (m *Manager) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CN             string   `json:"cn"`
		GIDNumber      int      `json:"gid_number"`
		Description    string   `json:"description"`
		InitialMembers []string `json:"initial_members"` // WRITE-GRP-1
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.CN == "" {
		jsonError(w, "cn is required", http.StatusBadRequest)
		return
	}
	if len(body.Description) > 256 {
		jsonError(w, "description must be 256 characters or fewer", http.StatusBadRequest)
		return
	}

	// GID auto-allocation / validation (#93, #113: use RoleLDAPUser range 10000-60000).
	if body.GIDNumber == 0 {
		gid, err := m.allocator.AllocateGID(r.Context(), posixid.RoleLDAPUser)
		if err != nil {
			jsonErrorPosixID(w, "gid_number", err)
			return
		}
		body.GIDNumber = gid
	} else {
		if err := m.allocator.Validate(r.Context(), body.GIDNumber, posixid.KindGID, posixid.RoleLDAPUser); err != nil {
			jsonErrorPosixID(w, "gid_number", err)
			return
		}
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.CreateGroup(body.CN, body.GIDNumber, body.Description); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add initial members if provided.
	for _, uid := range body.InitialMembers {
		if addErr := dit.AddGroupMember(body.CN, uid); addErr != nil {
			log.Warn().Err(addErr).Str("cn", body.CN).Str("uid", uid).Msg("ldap: create group: add initial member failed")
		}
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupCreated, "ldap_group", body.CN, r.RemoteAddr,
		nil, directoryWriteAudit(dit.groupDN(body.CN), "add", map[string]string{
			"cn":        body.CN,
			"gidNumber": fmt.Sprintf("%d", body.GIDNumber),
		}))

	jsonResponse(w, LDAPGroup{CN: body.CN, GIDNumber: body.GIDNumber, MemberUIDs: body.InitialMembers, Description: body.Description}, http.StatusCreated)
}

// handleUpdateGroup handles PUT /api/v1/ldap/groups/{cn}.
// Supports description update and member-list edits (WRITE-GRP-2).
func (m *Manager) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	var body struct {
		Description    string   `json:"description"`
		AddMembers     []string `json:"add_members"`    // WRITE-GRP-2
		RemoveMembers  []string `json:"remove_members"` // WRITE-GRP-2
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(body.Description) > 256 {
		jsonError(w, "description must be 256 characters or fewer", http.StatusBadRequest)
		return
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Update description (may be empty string to clear it).
	if body.Description != "" || len(body.AddMembers)+len(body.RemoveMembers) == 0 {
		if err := dit.UpdateGroup(cn, body.Description); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Apply member changes per memberUid dialect (WRITE-GRP-2).
	for _, uid := range body.AddMembers {
		if addErr := dit.AddGroupMember(cn, uid); addErr != nil {
			log.Warn().Err(addErr).Str("cn", cn).Str("uid", uid).Msg("ldap: update group: add member failed")
		}
	}
	for _, uid := range body.RemoveMembers {
		if rmErr := dit.RemoveGroupMember(cn, uid); rmErr != nil {
			log.Warn().Err(rmErr).Str("cn", cn).Str("uid", uid).Msg("ldap: update group: remove member failed")
		}
	}

	attrMap := map[string]string{"cn": cn}
	if body.Description != "" {
		attrMap["description"] = body.Description
	}
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupUpdated, "ldap_group", cn, r.RemoteAddr,
		nil, directoryWriteAudit(dit.groupDN(cn), "modify", attrMap))

	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")

	// WRITE-GRP-3: 409 if any system reference still uses this group
	// (sudoers group is the primary check in v0.4.0).
	row, cfgErr := m.db.LDAPGetConfig(r.Context())
	if cfgErr == nil && row.SudoersEnabled && row.SudoersGroupCN == cn {
		jsonResponse(w, map[string]interface{}{
			"error": fmt.Sprintf("group %q is the active sudoers group; disable sudoers before deleting", cn),
			"code":  "system_reference",
		}, http.StatusConflict)
		return
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	dn := dit.groupDN(cn)
	if err := dit.DeleteGroup(cn); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupDeleted, "ldap_group", cn, r.RemoteAddr,
		nil, directoryWriteAudit(dn, "delete", map[string]string{"cn": cn}))

	// Also remove the group mode row if it exists.
	_ = m.db.LDAPSetGroupMode(r.Context(), cn, "overlay", "system")

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

	// Check group mode — only write to the directory if mode is "direct".
	// In overlay mode, use the supplementary overlay (Sprint 7 path).
	mode, _ := m.db.LDAPGetGroupMode(r.Context(), cn)
	if mode != "direct" {
		// Overlay mode: caller should use the overlay endpoint instead.
		jsonError(w, `group is in overlay mode; use /api/v1/groups/<dn>/supplementary-members to add members without writing the directory`, http.StatusConflict)
		return
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.AddGroupMember(cn, body.UID); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupUpdated, "ldap_group", cn, r.RemoteAddr,
		nil, directoryWriteAudit(dit.groupDN(cn), "modify_add_member", map[string]string{"memberUid": body.UID}))

	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

func (m *Manager) handleRemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	uid := chi.URLParam(r, "uid")

	mode, _ := m.db.LDAPGetGroupMode(r.Context(), cn)
	if mode != "direct" {
		jsonError(w, `group is in overlay mode; use /api/v1/groups/<dn>/supplementary-members to manage members`, http.StatusConflict)
		return
	}

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := dit.RemoveGroupMember(cn, uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupUpdated, "ldap_group", cn, r.RemoteAddr,
		nil, directoryWriteAudit(dit.groupDN(cn), "modify_remove_member", map[string]string{"memberUid": uid}))

	w.WriteHeader(http.StatusNoContent)
}

// ─── Sudoers ──────────────────────────────────────────────────────────────────

func handleSudoersStatus(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, groupCN, members, err := mgr.SudoersStatus(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]interface{}{
			"enabled":      enabled,
			"group_cn":     groupCN,
			"members":      members,
			"member_count": len(members),
		}, http.StatusOK)
	}
}

func handleEnableSudoers(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := mgr.EnableSudoers(r.Context()); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonResponse(w, map[string]string{"status": "enabled"}, http.StatusOK)
	}
}

func handleDisableSudoers(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := mgr.DisableSudoers(r.Context()); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "disabled"}, http.StatusOK)
	}
}

func handleSudoersMembers(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, members, err := mgr.SudoersStatus(r.Context())
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if members == nil {
			members = []string{}
		}
		jsonResponse(w, map[string]interface{}{
			"members": members,
			"total":   len(members),
		}, http.StatusOK)
	}
}

func handleGrantSudo(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UID string `json:"uid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UID == "" {
			jsonError(w, "uid is required", http.StatusBadRequest)
			return
		}
		if err := mgr.GrantSudo(r.Context(), body.UID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]string{"status": "ok", "uid": body.UID}, http.StatusOK)
	}
}

func handleRevokeSudo(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := chi.URLParam(r, "uid")
		if uid == "" {
			jsonError(w, "uid is required", http.StatusBadRequest)
			return
		}
		if err := mgr.RevokeSudo(r.Context(), uid); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─── LDAP Config (LDAP-1..2, Sprint 7) ───────────────────────────────────────

// LDAPExternalConfig is the operator-facing LDAP config for an external directory.
// Separate from the built-in slapd Enable flow — this is for connecting clustr to
// an existing enterprise LDAP/AD server for user/group browse.
type LDAPExternalConfig struct {
	ServerURL         string `json:"server_url"`
	BaseDN            string `json:"base_dn"`
	BindDN            string `json:"bind_dn"`
	BindPassword      string `json:"bind_password,omitempty"` // write-only; never returned
	UserSearchFilter  string `json:"user_search_filter"`
	GroupSearchFilter string `json:"group_search_filter"`
	TLSMode           string `json:"tls_mode"` // none | starttls | tls
	CACert            string `json:"ca_cert,omitempty"`
}

func (m *Manager) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	row, err := m.db.LDAPGetConfig(r.Context())
	if err != nil {
		// If no row, return an empty config (module not yet enabled).
		jsonResponse(w, map[string]interface{}{
			"enabled":         false,
			"status":          "disabled",
			"base_dn":         "",
			"service_bind_dn": "",
		}, http.StatusOK)
		return
	}

	// Build write-capable status for the WRITE-SAFETY-2 banner.
	writeStatus, writeCapable := m.WriteCapableStatus(r.Context())

	// Return the config, masking the password.
	jsonResponse(w, map[string]interface{}{
		"enabled":         row.Enabled,
		"status":          row.Status,
		"status_detail":   row.StatusDetail,
		"base_dn":         row.BaseDN,
		"base_dn_locked":  row.BaseDNLocked,
		"service_bind_dn": row.ServiceBindDN,
		"ca_fingerprint":  row.CACertFingerprint,
		// bind_password is write-only — never returned. Placeholder indicates if set.
		"bind_password_set": row.AdminPasswd != "",
		// Sprint 8 write-bind fields (WRITE-CFG-3).
		"write_bind_dn_set":   row.WriteBindDN != "",
		"write_capable":       writeCapable,
		"write_status":        writeStatus,
		"write_capable_detail": row.WriteCapableDetail,
		// dialect is always openldap for the built-in slapd (WRITE-DIALECT-1).
		"backend_dialect": string(DialectOpenLDAP),
	}, http.StatusOK)
}

func (m *Manager) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	// PUT /api/v1/ldap/config is the trigger for the enable flow.
	// Delegate to handleEnable for now — the Enable() call accepts base_dn and admin_password.
	m.handleEnable(w, r)
}

func (m *Manager) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	// Test the current LDAP module connection by checking status and doing a health bind.
	status, err := m.Status(r.Context())
	if err != nil {
		jsonError(w, "failed to read LDAP status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !status.Enabled {
		jsonResponse(w, map[string]interface{}{
			"ok":     false,
			"error":  "LDAP module is not enabled",
			"status": status.Status,
		}, http.StatusOK)
		return
	}
	if status.Status != "ready" {
		jsonResponse(w, map[string]interface{}{
			"ok":     false,
			"error":  "LDAP module is not ready: " + status.StatusDetail,
			"status": status.Status,
		}, http.StatusOK)
		return
	}

	// Try a reader bind.
	dit, err := m.ReaderDIT(r.Context())
	if err != nil {
		jsonResponse(w, map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		}, http.StatusOK)
		return
	}
	users, err := dit.ListUsers()
	if err != nil {
		jsonResponse(w, map[string]interface{}{
			"ok":    false,
			"error": "bind succeeded but user search failed: " + err.Error(),
		}, http.StatusOK)
		return
	}
	jsonResponse(w, map[string]interface{}{
		"ok":         true,
		"user_count": len(users),
		"base_dn":    status.BaseDN,
	}, http.StatusOK)
}

// handleSearchUsers handles GET /api/v1/ldap/users/search?q=<query>.
// Returns LDAP user entries matching the query via the reader bind.
func (m *Manager) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")

	dit, err := m.ReaderDIT(r.Context())
	if err != nil {
		// LDAP not ready — return empty list gracefully.
		jsonResponse(w, map[string]interface{}{"users": []interface{}{}, "total": 0}, http.StatusOK)
		return
	}

	users, err := dit.ListUsers()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter by query string if provided.
	if q != "" {
		qLower := strings.ToLower(q)
		var filtered []LDAPUser
		for _, u := range users {
			if strings.Contains(strings.ToLower(u.UID), qLower) ||
				strings.Contains(strings.ToLower(u.GivenName+" "+u.SN), qLower) ||
				strings.Contains(strings.ToLower(u.Mail), qLower) {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}

	if users == nil {
		users = []LDAPUser{}
	}
	jsonResponse(w, map[string]interface{}{"users": users, "total": len(users)}, http.StatusOK)
}

// ─── Sprint 8: write-bind config (WRITE-CFG-1..3) ────────────────────────────

// handlePutWriteBind handles PUT /api/v1/ldap/write-bind.
// Accepts write_bind_dn and write_bind_password, persists them, runs a probe, and
// returns the probe result. Passing empty strings clears the write bind.
func (m *Manager) handlePutWriteBind(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WriteBindDN       string `json:"write_bind_dn"`
		WriteBindPassword string `json:"write_bind_password"` // write-only — never returned
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Audit before saving so we capture intent even if the probe fails.
	actorIP := r.RemoteAddr
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPWriteBindSaved, "ldap", "write_bind", actorIP,
		nil,
		map[string]interface{}{
			"write_bind_dn_set":       body.WriteBindDN != "",
			"write_bind_password_set": body.WriteBindPassword != "",
			"directory_write":         true,
		},
	)

	var probeCapable bool
	var probeDetail string
	err := m.SaveWriteBind(r.Context(), body.WriteBindDN, body.WriteBindPassword,
		func(capable bool, detail string) {
			probeCapable = capable
			probeDetail = detail
			m.audit.Record(r.Context(), "", "", db.AuditActionLDAPWriteProbe, "ldap", "write_bind", actorIP,
				nil,
				map[string]interface{}{
					"directory_write": true,
					"capable":         capable,
					"detail":          detail,
				},
			)
		},
	)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]interface{}{
		"write_bind_dn_set": body.WriteBindDN != "",
		"write_capable":     probeCapable,
		"write_status":      map[string]interface{}{"capable": probeCapable, "detail": probeDetail},
	}, http.StatusOK)
}

// handleGetWriteCapable handles GET /api/v1/ldap/write-capable.
// Returns the cached probe result and write-bind status.
func (m *Manager) handleGetWriteCapable(w http.ResponseWriter, r *http.Request) {
	row, err := m.db.LDAPGetConfig(r.Context())
	if err != nil {
		jsonError(w, "failed to read LDAP config", http.StatusInternalServerError)
		return
	}

	status, capable := m.WriteCapableStatus(r.Context())
	jsonResponse(w, map[string]interface{}{
		"write_bind_dn_set": row.WriteBindDN != "",
		"write_capable":     capable,
		"write_status":      status,
		"write_capable_detail": row.WriteCapableDetail,
	}, http.StatusOK)
}

// ─── Sprint 8: WRITE-USER-4 — admin password reset ───────────────────────────

// handleResetPassword handles POST /api/v1/ldap/users/{uid}/reset-password.
// Generates a cryptographically random temp password, applies it to the LDAP
// entry with forceChange=true, and returns the temp password once in the
// response. The caller MUST show it to the operator and then discard it.
func (m *Manager) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "uid")

	dit, err := m.WriteBind(r.Context())
	if err != nil {
		jsonError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Generate a 20-char random password.
	tempPwd, err := generateRandomPassword(20)
	if err != nil {
		jsonError(w, "failed to generate temporary password", http.StatusInternalServerError)
		return
	}

	if err := dit.SetPassword(uid, tempPwd, true); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Audit — attribute hash only, never the password value.
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPPasswordReset, "ldap_user", uid, r.RemoteAddr,
		nil,
		directoryWriteAudit(dit.userDN(uid), "password_reset", map[string]string{
			"userPassword": tempPwd, // value is hashed inside directoryWriteAudit
		}),
	)

	// Return the temp password exactly once.
	jsonResponse(w, map[string]interface{}{
		"uid":          uid,
		"temp_password": tempPwd,
		"force_change":  true,
		"note":          "Show this to the operator once. It will not be returned again.",
	}, http.StatusOK)
}

// ─── Sprint 8: WRITE-GRP-4 — per-group mode toggle ───────────────────────────

// handleSetGroupMode handles PUT /api/v1/ldap/groups/{cn}/mode.
// Body: {"mode": "overlay"|"direct"}
func (m *Manager) handleSetGroupMode(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Mode != "overlay" && body.Mode != "direct" {
		jsonError(w, `mode must be "overlay" or "direct"`, http.StatusBadRequest)
		return
	}

	// When switching to direct, check write capability.
	if body.Mode == "direct" {
		_, capable := m.WriteCapableStatus(r.Context())
		if !capable {
			jsonError(w, "write bind is not configured or write probe failed — configure a write bind before enabling direct mode", http.StatusConflict)
			return
		}
	}

	if err := m.db.LDAPSetGroupMode(r.Context(), cn, body.Mode, r.RemoteAddr); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPGroupModeChanged, "ldap_group", cn, r.RemoteAddr,
		nil,
		map[string]interface{}{
			"directory_write": true,
			"cn":              cn,
			"mode":            body.Mode,
		},
	)

	jsonResponse(w, map[string]interface{}{"cn": cn, "mode": body.Mode}, http.StatusOK)
}

// handleGetGroupMode handles GET /api/v1/ldap/groups/{cn}/mode.
func (m *Manager) handleGetGroupMode(w http.ResponseWriter, r *http.Request) {
	cn := chi.URLParam(r, "cn")
	mode, err := m.db.LDAPGetGroupMode(r.Context(), cn)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]interface{}{"cn": cn, "mode": mode}, http.StatusOK)
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

// jsonErrorPosixID returns a 400 with a structured body identifying which field
// failed posixid validation and why.
func jsonErrorPosixID(w http.ResponseWriter, field string, err error) {
	code := "posixid_error"
	switch {
	case errors.Is(err, posixid.ErrRangeExhausted):
		code = "range_exhausted"
	case errors.Is(err, posixid.ErrReserved):
		code = "reserved_id"
	case errors.Is(err, posixid.ErrOutOfRange):
		code = "out_of_range"
	case errors.Is(err, posixid.ErrCollision):
		code = "id_collision"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": err.Error(),
		"code":  code,
		"field": field,
	})
}
