package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// fakeLDAPDB is a minimal stub of ClientdDBIface for the applyLDAPHealth
// unit tests. Only LDAPNodeIsConfigured and RecordNodeLDAPReady are exercised;
// the rest of the iface returns zero values so the type satisfies the
// interface but never fires from this test.
type fakeLDAPDB struct {
	configured     bool
	configuredErr  error
	recordedReady  *bool
	recordedDetail string
	recordCount    int
	recordErr      error
}

func (f *fakeLDAPDB) UpsertHeartbeat(context.Context, string, *db.HeartbeatRow) error { return nil }
func (f *fakeLDAPDB) GetHeartbeat(context.Context, string) (*db.HeartbeatRow, error) {
	return nil, nil
}
func (f *fakeLDAPDB) UpdateLastSeen(context.Context, string) error         { return nil }
func (f *fakeLDAPDB) InsertLogBatch(context.Context, []api.LogEntry) error { return nil }
func (f *fakeLDAPDB) GetNodeConfig(context.Context, string) (api.NodeConfig, error) {
	return api.NodeConfig{}, nil
}
func (f *fakeLDAPDB) InsertStatsBatch(context.Context, []db.NodeStatRow) error { return nil }

func (f *fakeLDAPDB) LDAPNodeIsConfigured(_ context.Context, _ string) (bool, error) {
	return f.configured, f.configuredErr
}

func (f *fakeLDAPDB) RecordNodeLDAPReady(_ context.Context, _ string, ready bool, detail string) error {
	f.recordCount++
	f.recordedReady = &ready
	f.recordedDetail = detail
	return f.recordErr
}

// TestApplyLDAPHealth_NodeConfigured covers the happy path where the node was
// deployed with LDAP and the probe says sssd is online — we expect ready=true
// to be written through.
func TestApplyLDAPHealth_NodeConfigured(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Configured: true,
		Active:     true,
		Connected:  true,
		Domain:     "cluster.local",
		Detail:     "sssd online (cluster.local)",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true on successful write")
	}
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady call, got %d", d.recordCount)
	}
	if d.recordedReady == nil || !*d.recordedReady {
		t.Fatalf("expected ready=true, got %v", d.recordedReady)
	}
	if d.recordedDetail != "sssd online (cluster.local)" {
		t.Fatalf("unexpected detail: %q", d.recordedDetail)
	}
}

// TestApplyLDAPHealth_NodeConfiguredButOffline covers the case where the node
// is LDAP-configured but the probe shows sssd is broken — we expect
// ready=false to be written through with the probe's Detail.
func TestApplyLDAPHealth_NodeConfiguredButOffline(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: false,
		Detail:    "sssd active but domain offline: …",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true (write succeeded even though probe says not-ready)")
	}
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady call, got %d", d.recordCount)
	}
	if d.recordedReady == nil || *d.recordedReady {
		t.Fatalf("expected ready=false, got %v", d.recordedReady)
	}
}

// TestApplyLDAPHealth_NodeNotConfigured covers a node deployed without LDAP —
// we MUST NOT write to node_configs.ldap_ready (the column stays NULL so
// pkg/api.NodeConfig.State() does not flag the node as "LDAP failed").
func TestApplyLDAPHealth_NodeNotConfigured(t *testing.T) {
	d := &fakeLDAPDB{configured: false}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false for non-LDAP node (intentional skip)")
	}
	if d.recordCount != 0 {
		t.Fatalf("expected 0 RecordNodeLDAPReady calls for non-LDAP node, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_NilHealth — defensive: nil snapshot is a no-op.
func TestApplyLDAPHealth_NilHealth(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false for nil snapshot")
	}
	if d.recordCount != 0 {
		t.Fatalf("expected 0 RecordNodeLDAPReady calls for nil snapshot, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_LookupError — when LDAPNodeIsConfigured fails we must
// not write a misleading row; we log and skip. applied=false, err=nil because
// the lookup error is non-fatal — the probe will retry on the next heartbeat.
func TestApplyLDAPHealth_LookupError(t *testing.T) {
	d := &fakeLDAPDB{configuredErr: errors.New("db down")}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
	})
	if err != nil {
		t.Fatalf("expected nil err on lookup failure (non-fatal), got %v", err)
	}
	if applied {
		t.Fatalf("expected applied=false when LDAPNodeIsConfigured errors")
	}
	if d.recordCount != 0 {
		t.Fatalf("expected no record when LDAPNodeIsConfigured errors, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_RecordError — when RecordNodeLDAPReady fails (transient
// DB write error), applyLDAPHealth must return applied=false AND err!=nil so
// callers (specifically VerifyLDAPOnNode) can surface a 5xx instead of
// reporting applied=true with a 200. Codex P2 fix on PR #5: previously the
// error was logged and dropped, and applied was inferred from
// LDAPNodeIsConfigured, which silently misled operators.
func TestApplyLDAPHealth_RecordError(t *testing.T) {
	writeErr := errors.New("disk full")
	d := &fakeLDAPDB{configured: true, recordErr: writeErr}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
	})
	if applied {
		t.Fatalf("expected applied=false when RecordNodeLDAPReady returns an error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected err to wrap RecordNodeLDAPReady's error, got %v", err)
	}
	if d.recordCount != 1 {
		t.Fatalf("expected exactly 1 RecordNodeLDAPReady attempt, got %d", d.recordCount)
	}
}

// TestApplyLDAPHealth_DefaultDetails — when the probe omits Detail (defensive
// path) we synthesize a sane string so the UI never shows an empty cell.
func TestApplyLDAPHealth_DefaultDetails(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	applied, err := applyLDAPHealth(context.Background(), d, "node-1", &clientd.LDAPHealthStatus{
		Active:    true,
		Connected: true,
		// Detail intentionally empty
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !applied {
		t.Fatalf("expected applied=true on successful write")
	}
	if d.recordedDetail == "" {
		t.Fatalf("expected non-empty default detail")
	}
}

// ─── VerifyLDAPOnNode HTTP tests ─────────────────────────────────────────────

// fakeLDAPHub is a minimal ClientdHubIface stub that lets us drive
// VerifyLDAPOnNode end-to-end: IsConnected returns true, Send is a no-op,
// RegisterLDAPHealth hands back a buffered channel that we pre-load with the
// payload we want the handler to "receive" from the node. All other registry
// methods return zero values so the type satisfies the interface.
type fakeLDAPHub struct {
	connected  bool
	resultCh   chan clientd.LDAPHealthResultPayload
	preloaded  clientd.LDAPHealthResultPayload
	hasPayload bool
}

func (h *fakeLDAPHub) RegisterConn(string, *websocket.Conn, chan []byte, context.CancelFunc) {
}
func (h *fakeLDAPHub) Unregister(string)                            {}
func (h *fakeLDAPHub) ConnectedNodes() []string                     { return nil }
func (h *fakeLDAPHub) IsConnected(string) bool                      { return h.connected }
func (h *fakeLDAPHub) AppendJournalEntries(string, []api.LogEntry)  {}
func (h *fakeLDAPHub) Send(string, clientd.ServerMessage) error     { return nil }
func (h *fakeLDAPHub) RegisterAck(string) <-chan clientd.AckPayload { return nil }
func (h *fakeLDAPHub) UnregisterAck(string)                         {}
func (h *fakeLDAPHub) DeliverAck(string, clientd.AckPayload) bool   { return false }
func (h *fakeLDAPHub) RegisterExec(string) <-chan clientd.ExecResultPayload {
	return nil
}
func (h *fakeLDAPHub) UnregisterExec(string)                                    {}
func (h *fakeLDAPHub) DeliverExecResult(string, clientd.ExecResultPayload) bool { return false }
func (h *fakeLDAPHub) RegisterOperatorExec(string) <-chan clientd.OperatorExecResultPayload {
	return nil
}
func (h *fakeLDAPHub) UnregisterOperatorExec(string) {}
func (h *fakeLDAPHub) DeliverOperatorExecResult(string, clientd.OperatorExecResultPayload) bool {
	return false
}
func (h *fakeLDAPHub) RegisterDiskCapture(string) <-chan clientd.DiskCaptureResultPayload {
	return nil
}
func (h *fakeLDAPHub) UnregisterDiskCapture(string) {}
func (h *fakeLDAPHub) DeliverDiskCaptureResult(string, clientd.DiskCaptureResultPayload) bool {
	return false
}
func (h *fakeLDAPHub) RegisterBiosRead(string) <-chan clientd.BiosReadResultPayload {
	return nil
}
func (h *fakeLDAPHub) UnregisterBiosRead(string) {}
func (h *fakeLDAPHub) DeliverBiosReadResult(string, clientd.BiosReadResultPayload) bool {
	return false
}
func (h *fakeLDAPHub) RegisterBiosApply(string) <-chan clientd.BiosApplyResultPayload {
	return nil
}
func (h *fakeLDAPHub) UnregisterBiosApply(string) {}
func (h *fakeLDAPHub) DeliverBiosApplyResult(string, clientd.BiosApplyResultPayload) bool {
	return false
}
func (h *fakeLDAPHub) RegisterLDAPHealth(_ string) <-chan clientd.LDAPHealthResultPayload {
	if h.resultCh == nil {
		h.resultCh = make(chan clientd.LDAPHealthResultPayload, 1)
	}
	if h.hasPayload {
		h.resultCh <- h.preloaded
	}
	return h.resultCh
}
func (h *fakeLDAPHub) UnregisterLDAPHealth(string) {}
func (h *fakeLDAPHub) DeliverLDAPHealthResult(string, clientd.LDAPHealthResultPayload) bool {
	return false
}

func newVerifyLDAPRouter(h *ClientdHandler) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/v1/nodes/{id}/verify-ldap", h.VerifyLDAPOnNode)
	return r
}

// TestVerifyLDAPOnNode_RecordError pins the Codex P2 contract on PR #5:
// when RecordNodeLDAPReady fails, VerifyLDAPOnNode MUST surface a 5xx and
// applied=false in the response body. Previously the handler set applied=true
// purely from LDAPNodeIsConfigured and returned 200, misleading operators
// into thinking the row was rewritten when the write had actually failed.
func TestVerifyLDAPOnNode_RecordError(t *testing.T) {
	d := &fakeLDAPDB{configured: true, recordErr: errors.New("disk full")}
	hub := &fakeLDAPHub{
		connected:  true,
		hasPayload: true,
		preloaded: clientd.LDAPHealthResultPayload{
			Health: clientd.LDAPHealthStatus{
				Configured: true,
				Active:     true,
				Connected:  true,
				Domain:     "cluster.local",
				Detail:     "sssd online",
			},
		},
	}
	h := &ClientdHandler{DB: d, Hub: hub}
	router := newVerifyLDAPRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-1/verify-ldap", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when RecordNodeLDAPReady fails, got %d: %s",
			rr.Code, rr.Body.String())
	}
	var resp struct {
		VerifyLDAPResponse
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v: %s", err, rr.Body.String())
	}
	if resp.Applied {
		t.Fatalf("expected applied=false when RecordNodeLDAPReady errors, got applied=true (Codex P2 regression)")
	}
	if resp.Code != "ldap_ready_write_failed" {
		t.Fatalf("expected code=ldap_ready_write_failed, got %q", resp.Code)
	}
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady attempt, got %d", d.recordCount)
	}
}

// TestVerifyLDAPOnNode_Success verifies the happy path: when the probe
// succeeds AND RecordNodeLDAPReady persists, the response is 200 with
// applied=true.
func TestVerifyLDAPOnNode_Success(t *testing.T) {
	d := &fakeLDAPDB{configured: true}
	hub := &fakeLDAPHub{
		connected:  true,
		hasPayload: true,
		preloaded: clientd.LDAPHealthResultPayload{
			Health: clientd.LDAPHealthStatus{
				Configured: true,
				Active:     true,
				Connected:  true,
				Domain:     "cluster.local",
				Detail:     "sssd online",
			},
		},
	}
	h := &ClientdHandler{DB: d, Hub: hub}
	router := newVerifyLDAPRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-1/verify-ldap", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp VerifyLDAPResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v: %s", err, rr.Body.String())
	}
	if !resp.Applied {
		t.Fatalf("expected applied=true on successful write, got false")
	}
	if !resp.Ready {
		t.Fatalf("expected ready=true (Active && Connected)")
	}
	if d.recordCount != 1 {
		t.Fatalf("expected 1 RecordNodeLDAPReady call, got %d", d.recordCount)
	}
}

// TestVerifyLDAPOnNode_NotConfigured covers the intentional skip: when the
// node has no LDAP config, applied=false but the response is 200 (no error,
// just nothing to persist).
func TestVerifyLDAPOnNode_NotConfigured(t *testing.T) {
	d := &fakeLDAPDB{configured: false}
	hub := &fakeLDAPHub{
		connected:  true,
		hasPayload: true,
		preloaded: clientd.LDAPHealthResultPayload{
			Health: clientd.LDAPHealthStatus{
				Configured: false,
				Active:     false,
				Connected:  false,
			},
		},
	}
	h := &ClientdHandler{DB: d, Hub: hub}
	router := newVerifyLDAPRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-1/verify-ldap", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (intentional skip is not an error), got %d: %s",
			rr.Code, rr.Body.String())
	}
	var resp VerifyLDAPResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v: %s", err, rr.Body.String())
	}
	if resp.Applied {
		t.Fatalf("expected applied=false for non-LDAP node (intentional skip)")
	}
	if d.recordCount != 0 {
		t.Fatalf("expected 0 RecordNodeLDAPReady calls for non-LDAP node, got %d", d.recordCount)
	}
}
