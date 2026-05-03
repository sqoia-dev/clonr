package main

// health_ping_test.go — unit tests for `clustr health --ping` (#219 UX-15)

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestResolvePingTimeout_Flag verifies that an explicit flag value is used.
func TestResolvePingTimeout_Flag(t *testing.T) {
	got := resolvePingTimeout("10s")
	if got != 10*time.Second {
		t.Errorf("expected 10s, got %s", got)
	}
}

// TestResolvePingTimeout_Env verifies that CLUSTR_PING_TIMEOUT is honoured
// when no flag value is present.
func TestResolvePingTimeout_Env(t *testing.T) {
	t.Setenv("CLUSTR_PING_TIMEOUT", "3s")
	got := resolvePingTimeout("")
	if got != 3*time.Second {
		t.Errorf("expected 3s from env, got %s", got)
	}
}

// TestResolvePingTimeout_Default verifies the 5s default applies when neither
// flag nor env is set.
func TestResolvePingTimeout_Default(t *testing.T) {
	t.Setenv("CLUSTR_PING_TIMEOUT", "") // clear any ambient value
	got := resolvePingTimeout("")
	if got != 5*time.Second {
		t.Errorf("expected default 5s, got %s", got)
	}
}

// TestRunServerPing_Success verifies the happy path: server returns 200,
// output line matches "OK clustr-server <host> rt=<N>ms".
func TestRunServerPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.HealthResponse{
				Status:  "ok",
				Version: "dev",
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	// Capture stdout via the healthCmd.
	cmd := newHealthCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	var out strings.Builder
	cmd.SetOut(&out)

	// Route through cobra so persistent flags are wired.
	// We call runServerPing directly to avoid needing stdout capture on cobra.
	if err := runServerPing(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunServerPing_Non200 verifies that a non-200 response causes an error.
func TestRunServerPing_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = srv.URL

	err := runServerPing("")
	if err == nil {
		t.Fatal("expected error for HTTP 503 response")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected error to mention HTTP status, got: %v", err)
	}
}

// TestRunServerPing_ConnRefused verifies that a connection-refused error
// is returned as a non-nil error (not a panic).
func TestRunServerPing_ConnRefused(t *testing.T) {
	// Point at a port that is (almost certainly) not listening.
	origServer := flagServer
	defer func() { flagServer = origServer }()
	flagServer = "http://127.0.0.1:19999"

	err := runServerPing("500ms") // short timeout so the test stays fast
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
}

// TestHealthCmd_PingFlag verifies the --ping flag is wired into the cobra command.
func TestHealthCmd_PingFlag(t *testing.T) {
	cmd := newHealthCmd()
	f := cmd.Flags().Lookup("ping")
	if f == nil {
		t.Fatal("expected --ping flag to be registered")
	}
	if f.DefValue != "false" {
		t.Errorf("expected default false, got %s", f.DefValue)
	}
}

// TestHealthCmd_PingTimeoutFlag verifies the --ping-timeout flag is registered.
func TestHealthCmd_PingTimeoutFlag(t *testing.T) {
	cmd := newHealthCmd()
	f := cmd.Flags().Lookup("ping-timeout")
	if f == nil {
		t.Fatal("expected --ping-timeout flag to be registered")
	}
}

// TestRunServerPing_AuthHeader verifies that a non-empty token is forwarded
// as an Authorization: Bearer header in the ping request.
func TestRunServerPing_AuthHeader(t *testing.T) {
	const wantToken = "test-token-abc"
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.HealthResponse{Status: "ok"})
	}))
	defer srv.Close()

	origServer := flagServer
	origToken := flagToken
	defer func() {
		flagServer = origServer
		flagToken = origToken
	}()
	flagServer = srv.URL
	flagToken = wantToken

	if err := runServerPing(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuth != "Bearer "+wantToken {
		t.Errorf("expected Authorization header %q, got %q", "Bearer "+wantToken, gotAuth)
	}
}
