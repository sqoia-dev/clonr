package handlers

// auth_test.go — tests for auth handler security invariants.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testAuthHandler returns an AuthHandler wired for unit tests.
// The SetPassword function always succeeds; we test the validation
// layer that runs before it is called.
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

// TestHandleSetPassword_RejectsDefaultPassword_DEF3 verifies that the literal
// string "clustr" is rejected as a new password with a clear error message.
// This is the DEF-3 invariant: the default password must not be allowed as a
// "new" password when transitioning out of the force-change flow.
func TestHandleSetPassword_RejectsDefaultPassword_DEF3(t *testing.T) {
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when setting password to 'clustr', got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result["code"] != "validation_error" {
		t.Errorf("expected code=validation_error, got %q", result["code"])
	}

	// The error message must be explicit about what is disallowed.
	if !strings.Contains(result["error"], "clustr") {
		t.Errorf("error message should mention 'clustr', got: %q", result["error"])
	}
}

// TestHandleSetPassword_AcceptsStrongPassword confirms the DEF-3 check does
// not interfere with legitimate password changes.
func TestHandleSetPassword_AcceptsStrongPassword(t *testing.T) {
	h := testAuthHandler()

	body, _ := json.Marshal(map[string]string{
		"current_password": "clustr",
		"new_password":     "RealPassword1!",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/set-password", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "clustr_session", Value: "valid-token"})
	w := httptest.NewRecorder()

	h.HandleSetPassword(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for strong password, got %d", resp.StatusCode)
	}
}
