// internal_routes.go — HTTP handlers for the internal slapd auto-deploy flow (Sprint 9).
//
// New endpoints registered by RegisterRoutes:
//   GET  /ldap/source-mode           — returns current mode ("internal"|"external")
//   PUT  /ldap/source-mode           — switches mode; requires typed confirm
//   POST /ldap/internal/enable       — provisions slapd; wraps Manager.Enable()
//   GET  /ldap/internal/status       — runtime slapd state for polling
//   POST /ldap/internal/disable      — stops slapd + wipes data dir by default;
//                                      set {preserve_data:true} to keep data
//   POST /ldap/internal/destroy      — stops + wipes data; requires confirm:"destroy"
//   POST /ldap/internal/repair-dit   — idempotent seed-or-repair of dc=cluster,...
//                                      DIT against running slapd; v0.1.15 fix.
package ldap

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/sysd"
)

// ─── Internal route registration (called from RegisterRoutes) ─────────────────

func (m *Manager) registerInternalRoutes(r chi.Router) {
	r.Get("/ldap/source-mode", m.handleGetSourceMode)
	r.Put("/ldap/source-mode", m.handlePutSourceMode)
	r.Post("/ldap/internal/enable", m.handleInternalEnable)
	r.Get("/ldap/internal/status", m.handleInternalStatus)
	r.Post("/ldap/internal/disable", m.handleInternalDisable)
	r.Post("/ldap/internal/destroy", m.handleInternalDestroy)
	r.Post("/ldap/internal/repair-dit", m.handleInternalRepairDIT)
}

// handleInternalRepairDIT re-runs the idempotent DIT seed against the live
// slapd instance, recovering installs where ldap_module_config.status=ready
// but the data backend is empty.
//
// Body (optional): {} — no parameters, no typed-confirm required because the
// operation is idempotent and read-mostly (only writes entries that are
// missing, plus a userPassword self-heal on the node-reader service account).
//
// Returns 200 OK with {"status":"ok"} on success, 422 with the error text on
// any seed failure.
func (m *Manager) handleInternalRepairDIT(w http.ResponseWriter, r *http.Request) {
	if err := m.RepairDIT(r.Context()); err != nil {
		log.Error().Err(err).Msg("ldap: repair-dit endpoint failed")
		jsonError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPDITRepaired,
		"ldap_module", "1", r.RemoteAddr,
		nil, map[string]interface{}{"op": "repair_dit"},
	)

	jsonResponse(w, map[string]string{"status": "ok"}, http.StatusOK)
}

// ─── Source mode ──────────────────────────────────────────────────────────────

// handleGetSourceMode returns {"source_mode":"internal"|"external"}.
func (m *Manager) handleGetSourceMode(w http.ResponseWriter, r *http.Request) {
	mode, err := m.db.LDAPGetSourceMode(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("ldap: get source mode")
		mode = "internal"
	}
	jsonResponse(w, map[string]string{"source_mode": mode}, http.StatusOK)
}

// putSourceModeRequest is the body for PUT /api/v1/ldap/source-mode.
type putSourceModeRequest struct {
	// Mode is the desired mode: "internal" or "external".
	Mode string `json:"mode"`
	// Confirm must equal the value of Mode for the switch to proceed (typed-confirm).
	Confirm string `json:"confirm"`
}

// handlePutSourceMode switches the LDAP source mode.
// Requires Confirm == Mode (typed-confirm for mode switches, MODE-4).
//
// When the current mode is "internal" and slapd is running, the response
// includes running:true so the web layer can prompt the operator to choose
// leave-running vs stop (DISABLE-3). The mode switch itself proceeds regardless
// — clustr just records the intent; the operator can call /disable later.
func (m *Manager) handlePutSourceMode(w http.ResponseWriter, r *http.Request) {
	var req putSourceModeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Mode != "internal" && req.Mode != "external" {
		jsonError(w, `mode must be "internal" or "external"`, http.StatusBadRequest)
		return
	}
	if req.Confirm != req.Mode {
		jsonError(w, fmt.Sprintf("typed confirm %q does not match mode %q", req.Confirm, req.Mode), http.StatusBadRequest)
		return
	}

	current, _ := m.db.LDAPGetSourceMode(r.Context())
	if current == req.Mode {
		// No-op — already in requested mode.
		jsonResponse(w, map[string]interface{}{"source_mode": req.Mode, "changed": false}, http.StatusOK)
		return
	}

	if err := m.db.LDAPSetSourceMode(r.Context(), req.Mode); err != nil {
		log.Error().Err(err).Str("mode", req.Mode).Msg("ldap: set source mode")
		jsonError(w, "failed to save source mode: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Emit audit event.
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPModeSwitched,
		"ldap_module", "1", r.RemoteAddr,
		nil, map[string]interface{}{"from": current, "to": req.Mode},
	)

	// If switching away from internal, report whether slapd is currently running
	// so the web layer can surface the leave-running-or-stop prompt (DISABLE-3).
	// Use live systemd query — no cached state.
	slapdSt, _ := SlapdStatus()
	slapdRunning := slapdSt.Active == "active" || slapdSt.ActiveState == "active"

	jsonResponse(w, map[string]interface{}{
		"source_mode":       req.Mode,
		"changed":           true,
		"slapd_was_running": slapdRunning && current == "internal",
	}, http.StatusOK)
}

// ─── Internal enable ──────────────────────────────────────────────────────────

// internalEnableRequest is the body for POST /api/v1/ldap/internal/enable.
type internalEnableRequest struct {
	BaseDN        string `json:"base_dn"`
	AdminPassword string `json:"admin_password,omitempty"`
}

// InternalEnableError is the structured error response for enable failures (ENABLE-2).
type InternalEnableError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation"`
	DiagCmd     string `json:"diag_cmd,omitempty"`
}

// handleInternalEnable provisions the built-in slapd.
// Returns 202 Accepted immediately and sets the module into provisioning state.
// The web layer polls GET /ldap/internal/status for completion.
//
// Structured error codes (ENABLE-2):
//   port_in_use         — port 636 is already occupied
//   slapd_not_installed — openldap-servers not available via dnf
//   selinux_denied      — SELinux is enforcing and rejecting slapd
//   unit_failed_to_start — systemd unit exited non-zero
func (m *Manager) handleInternalEnable(w http.ResponseWriter, r *http.Request) {
	var req internalEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Default base_dn if not provided.
	if req.BaseDN == "" {
		req.BaseDN = "dc=cluster,dc=local"
	}

	// Pre-flight: detect likely failure modes before kicking off provisioning.
	if code, iErr := preflightInternalEnable(); code != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(iErr)
		return
	}

	// Auto-generate admin password if not supplied.
	if req.AdminPassword == "" {
		pwd, err := generateRandomPassword(24)
		if err != nil {
			jsonError(w, "failed to generate admin password: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.AdminPassword = pwd
	}

	enableReq := EnableRequest{
		BaseDN:        req.BaseDN,
		AdminPassword: req.AdminPassword,
	}

	if err := m.Enable(r.Context(), enableReq); err != nil {
		// Map known error strings to structured codes.
		iErr := mapEnableError(err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(iErr)
		return
	}

	// Emit audit event.
	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPInternalEnabled,
		"ldap_module", "1", r.RemoteAddr,
		nil, map[string]interface{}{"base_dn": req.BaseDN},
	)

	w.Header().Set("Location", "/api/v1/ldap/internal/status")
	jsonResponse(w, map[string]string{
		"status":      "provisioning",
		"polling_url": "/api/v1/ldap/internal/status",
	}, http.StatusAccepted)
}

// preflightInternalEnable detects common failure modes before triggering provisioning.
// Returns ("", nil) when no pre-flight issue is detected.
func preflightInternalEnable() (string, *InternalEnableError) {
	// Check port 636 availability.
	if isPortInUse(636) {
		return "port_in_use", &InternalEnableError{
			Code:        "port_in_use",
			Message:     "Port 636 (LDAPS) is already in use",
			Remediation: "Stop the process occupying port 636 before enabling the internal LDAP server.",
			DiagCmd:     "ss -tlnp | grep :636",
		}
	}
	return "", nil
}

// isPortInUse returns true if a TCP port is already bound on the host.
func isPortInUse(port int) bool {
	out, err := exec.Command("ss", "-tlnH", fmt.Sprintf("sport = :%d", port)).Output()
	if err != nil {
		// ss not available — skip check.
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// mapEnableError converts a raw Enable() error into a structured InternalEnableError.
func mapEnableError(err error) *InternalEnableError {
	// Check for port-in-use error from sysd.Enable first.
	var piue *sysd.PortInUseError
	if errors.As(err, &piue) {
		return &InternalEnableError{
			Code:        "port_in_use",
			Message:     fmt.Sprintf("Port %s (LDAPS) is already in use", sysd.FormatPort(piue.Port)),
			Remediation: fmt.Sprintf("Stop the process occupying port %s before enabling the internal LDAP server.", sysd.FormatPort(piue.Port)),
			DiagCmd:     fmt.Sprintf("ss -tlnp | grep :%s", sysd.FormatPort(piue.Port)),
		}
	}

	msg := err.Error()

	switch {
	case strings.Contains(msg, "openldap-servers install failed") || strings.Contains(msg, "dnf") || strings.Contains(msg, "package not found"):
		return &InternalEnableError{
			Code:        "slapd_not_installed",
			Message:     "openldap-servers could not be installed: " + msg,
			Remediation: "Install openldap-servers manually: sudo dnf install openldap-servers",
			DiagCmd:     "dnf info openldap-servers",
		}
	case strings.Contains(msg, "selinux") || strings.Contains(msg, "SELinux") || strings.Contains(msg, "avc: denied") || strings.Contains(msg, "permission denied") && strings.Contains(msg, "slapd"):
		return &InternalEnableError{
			Code:        "selinux_denied",
			Message:     "SELinux blocked slapd: " + msg,
			Remediation: "Run: sealert -a /var/log/audit/audit.log | tail -40",
			DiagCmd:     "ausearch -c slapd --raw | audit2why",
		}
	case strings.Contains(msg, "start slapd failed") || strings.Contains(msg, "unit_failed") || strings.Contains(msg, "clustr-slapd") && strings.Contains(msg, "failed"):
		return &InternalEnableError{
			Code:        "unit_failed_to_start",
			Message:     "clustr-slapd.service failed to start: " + msg,
			Remediation: "Check the unit status and logs for details.",
			DiagCmd:     "systemctl status clustr-slapd.service",
		}
	default:
		return &InternalEnableError{
			Code:        "enable_failed",
			Message:     msg,
			Remediation: "Check the server logs for more details.",
			DiagCmd:     "journalctl -u clustr-serverd --since '5 minutes ago'",
		}
	}
}

// ─── Internal status ──────────────────────────────────────────────────────────

// InternalStatusResponse is the response for GET /api/v1/ldap/internal/status (ENABLE-3).
type InternalStatusResponse struct {
	// Enabled reflects the DB enabled flag.
	Enabled bool `json:"enabled"`
	// Status is the DB status: "disabled"|"provisioning"|"ready"|"error".
	Status string `json:"status"`
	// StatusDetail is a human-readable description of the current state.
	StatusDetail string `json:"status_detail,omitempty"`
	// BaseDN is the base distinguished name (populated after provisioning).
	BaseDN string `json:"base_dn,omitempty"`
	// Running is true when clustr-slapd.service is active.
	// Derived from live systemd state — never cached.
	Running bool `json:"running"`
	// Port is the LDAPS port (always 636 for internal mode).
	Port int `json:"port"`
	// UptimeSec is the slapd service uptime in seconds (0 when not running).
	UptimeSec int64 `json:"uptime_sec"`
	// AdminPasswordSet indicates whether an admin password has been stored.
	AdminPasswordSet bool `json:"admin_password_set"`
	// SourceMode mirrors ldap_module_config.source_mode.
	SourceMode string `json:"source_mode"`
	// SystemdActive is the live ActiveState from systemd ("active", "inactive",
	// "failed", etc.). Populated from sysd.QueryStatus on every request.
	SystemdActive string `json:"systemd_active,omitempty"`
	// SystemdEnabled is the live UnitFileState from systemd ("enabled",
	// "disabled", "masked", etc.). Populated from sysd.QueryStatus.
	SystemdEnabled string `json:"systemd_enabled,omitempty"`
	// UIButtons is the recommended button set for the UI derived from live
	// systemd state. Values: "enable", "disable", "takeover", "stop", "start".
	UIButtons []string `json:"ui_buttons,omitempty"`
}

// handleInternalStatus returns the runtime state of the internal slapd.
// Always queries systemd live — no cached state.
func (m *Manager) handleInternalStatus(w http.ResponseWriter, r *http.Request) {
	mode, _ := m.db.LDAPGetSourceMode(r.Context())

	row, err := m.db.LDAPGetConfig(r.Context())
	if err != nil {
		// Module not yet configured — return baseline.
		jsonResponse(w, InternalStatusResponse{
			Status:     "disabled",
			Port:       636,
			SourceMode: mode,
		}, http.StatusOK)
		return
	}

	// Query live systemd state — no caching.
	slapdSt, _ := SlapdStatus()
	running := slapdSt.Active == "active" || slapdSt.ActiveState == "active"
	uptime := slapdUptimeSec()
	buttons := sysd.ButtonState(slapdSt)

	jsonResponse(w, InternalStatusResponse{
		Enabled:          row.Enabled,
		Status:           row.Status,
		StatusDetail:     row.StatusDetail,
		BaseDN:           row.BaseDN,
		Running:          running,
		Port:             636,
		UptimeSec:        uptime,
		AdminPasswordSet: row.AdminPasswd != "",
		SourceMode:       mode,
		SystemdActive:    slapdSt.Active,
		SystemdEnabled:   slapdSt.UnitFileState,
		UIButtons:        buttons,
	}, http.StatusOK)
}

// slapdUptimeSec returns the uptime of clustr-slapd.service in seconds.
// Returns 0 if the service is not running or if systemctl cannot be queried.
func slapdUptimeSec() int64 {
	out, err := exec.Command(
		"systemctl", "show", "clustr-slapd.service",
		"--property=ActiveEnterTimestamp", "--value",
	).Output()
	if err != nil {
		return 0
	}
	ts := strings.TrimSpace(string(out))
	if ts == "" || ts == "0" || ts == "n/a" {
		return 0
	}
	// systemd timestamp format: "Mon 2006-01-02 15:04:05 MST"
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", ts)
	if err != nil {
		// Try without weekday.
		t, err = time.Parse("2006-01-02 15:04:05 MST", ts)
		if err != nil {
			return 0
		}
	}
	sec := int64(time.Since(t).Seconds())
	if sec < 0 {
		return 0
	}
	return sec
}

// ─── Internal disable / destroy ───────────────────────────────────────────────

// internalDisableRequest is the optional body for POST /api/v1/ldap/internal/disable.
// All fields are optional — an empty body triggers the default (wipe data).
type internalDisableRequest struct {
	// PreserveData keeps the slapd data directory intact when true.
	// Default (omitted or false): data dir is wiped on disable to prevent
	// state divergence on re-enable (see feedback_no_preserve_default.md).
	PreserveData bool `json:"preserve_data"`
}

// handleInternalDisable stops clustr-slapd.service.
//
// Default behavior (no body or {preserve_data: false}): wipes the slapd data
// directory at /var/lib/clustr/ldap/data/ and the cn=config directory so that
// a subsequent Enable() always provisions from a clean slate.
//
// Set {preserve_data: true} to keep the data dir (advanced — risk of drift).
func (m *Manager) handleInternalDisable(w http.ResponseWriter, r *http.Request) {
	// Parse optional body. Empty body is valid — defaults to preserve_data=false (wipe).
	var req internalDisableRequest
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if err := StopSlapd(r.Context()); err != nil {
		log.Warn().Err(err).Msg("ldap: internal disable: stop slapd (non-fatal)")
	}

	// Wipe persistent state unless the operator explicitly opted in to preserve.
	ldapDataDir := m.cfg.LDAPDataDir
	dataDir := ldapDataDir + "/data"
	configDir := m.cfg.LDAPConfigDir + "/slapd.d"

	if !req.PreserveData {
		log.Info().Str("data_dir", dataDir).Msg("ldap: disable: wiping data dir (default behavior)")
		if err := os.RemoveAll(dataDir); err != nil {
			log.Error().Err(err).Str("dir", dataDir).Msg("ldap: disable: wipe data dir (non-fatal)")
		}
		if err := os.RemoveAll(configDir); err != nil {
			log.Error().Err(err).Str("dir", configDir).Msg("ldap: disable: wipe config dir (non-fatal)")
		}
	} else {
		log.Info().Msg("ldap: disable: preserve_data=true — data dir retained")
	}

	if err := m.db.LDAPDisable(r.Context()); err != nil {
		log.Error().Err(err).Msg("ldap: internal disable: db disable")
		jsonError(w, "failed to disable LDAP module: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear in-memory passwords.
	m.mu.Lock()
	m.adminPassword = ""
	m.servicePassword = ""
	m.mu.Unlock()

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPInternalDisabled,
		"ldap_module", "1", r.RemoteAddr,
		nil, map[string]interface{}{"data_preserved": req.PreserveData},
	)

	jsonResponse(w, map[string]string{"status": "disabled"}, http.StatusOK)
}

// internalDestroyRequest is the body for POST /api/v1/ldap/internal/destroy.
type internalDestroyRequest struct {
	// Confirm must be the literal string "destroy" (typed-confirm, DISABLE-1).
	Confirm string `json:"confirm"`
}

// handleInternalDestroy stops slapd and wipes the data directory (DISABLE-1).
// Requires {"confirm":"destroy"} in the request body.
func (m *Manager) handleInternalDestroy(w http.ResponseWriter, r *http.Request) {
	var req internalDestroyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Confirm != "destroy" {
		jsonError(w, `confirm must be the literal string "destroy"`, http.StatusBadRequest)
		return
	}

	disableReq := DisableRequest{
		Mode:              DisableModeDestroy,
		NodesAcknowledged: true,
	}
	if err := m.Disable(r.Context(), disableReq); err != nil {
		log.Error().Err(err).Msg("ldap: internal destroy")
		jsonError(w, "destroy failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	m.audit.Record(r.Context(), "", "", db.AuditActionLDAPInternalDestroyed,
		"ldap_module", "1", r.RemoteAddr,
		nil, map[string]interface{}{"data_wiped": true},
	)

	jsonResponse(w, map[string]string{"status": "destroyed"}, http.StatusOK)
}

// ─── Admin password recovery (one-time show, ENABLE-6) ───────────────────────

// handleInternalAdminPassword returns the stored admin password (plaintext) for
// one-time recovery display. The web layer should immediately mark the value as
// "shown" and not re-request it.
//
// This endpoint requires the LDAP module to be in the ready state and the
// in-memory admin password to be populated.
func (m *Manager) handleInternalAdminPassword(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	pwd := m.adminPassword
	m.mu.RUnlock()

	if pwd == "" {
		// Fall back to DB value (post-restart recovery).
		row, err := m.db.LDAPGetConfig(r.Context())
		if err != nil || row.AdminPasswd == "" {
			jsonError(w, "admin password not available; re-run Enable to restore", http.StatusNotFound)
			return
		}
		pwd = row.AdminPasswd
	}

	jsonResponse(w, map[string]string{"admin_password": pwd}, http.StatusOK)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// uptimeSecondsFromTimestamp parses a systemd timestamp string and returns elapsed seconds.
// Exported for test use.
func uptimeSecondsFromTimestamp(ts string) int64 {
	ts = strings.TrimSpace(ts)
	if ts == "" || ts == "0" || ts == "n/a" {
		return 0
	}
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", ts)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05 MST", ts)
		if err != nil {
			return 0
		}
	}
	sec := int64(time.Since(t).Seconds())
	if sec < 0 {
		return 0
	}
	return sec
}

