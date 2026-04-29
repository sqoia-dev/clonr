package handlers

// auth_test.go — anti-regression tests for auth handler.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testAuthHandler returns an AuthHandler wired for unit tests.
func testAuthHandler() *AuthHandler {
	return &AuthHandler{
		CookieName: "clustr_session",
		Secure:     false,
		Validate: func(token string) (sub, role string, exp time.Time, needsReissue bool, newToken string, ok bool) {
			if token == "valid-token" {
				return "user-123", "admin", time.Now().Add(time.Hour), false, "", true
			}
			return "", "", time.Time{}, false, "", false
		},
		SetPassword: func(userID, currentPassword, newPassword string) (token string, exp time.Time, err error) {
			return "new-token", time.Now().Add(time.Hour), nil
		},
	}
}

// TestHandleSetPassword_AcceptsDefaultPassword verifies that "clustr" is a valid
// new password. No rejection list. Operator owns their security posture.
func TestHandleSetPassword_AcceptsDefaultPassword(t *testing.T) {
	h := testAuthHandler()

	body, _ := json.Marshal(map[string]string{
		"current_password": "clustr",
		"new_password":     "clustr",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/set-password", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "valid-token"})
	w := httptest.NewRecorder()

	h.HandleSetPassword(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 when setting password to 'clustr', got %d — rejection list must be removed", resp.StatusCode)
	}
}

// TestHandleSetPassword_AcceptsAnyNonEmptyPassword confirms any non-empty password is allowed.
func TestHandleSetPassword_AcceptsAnyNonEmptyPassword(t *testing.T) {
	h := testAuthHandler()

	passwords := []string{"clustr", "simple", "RealPassword1!", "x", "a b c"}
	for _, pw := range passwords {
		body, _ := json.Marshal(map[string]string{
			"current_password": "clustr",
			"new_password":     pw,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/set-password", bytes.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "valid-token"})
		w := httptest.NewRecorder()

		h.HandleSetPassword(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("password %q: expected 200, got %d", pw, resp.StatusCode)
		}
	}
}

// TestHandleSetPassword_RejectsEmptyPassword confirms empty new_password returns 400.
func TestHandleSetPassword_RejectsEmptyPassword(t *testing.T) {
	h := testAuthHandler()

	body, _ := json.Marshal(map[string]string{
		"current_password": "clustr",
		"new_password":     "",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/set-password", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "valid-token"})
	w := httptest.NewRecorder()

	h.HandleSetPassword(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty new_password, got %d", resp.StatusCode)
	}
}

// TestHandleLogin_ForcePasswordChangeAlwaysFalse verifies that the login response
// always returns force_password_change:false regardless of the user's DB state.
func TestHandleLogin_ForcePasswordChangeAlwaysFalse(t *testing.T) {
	h := &AuthHandler{
		CookieName: "clustr_session",
		Secure:     false,
		LoginWithPassword: func(username, password string) (userID, role string, mustChange bool, err error) {
			// Simulate a user that has must_change_password=true in the DB.
			return "user-123", "admin", true, nil
		},
		SignForUser: func(userID, role string) (token string, exp time.Time, err error) {
			return "tok", time.Now().Add(time.Hour), nil
		},
	}

	body, _ := json.Marshal(map[string]string{
		"username": "clustr",
		"password": "clustr",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// force_password_change must be false (or absent) — never true.
	if fpc, ok := result["force_password_change"]; ok {
		if fpc == true {
			t.Error("ANTI-REGRESSION: force_password_change must never be true in login response")
		}
	}

	// No clustr_force_password_change cookie should be set.
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "clustr_force_password_change" {
			t.Error("ANTI-REGRESSION: clustr_force_password_change cookie must not be set on login")
		}
	}
}
