package handlers

// auth.go — login / logout / me handlers (ADR-0006 browser session layer).
//
// POST /api/v1/auth/login   — validates admin API key, issues session cookie
// POST /api/v1/auth/logout  — clears session cookie
// GET  /api/v1/auth/me      — returns session info if valid, 401 otherwise

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
	// Login validates a raw API key string. Returns (keyPrefix, scope, ok).
	// keyPrefix is the first 8 chars of the raw key, used for the token kid field.
	Login func(rawKey string) (keyPrefix string, scope string, ok bool)

	// Sign returns a signed session token for the given keyPrefix.
	Sign func(keyPrefix string) (token string, exp time.Time, err error)

	// Validate checks a token and returns (scope, expiry, needsReissue, ok).
	Validate func(token string) (scope string, exp time.Time, needsReissue bool, newToken string, ok bool)

	// CookieName is the cookie name (e.g. "clonr_session").
	CookieName string

	// Secure sets the Secure flag on the session cookie.
	Secure bool
}

// loginRequest is the JSON body expected by POST /api/v1/auth/login.
type loginRequest struct {
	Key string `json:"key"`
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Key) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "request body must be JSON with a non-empty \"key\" field",
			"code":  "bad_request",
		})
		return
	}

	keyPrefix, _, ok := h.Login(strings.TrimSpace(req.Key))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid API key",
			"code":  "unauthorized",
		})
		return
	}

	token, exp, err := h.Sign(keyPrefix)
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

	scope, exp, needsReissue, newToken, ok := h.Validate(c.Value)
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

	writeJSON(w, http.StatusOK, map[string]any{
		"scope":      scope,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
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

