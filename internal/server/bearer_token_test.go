package server

// bearer_token_test.go — SEC-1 unit tests for extractBearerToken and wsTokenLift.
//
// These tests live in package server (internal) so they can directly exercise the
// unexported extractBearerToken function and the wsTokenLift middleware.
//
// Integration-level tests (HTTP endpoint rejection) live in bearer_token_int_test.go
// in package server_test so they can use the exported server constructor.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─── extractBearerToken unit tests ───────────────────────────────────────────

func TestExtractBearerToken_HeaderPresent(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer abc123")

	got := extractBearerToken(r)
	if got != "abc123" {
		t.Errorf("extractBearerToken: got %q, want %q", got, "abc123")
	}
}

func TestExtractBearerToken_CaseInsensitiveScheme(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "BEARER mytoken")

	got := extractBearerToken(r)
	if got != "mytoken" {
		t.Errorf("extractBearerToken: got %q, want %q", got, "mytoken")
	}
}

func TestExtractBearerToken_MissingHeader_ReturnsEmpty(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)

	got := extractBearerToken(r)
	if got != "" {
		t.Errorf("extractBearerToken: missing header should return empty, got %q", got)
	}
}

// SEC-1 regression guard: query-only ?token= must be rejected by the strict extractor.
// If this test fails, the credential-leakage path is reinstated.
func TestExtractBearerToken_QueryParamOnly_ReturnsEmpty(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/?token=secrettoken", nil)
	// Explicitly no Authorization header.

	got := extractBearerToken(r)
	if got != "" {
		t.Errorf("SEC-1 REGRESSION: extractBearerToken accepted ?token= without Authorization header; got %q", got)
	}
}

func TestExtractBearerToken_BothHeaderAndQuery_HeaderWins(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/?token=querytoken", nil)
	r.Header.Set("Authorization", "Bearer headertoken")

	got := extractBearerToken(r)
	if got != "headertoken" {
		t.Errorf("extractBearerToken: header should win over query param, got %q", got)
	}
}

// ─── wsTokenLift unit tests ───────────────────────────────────────────────────

func TestWsTokenLift_HoistsQueryTokenIntoHeader(t *testing.T) {
	var capturedAuth string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	handler := wsTokenLift(inner)
	r, _ := http.NewRequest(http.MethodGet, "/ws?token=mysecret", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	want := "Bearer mysecret"
	if capturedAuth != want {
		t.Errorf("wsTokenLift: downstream saw Authorization=%q, want %q", capturedAuth, want)
	}
}

func TestWsTokenLift_ExistingHeader_NotOverwritten(t *testing.T) {
	var capturedAuth string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	handler := wsTokenLift(inner)
	r, _ := http.NewRequest(http.MethodGet, "/ws?token=querytoken", nil)
	r.Header.Set("Authorization", "Bearer headertoken")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if capturedAuth != "Bearer headertoken" {
		t.Errorf("wsTokenLift: existing Authorization header was overwritten; got %q", capturedAuth)
	}
}

func TestWsTokenLift_NoTokenNoHeader_PassesThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := wsTokenLift(inner)
	r, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if !called {
		t.Error("wsTokenLift: inner handler was not called when no token and no header present")
	}
}

// TestWsTokenLift_DoesNotMutateOriginalRequest verifies that wsTokenLift uses
// r.Clone so the original *http.Request is not mutated.
func TestWsTokenLift_DoesNotMutateOriginalRequest(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := wsTokenLift(inner)
	r, _ := http.NewRequest(http.MethodGet, "/ws?token=tok", nil)
	origAuth := r.Header.Get("Authorization") // should be ""
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if r.Header.Get("Authorization") != origAuth {
		t.Error("wsTokenLift: original request header was mutated; Clone not used")
	}
}
