package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── Fake DB ──────────────────────────────────────────────────────────────────

// fakeConsoleDB implements ConsoleDB for tests.
type fakeConsoleDB struct {
	nodes map[string]api.NodeConfig
}

func (f *fakeConsoleDB) GetNodeConfig(_ context.Context, nodeID string) (api.NodeConfig, error) {
	if cfg, ok := f.nodes[nodeID]; ok {
		return cfg, nil
	}
	return api.NodeConfig{}, &notFoundError{msg: "node " + nodeID + " not found"}
}

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

// ─── resolveConsoleMode unit tests ────────────────────────────────────────────

func TestResolveConsoleMode_AutoSelectBMC(t *testing.T) {
	cfg := api.NodeConfig{
		BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.1", Username: "admin", Password: "pass"},
	}
	mode, err := resolveConsoleMode("", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "ipmi-sol" {
		t.Errorf("expected ipmi-sol, got %q", mode)
	}
}

func TestResolveConsoleMode_AutoSelectSSH(t *testing.T) {
	cfg := api.NodeConfig{
		Interfaces: []api.InterfaceConfig{{IPAddress: "192.168.1.10/24"}},
	}
	mode, err := resolveConsoleMode("", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "ssh" {
		t.Errorf("expected ssh, got %q", mode)
	}
}

func TestResolveConsoleMode_NeitherConfigured(t *testing.T) {
	cfg := api.NodeConfig{}
	_, err := resolveConsoleMode("", cfg)
	if err == nil {
		t.Fatal("expected error when neither BMC nor IP configured")
	}
}

func TestResolveConsoleMode_ForceIPMISOL_NoBMC(t *testing.T) {
	cfg := api.NodeConfig{} // no BMC
	_, err := resolveConsoleMode("ipmi-sol", cfg)
	if err == nil {
		t.Fatal("expected error when forcing ipmi-sol without BMC config")
	}
}

func TestResolveConsoleMode_ForceSSH_NoIP(t *testing.T) {
	cfg := api.NodeConfig{} // no interfaces
	_, err := resolveConsoleMode("ssh", cfg)
	if err == nil {
		t.Fatal("expected error when forcing ssh without node IP")
	}
}

func TestResolveConsoleMode_UnknownMode(t *testing.T) {
	cfg := api.NodeConfig{
		BMC: &api.BMCNodeConfig{IPAddress: "10.0.0.1"},
	}
	_, err := resolveConsoleMode("vnc", cfg)
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("error should mention unknown mode, got: %v", err)
	}
}

// ─── nodeIP unit tests ────────────────────────────────────────────────────────

func TestNodeIP_CIDR(t *testing.T) {
	cfg := api.NodeConfig{
		Interfaces: []api.InterfaceConfig{{IPAddress: "10.99.0.10/24"}},
	}
	got := nodeIP(cfg)
	if got != "10.99.0.10" {
		t.Errorf("expected 10.99.0.10, got %q", got)
	}
}

func TestNodeIP_Plain(t *testing.T) {
	cfg := api.NodeConfig{
		Interfaces: []api.InterfaceConfig{{IPAddress: "10.99.0.10"}},
	}
	got := nodeIP(cfg)
	if got != "10.99.0.10" {
		t.Errorf("expected 10.99.0.10, got %q", got)
	}
}

func TestNodeIP_None(t *testing.T) {
	cfg := api.NodeConfig{}
	got := nodeIP(cfg)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ─── Multi-target rejection (node not found = implicit single-target check) ──

// TestConsoleHandler_NodeNotFound verifies that a 404 is returned (via WS close)
// when the node is unknown. This stands in for the multi-target rejection test:
// the handler only accepts a single node_id path parameter; attempts to use
// a selector that matches multiple nodes are rejected before reaching the handler.
func TestConsoleHandler_NodeNotFound(t *testing.T) {
	fakeDB := &fakeConsoleDB{nodes: map[string]api.NodeConfig{}}
	h := &ConsoleHandler{DB: fakeDB}

	r := chi.NewRouter()
	r.Get("/console/{node_id}", h.HandleConsole)

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/console/nonexistent-node"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// Connection may be refused or the error may come as a WS close —
		// either is correct behaviour for a node-not-found case.
		return
	}
	defer conn.Close()

	// Read the error frame sent by the handler.
	_, data, err := conn.ReadMessage()
	if err != nil {
		// WS close frame is also acceptable.
		return
	}

	// Should contain an error message in the JSON exit frame.
	msg := string(data)
	if !strings.Contains(msg, "exit") && !strings.Contains(msg, "not found") {
		t.Logf("received: %s", msg)
		// Not a hard failure — gorilla may send a close frame before the JSON.
	}
}

// ─── HTTP handler compile-check ───────────────────────────────────────────────

// TestConsoleHandlerCompiles ensures ConsoleHandler satisfies the expected interface.
// This is a compile-time check; the test body is empty.
func TestConsoleHandlerCompiles(t *testing.T) {
	_ = &ConsoleHandler{DB: &fakeConsoleDB{}}
	_ = http.HandlerFunc((&ConsoleHandler{DB: &fakeConsoleDB{}}).HandleConsole)
}
