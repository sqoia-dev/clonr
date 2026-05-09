package main

// ipmi_admin_test.go — unit tests for the Sprint 34 `clustr ipmi node ...`
// CLI. We exercise URL composition + response decoding via a fake httptest
// server so we don't depend on an external clustr-serverd.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withFakeServer spins up an httptest server and rebinds the package-level
// flagServer/flagToken so every call resolves to it.  Returns a capture
// struct with each (method, path) tuple.
func withFakeServer(t *testing.T, handler http.Handler) (capture *requestCapture, restore func()) {
	t.Helper()
	c := &requestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls = append(c.calls, request{Method: r.Method, Path: r.URL.Path})
		handler.ServeHTTP(w, r)
	}))
	prevServer := flagServer
	prevToken := flagToken
	flagServer = srv.URL
	flagToken = "fake-token"
	return c, func() {
		srv.Close()
		flagServer = prevServer
		flagToken = prevToken
	}
}

type requestCapture struct {
	calls []request
}
type request struct{ Method, Path string }

// ─── Power ────────────────────────────────────────────────────────────────────

func TestRunIPMINodePower_BuildsCorrectURL(t *testing.T) {
	c, restore := withFakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ipmiPowerActionResponse{
			NodeID: "compute01", Action: "status", Output: "10.0.0.5: on", OK: true,
		})
	}))
	defer restore()

	if err := runIPMINodePower("compute01", "status"); err != nil {
		t.Fatalf("runIPMINodePower: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(c.calls))
	}
	got := c.calls[0]
	if got.Method != "POST" {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.Path != "/api/v1/nodes/compute01/ipmi/power/status" {
		t.Errorf("path = %q, want /api/v1/nodes/compute01/ipmi/power/status", got.Path)
	}
}

func TestRunIPMINodePower_RejectsInvalidAction(t *testing.T) {
	if err := runIPMINodePower("compute01", "detonate"); err == nil {
		t.Error("expected error for invalid action")
	} else if !strings.Contains(err.Error(), "invalid power action") {
		t.Errorf("error should mention invalid action: %v", err)
	}
}

// ─── SEL ──────────────────────────────────────────────────────────────────────

func TestRunIPMINodeSEL_List(t *testing.T) {
	c, restore := withFakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ipmiSELResponse{NodeID: "compute01"})
	}))
	defer restore()

	if err := runIPMINodeSEL("compute01", []string{"list"}); err != nil {
		t.Fatalf("sel list: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(c.calls))
	}
	if c.calls[0].Method != "GET" || c.calls[0].Path != "/api/v1/nodes/compute01/ipmi/sel" {
		t.Errorf("call mismatch: %+v", c.calls[0])
	}
}

func TestRunIPMINodeSEL_ClearWithYes(t *testing.T) {
	c, restore := withFakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cleared":true}`))
	}))
	defer restore()

	if err := runIPMINodeSEL("compute01", []string{"clear", "-y"}); err != nil {
		t.Fatalf("sel clear: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(c.calls))
	}
	if c.calls[0].Method != "DELETE" || c.calls[0].Path != "/api/v1/nodes/compute01/ipmi/sel" {
		t.Errorf("call mismatch: %+v", c.calls[0])
	}
}

func TestRunIPMINodeSEL_BadOp(t *testing.T) {
	if err := runIPMINodeSEL("compute01", []string{"truncate"}); err == nil {
		t.Error("expected error for unknown op")
	}
}

// ─── Sensors ──────────────────────────────────────────────────────────────────

func TestRunIPMINodeSensors_BuildsCorrectURL(t *testing.T) {
	c, restore := withFakeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ipmiSensorsResponse{
			NodeID:  "compute01",
			Sensors: []ipmiSensorEntry{{Name: "CPU0_Temp", Value: "42", Units: "C", Status: "ok"}},
		})
	}))
	defer restore()

	if err := runIPMINodeSensors("compute01"); err != nil {
		t.Fatalf("sensors: %v", err)
	}
	if len(c.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(c.calls))
	}
	if c.calls[0].Method != "GET" || c.calls[0].Path != "/api/v1/nodes/compute01/ipmi/sensors" {
		t.Errorf("call mismatch: %+v", c.calls[0])
	}
}

// ─── Argv parsing ─────────────────────────────────────────────────────────────

func TestNewIPMINodeCmd_RequiresVerb(t *testing.T) {
	cmd := newIPMINodeCmd()
	cmd.SetArgs([]string{"compute01"}) // no verb
	if err := cmd.Execute(); err == nil {
		t.Error("expected error when no verb supplied")
	}
}
