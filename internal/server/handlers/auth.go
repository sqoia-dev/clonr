package handlers

// auth.go — login / logout / me / set-password handlers (ADR-0006, ADR-0007).
//
// POST /api/v1/auth/login         — accepts username+password (primary) or key (deprecated)
// POST /api/v1/auth/logout        — clears session cookie
// GET  /api/v1/auth/me            — returns session info if valid
// POST /api/v1/auth/set-password  — change password for the logged-in user

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// AuthHandler handles the browser session auth endpoints.
// It is intentionally transport-agnostic — all session token logic lives in
// the server package (session.go); the handler receives the resolved functions
// via its function fields so it does not import the server package (avoiding
// circular deps — server imports handlers).
type AuthHandler struct {
	// HasAdminUser returns true when at least one active admin user exists.
	// Used by GET /api/v1/auth/status for first-run detection.
	HasAdminUser func() (bool, error)

	// LoginWithKey validates a raw API key string (deprecated legacy path).
	// Returns (keyPrefix, scope, ok). keyPrefix is the first 8 chars of raw key.
	LoginWithKey func(rawKey string) (keyPrefix string, scope string, ok bool)

	// LoginWithPassword looks up a user by username, checks the bcrypt hash,
	// and returns the user's ID, role, and mustChangePassword flag on success.
	LoginWithPassword func(username, password string) (userID, role string, mustChange bool, err error)

	// SignForUser returns a signed session token for the given user ID and role.
	SignForUser func(userID, role string) (token string, exp time.Time, err error)

	// SignForKey returns a signed session token for the given API key prefix (deprecated).
	SignForKey func(keyPrefix string) (token string, exp time.Time, err error)

	// Validate checks a token and returns (sub, role, exp, needsReissue, newToken, ok).
	Validate func(token string) (sub, role string, exp time.Time, needsReissue bool, newToken string, ok bool)

	// SetPassword changes the password for a user, given the current bcrypt hash.
	// Returns the new session token and expiry.
	SetPassword func(userID, currentPassword, newPassword string) (token string, exp time.Time, err error)

	// GetUserGroups returns the group IDs assigned to the user (for operator role scoping).
	// May be nil — if nil, HandleMe omits the assigned_groups field.
	GetUserGroups func(userID string) ([]string, error)

	// GetUsername returns the username string for a user ID.
	// May be nil — if nil, HandleMe omits the username field.
	// Used by the PI portal to display "Signed in as <username>".
	GetUsername func(userID string) (string, error)

	// CookieName is the cookie name (e.g. "clustr_session").
	CookieName string

	// Secure sets the Secure flag on the session cookie.
	Secure bool
}

// loginRequest is the JSON body expected by POST /api/v1/auth/login.
// Accepts either username+password (primary) or key (deprecated).
type loginRequest struct {
	// Primary form (ADR-0007)
	Username string `json:"username"`
	Password string `json:"password"`
	// Deprecated legacy form (removed in v1.1)
	Key string `json:"key"`
}

// HandleLogin handles POST /api/v1/auth/login.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request body must be valid JSON",
			"code":  "bad_request",
		})
		return
	}

	// Primary path: username + password.
	if strings.TrimSpace(req.Username) != "" {
		h.handlePasswordLogin(w, r, req.Username, req.Password)
		return
	}

	// Deprecated path: raw API key (removed in v1.1).
	if strings.TrimSpace(req.Key) != "" {
		log.Warn().Str("path", r.URL.Path).Msg("auth/login: key-based login is deprecated; use username+password")
		h.handleKeyLogin(w, r, req.Key)
		return
	}

	writeJSON(w, http.StatusBadRequest, map[string]string{
		"error": "provide username+password or key",
		"code":  "bad_request",
	})
}

func (h *AuthHandler) handlePasswordLogin(w http.ResponseWriter, r *http.Request, username, password string) {
	userID, role, _, err := h.LoginWithPassword(username, password)
	if err != nil {
		switch err.Error() {
		case "invalid":
			// Wrong username or password — generic message prevents user enumeration.
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Invalid username or password",
				"code":  "unauthorized",
			})
		case "disabled":
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Account disabled, contact admin",
				"code":  "account_disabled",
			})
		default:
			// Real DB or infrastructure error — do not mask as 401.
			log.Error().Err(err).Msg("auth: login db error")
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "internal server error",
				"code":  "internal_error",
			})
		}
		return
	}

	token, exp, err := h.SignForUser(userID, role)
	if err != nil {
		log.Error().Err(err).Msg("auth: sign session token failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
			"code":  "internal_error",
		})
		return
	}

	http.SetCookie(w, h.sessionCookie(token, int(time.Until(exp).Seconds())))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"force_password_change": false,
		"role":                  role, // C1-5: exposed for client-side role-based redirect
	})
}

func (h *AuthHandler) handleKeyLogin(w http.ResponseWriter, r *http.Request, rawKey string) {
	keyPrefix, _, ok := h.LoginWithKey(strings.TrimSpace(rawKey))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid API key",
			"code":  "unauthorized",
		})
		return
	}

	token, exp, err := h.SignForKey(keyPrefix)
	if err != nil {
		log.Error().Err(err).Msg("auth: sign session token failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
			"code":  "internal_error",
		})
		return
	}

	http.SetCookie(w, h.sessionCookie(token, int(time.Until(exp).Seconds())))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleLogout handles POST /api/v1/auth/logout — clears the session cookie.
func (h *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, h.sessionCookie("", -1))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleMe handles GET /api/v1/auth/me — returns session info if valid.
func (h *AuthHandler) HandleMe(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(h.CookieName)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "no session",
			"code":  "unauthorized",
		})
		return
	}

	sub, role, exp, needsReissue, newToken, ok := h.Validate(c.Value)
	if !ok {
		http.SetCookie(w, h.sessionCookie("", -1))
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "session expired or invalid",
			"code":  "unauthorized",
		})
		return
	}

	if needsReissue {
		http.SetCookie(w, h.sessionCookie(newToken, int(time.Until(exp).Seconds())))
	}

	resp := map[string]any{
		"sub":        sub,
		"role":       role,
		"expires_at": exp.UTC().Format(time.RFC3339),
	}

	// B1-4: include assigned_groups for operator role scoping.
	// Always present — empty slice when admin/readonly or no groups assigned.
	if h.GetUserGroups != nil {
		groups, err := h.GetUserGroups(sub)
		if err != nil {
			groups = []string{}
		}
		if groups == nil {
			groups = []string{}
		}
		resp["assigned_groups"] = groups
	} else {
		resp["assigned_groups"] = []string{}
	}

	// C5-1-4: include username for PI portal display.
	if h.GetUsername != nil {
		if username, err := h.GetUsername(sub); err == nil {
			resp["username"] = username
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// setPasswordRequest is the body for POST /api/v1/auth/set-password.
type setPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// HandleSetPassword handles POST /api/v1/auth/set-password.
// Requires a valid session (the user is already authenticated).
// On success issues a fresh session cookie and clears the force-change cookie.
func (h *AuthHandler) HandleSetPassword(w http.ResponseWriter, r *http.Request) {
	// Resolve user from session cookie.
	c, err := r.Cookie(h.CookieName)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "no session",
			"code":  "unauthorized",
		})
		return
	}

	sub, role, _, _, _, ok := h.Validate(c.Value)
	if !ok {
		http.SetCookie(w, h.sessionCookie("", -1))
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "session expired or invalid",
			"code":  "unauthorized",
		})
		return
	}

	// Key-based legacy sessions (sub starts with "key:") can't change password this way.
	if strings.HasPrefix(sub, "key:") {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "password change requires a user account session, not API key session",
			"code":  "bad_request",
		})
		return
	}

	var req setPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request body must be valid JSON",
			"code":  "bad_request",
		})
		return
	}

	// Only reject empty passwords — any non-empty value is accepted.
	// No rejection list, no complexity rules. The operator owns their security posture.
	if strings.TrimSpace(req.NewPassword) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "new_password must not be empty",
			"code":  "validation_error",
		})
		return
	}

	token, exp, err := h.SetPassword(sub, req.CurrentPassword, req.NewPassword)
	if err != nil {
		msg := "internal server error"
		code := "internal_error"
		status := http.StatusInternalServerError
		switch err.Error() {
		case "wrong_password":
			msg, code, status = "Current password is incorrect", "wrong_password", http.StatusUnauthorized
		case "user_not_found":
			msg, code, status = "User not found", "not_found", http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": msg, "code": code})
		return
	}

	http.SetCookie(w, h.sessionCookie(token, int(time.Until(exp).Seconds())))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"sub":  sub,
		"role": role,
	})
}

// HandleStatus handles GET /api/v1/auth/status.
// Public (no auth required). Returns {"has_admin": bool} so the web UI can
// detect first-run state and direct the operator to run bootstrap-admin.
func (h *AuthHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if h.HasAdminUser == nil {
		// Safety fallback: if the function is not wired, assume admin exists to
		// avoid accidentally routing a running server into first-run setup UI.
		writeJSON(w, http.StatusOK, map[string]bool{"has_admin": true})
		return
	}
	ok, err := h.HasAdminUser()
	if err != nil {
		log.Error().Err(err).Msg("auth/status: db error")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
			"code":  "internal_error",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"has_admin": ok})
}

// sessionCookie builds a Set-Cookie value for the session.
// maxAge < 0 clears the cookie.
func (h *AuthHandler) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     h.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.Secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}
