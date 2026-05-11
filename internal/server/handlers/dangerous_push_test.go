package handlers

// dangerous_push_test.go — Sprint 41 Day 3
//
// Tests for the typed-confirm-string dangerous-push gate.
//
// Coverage:
//   - Happy path: stage → confirm match → push delivered
//   - Confirm-string mismatch → 400 + counter increments
//   - 3-strikes lockout → 410 Gone
//   - Expired row → 410 Gone
//   - Non-dangerous plugin rejected at stage → 400
//   - Unknown plugin at stage → 404
//   - Regular config-push handler rejects dangerous plugins when gate enabled
//   - Gate disabled: dangerous plugins are not blocked by regular config-push
//   - SSSD plugin Dangerous flag is true (unit check)

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sqoia-dev/clustr/internal/auth"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/config/plugins"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fakeHubDangerous ─────────────────────────────────────────────────────────

// fakeHubDangerous implements ClientdHubIface for dangerous-push handler tests.
// Only Send is exercised; all other methods are no-ops.
// Set connected=true to make IsConnected return true (e.g. for confirm tests).
type fakeHubDangerous struct {
	sendFail  bool
	sentCount int
	connected bool // controls IsConnected return value; default false → node offline
}

func (h *fakeHubDangerous) RegisterConn(_ string, _ *websocket.Conn, _ chan []byte, _ context.CancelFunc) {
}
func (h *fakeHubDangerous) Unregister(_ string)                                      {}
func (h *fakeHubDangerous) ConnectedNodes() []string                                 { return nil }
func (h *fakeHubDangerous) IsConnected(_ string) bool                               { return h.connected }
func (h *fakeHubDangerous) AppendJournalEntries(_ string, _ []api.LogEntry)         {}
func (h *fakeHubDangerous) RegisterAck(_ string) <-chan clientd.AckPayload           { return nil }
func (h *fakeHubDangerous) UnregisterAck(_ string)                                   {}
func (h *fakeHubDangerous) DeliverAck(_ string, _ clientd.AckPayload) bool          { return false }
func (h *fakeHubDangerous) RegisterExec(_ string) <-chan clientd.ExecResultPayload   { return nil }
func (h *fakeHubDangerous) UnregisterExec(_ string)                                  {}
func (h *fakeHubDangerous) DeliverExecResult(_ string, _ clientd.ExecResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) RegisterOperatorExec(_ string) <-chan clientd.OperatorExecResultPayload {
	return nil
}
func (h *fakeHubDangerous) UnregisterOperatorExec(_ string) {}
func (h *fakeHubDangerous) DeliverOperatorExecResult(_ string, _ clientd.OperatorExecResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) RegisterDiskCapture(_ string) <-chan clientd.DiskCaptureResultPayload {
	return nil
}
func (h *fakeHubDangerous) UnregisterDiskCapture(_ string) {}
func (h *fakeHubDangerous) DeliverDiskCaptureResult(_ string, _ clientd.DiskCaptureResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) RegisterBiosRead(_ string) <-chan clientd.BiosReadResultPayload { return nil }
func (h *fakeHubDangerous) UnregisterBiosRead(_ string)                                     {}
func (h *fakeHubDangerous) DeliverBiosReadResult(_ string, _ clientd.BiosReadResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) RegisterBiosApply(_ string) <-chan clientd.BiosApplyResultPayload {
	return nil
}
func (h *fakeHubDangerous) UnregisterBiosApply(_ string) {}
func (h *fakeHubDangerous) DeliverBiosApplyResult(_ string, _ clientd.BiosApplyResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) RegisterLDAPHealth(_ string) <-chan clientd.LDAPHealthResultPayload {
	return nil
}
func (h *fakeHubDangerous) UnregisterLDAPHealth(_ string) {}
func (h *fakeHubDangerous) DeliverLDAPHealthResult(_ string, _ clientd.LDAPHealthResultPayload) bool {
	return false
}
func (h *fakeHubDangerous) Send(_ string, _ clientd.ServerMessage) error {
	if h.sendFail {
		return errSendFailed
	}
	h.sentCount++
	return nil
}

var errSendFailed = &testSendError{}

type testSendError struct{}

func (e *testSendError) Error() string { return "test: send failed" }

// ─── fakeDangerousDB ─────────────────────────────────────────────────────────

type fakeDangerousDB struct {
	rows     map[string]*db.PendingDangerousPush
	attempts map[string]int
}

func newFakeDangerousDB() *fakeDangerousDB {
	return &fakeDangerousDB{
		rows:     make(map[string]*db.PendingDangerousPush),
		attempts: make(map[string]int),
	}
}

func (f *fakeDangerousDB) InsertPendingDangerousPush(_ context.Context, p db.PendingDangerousPush) error {
	c := p
	f.rows[p.ID] = &c
	return nil
}

func (f *fakeDangerousDB) GetPendingDangerousPush(_ context.Context, id string) (*db.PendingDangerousPush, error) {
	row, ok := f.rows[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	c := *row
	c.AttemptCount = f.attempts[id]
	return &c, nil
}

func (f *fakeDangerousDB) IncrementDangerousPushAttempts(_ context.Context, id string, max int) (int, error) {
	f.attempts[id]++
	n := f.attempts[id]
	if n >= max {
		if row, ok := f.rows[id]; ok {
			row.Consumed = true
		}
	}
	return n, nil
}

func (f *fakeDangerousDB) ConsumePendingDangerousPush(_ context.Context, id string) error {
	if row, ok := f.rows[id]; ok {
		row.Consumed = true
	}
	return nil
}

func (f *fakeDangerousDB) InsertPluginBackup(_ context.Context, _ db.PluginBackup) error {
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func buildDangerousHandler(fdb *fakeDangerousDB, hub *fakeHubDangerous, clusterName string) *DangerousPushHandler {
	return &DangerousPushHandler{
		DB:          fdb,
		Hub:         hub,
		Audit:       nil,
		GetActorInfo: func(r *http.Request) (string, string) { return "test-actor", "user:test" },
		ClusterName: clusterName,
		PluginMetadata: func(name string) (config.PluginMetadata, bool) {
			switch name {
			case "sssd":
				return config.PluginMetadata{
					Priority:     80,
					Dangerous:    true,
					DangerReason: "test danger reason",
				}, true
			case "hostname":
				return config.PluginMetadata{Priority: 20, Dangerous: false}, true
			default:
				return config.PluginMetadata{}, false
			}
		},
		RenderPlugin: func(_ context.Context, pluginName, _ string) (api.InstallInstruction, string, error) {
			return api.InstallInstruction{
				Opcode:  "overwrite",
				Target:  "/etc/" + pluginName + ".conf",
				Payload: "# " + pluginName + " config\n",
			}, "renderedhash-" + pluginName, nil
		},
	}
}

func dangerousJSONRequest(t *testing.T, method, path string, body interface{}) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func withPendingID(r *http.Request, id string) *http.Request {
	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("pending_id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
}

func withNodeID(r *http.Request, id string) *http.Request {
	chiCtx := chi.NewRouteContext()
	chiCtx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chiCtx))
}

func decodeJSONDangerous(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(v); err != nil {
		t.Fatalf("decodeJSON: %v (body: %s)", err, w.Body.String())
	}
}

func stageSSSD(t *testing.T, h *DangerousPushHandler) string {
	t.Helper()
	body := map[string]string{"node_id": "node-1", "plugin_name": "sssd"}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("stage: want 202, got %d (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		PendingID string `json:"pending_id"`
	}
	decodeJSONDangerous(t, w, &resp)
	if resp.PendingID == "" {
		t.Fatal("stage returned empty pending_id")
	}
	return resp.PendingID
}

func confirm(t *testing.T, h *DangerousPushHandler, pendingID, str string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]string{"confirm_string": str}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push/"+pendingID+"/confirm", body)
	r = withPendingID(r, pendingID)
	w := httptest.NewRecorder()
	h.HandleConfirm(w, r)
	return w
}

// ─── Stage tests ──────────────────────────────────────────────────────────────

func TestDangerousPush_Stage_NonDangerousPlugin(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	body := map[string]string{"node_id": "node-1", "plugin_name": "hostname"}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("non-dangerous plugin: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp api.ErrorResponse
	decodeJSONDangerous(t, w, &resp)
	if resp.Code != "plugin_not_dangerous" {
		t.Errorf("expected code plugin_not_dangerous, got %q", resp.Code)
	}
}

func TestDangerousPush_Stage_UnknownPlugin(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	body := map[string]string{"node_id": "node-1", "plugin_name": "nonexistent"}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("unknown plugin: want 404, got %d", w.Code)
	}
}

func TestDangerousPush_Stage_Happy(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	body := map[string]string{"node_id": "node-1", "plugin_name": "sssd"}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("stage happy: want 202, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp struct {
		PendingID     string `json:"pending_id"`
		DangerReason  string `json:"danger_reason"`
		ConfirmPrompt string `json:"confirm_prompt"`
		ExpiresAt     string `json:"expires_at"`
	}
	decodeJSONDangerous(t, w, &resp)

	if resp.PendingID == "" {
		t.Error("pending_id is empty")
	}
	if resp.DangerReason == "" {
		t.Error("danger_reason is empty")
	}
	if resp.ConfirmPrompt == "" {
		t.Error("confirm_prompt is empty")
	}
	if len(fdb.rows) != 1 {
		t.Errorf("expected 1 staged row, got %d", len(fdb.rows))
	}
	for _, row := range fdb.rows {
		if row.Challenge != "mycluster" {
			t.Errorf("challenge: want mycluster, got %q", row.Challenge)
		}
	}
}

// ─── Confirm tests ────────────────────────────────────────────────────────────

func TestDangerousPush_Confirm_Happy(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	pendingID := stageSSSD(t, h)
	w := confirm(t, h, pendingID, "mycluster")

	if w.Code != http.StatusOK {
		t.Fatalf("confirm happy: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp struct {
		OK     bool   `json:"ok"`
		Plugin string `json:"plugin"`
	}
	decodeJSONDangerous(t, w, &resp)
	if !resp.OK {
		t.Error("ok should be true")
	}
	if resp.Plugin != "sssd" {
		t.Errorf("plugin: want sssd, got %q", resp.Plugin)
	}

	// Hub received the push.
	if hub.sentCount != 1 {
		t.Errorf("hub sent count: want 1, got %d", hub.sentCount)
	}

	// Row consumed.
	row, _ := fdb.GetPendingDangerousPush(context.Background(), pendingID)
	if !row.Consumed {
		t.Error("row should be consumed after confirmation")
	}
}

func TestDangerousPush_Confirm_Mismatch(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	pendingID := stageSSSD(t, h)
	w := confirm(t, h, pendingID, "wrongcluster")

	if w.Code != http.StatusBadRequest {
		t.Errorf("mismatch: want 400, got %d", w.Code)
	}
	var resp api.ErrorResponse
	decodeJSONDangerous(t, w, &resp)
	if resp.Code != "confirm_mismatch" {
		t.Errorf("expected code confirm_mismatch, got %q", resp.Code)
	}
	if fdb.attempts[pendingID] != 1 {
		t.Errorf("attempt count: want 1, got %d", fdb.attempts[pendingID])
	}
	row, _ := fdb.GetPendingDangerousPush(context.Background(), pendingID)
	if row.Consumed {
		t.Error("row must not be consumed after only 1 wrong attempt")
	}
}

func TestDangerousPush_Confirm_ThreeStrikeLockout(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	pendingID := stageSSSD(t, h)

	for i := 1; i <= 3; i++ {
		w := confirm(t, h, pendingID, "wrong")
		switch i {
		case 1, 2:
			if w.Code != http.StatusBadRequest {
				t.Errorf("attempt %d: want 400, got %d", i, w.Code)
			}
		case 3:
			if w.Code != http.StatusGone {
				t.Errorf("3rd attempt: want 410, got %d (body: %s)", w.Code, w.Body.String())
			}
		}
	}

	row, _ := fdb.GetPendingDangerousPush(context.Background(), pendingID)
	if !row.Consumed {
		t.Error("row must be consumed after 3-strike lockout")
	}
	if hub.sentCount != 0 {
		t.Errorf("no WS push should have been delivered: hub sent %d", hub.sentCount)
	}
}

func TestDangerousPush_Confirm_Expired(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	expiredID := "exp-id-999"
	fdb.rows[expiredID] = &db.PendingDangerousPush{
		ID:          expiredID,
		NodeID:      "node-1",
		PluginName:  "sssd",
		PayloadJSON: `{"target":"sssd","content":"","checksum":"sha256:x"}`,
		Reason:      "test",
		Challenge:   "mycluster",
		ExpiresAt:   time.Now().UTC().Add(-2 * time.Minute), // expired
		CreatedAt:   time.Now().UTC().Add(-13 * time.Minute),
	}

	w := confirm(t, h, expiredID, "mycluster")
	if w.Code != http.StatusGone {
		t.Errorf("expired: want 410, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestDangerousPush_Confirm_NotFound(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	w := confirm(t, h, "does-not-exist", "mycluster")
	if w.Code != http.StatusNotFound {
		t.Errorf("not found: want 404, got %d", w.Code)
	}
}

func TestDangerousPush_Confirm_AlreadyConsumed(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	h := buildDangerousHandler(fdb, hub, "mycluster")

	pendingID := stageSSSD(t, h)

	// First confirm succeeds.
	w1 := confirm(t, h, pendingID, "mycluster")
	if w1.Code != http.StatusOK {
		t.Fatalf("first confirm: want 200, got %d", w1.Code)
	}

	// Second confirm should be gone.
	w2 := confirm(t, h, pendingID, "mycluster")
	if w2.Code != http.StatusGone {
		t.Errorf("re-confirm: want 410, got %d (body: %s)", w2.Code, w2.Body.String())
	}
}

// ─── ConfigPush gate tests ────────────────────────────────────────────────────

func TestConfigPush_RejectsDangerousPlugin_GateEnabled(t *testing.T) {
	h := &ClientdHandler{
		DangerousGateEnabled: true,
		IsPluginDangerous:    func(target string) bool { return target == "sssd" },
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]string{"target": "sssd", "content": "[sssd]\n"})
	r := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/n1/config-push", &buf)
	r.Header.Set("Content-Type", "application/json")
	r = withNodeID(r, "n1")
	w := httptest.NewRecorder()
	h.ConfigPush(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("gate enabled: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp api.ErrorResponse
	decodeJSONDangerous(t, w, &resp)
	if resp.Code != "use_dangerous_push" {
		t.Errorf("expected code use_dangerous_push, got %q", resp.Code)
	}
}

func TestConfigPush_AllowsNonDangerous_GateEnabled(t *testing.T) {
	// Provide a hub whose IsConnected returns false so we get 502 (not 409).
	// The test only verifies the dangerous-gate does not fire for a non-dangerous target.
	h := &ClientdHandler{
		Hub:                  &fakeHubDangerous{connected: false}, // IsConnected=false → 502, not 409
		DangerousGateEnabled: true,
		IsPluginDangerous:    func(target string) bool { return target == "sssd" },
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]string{"target": "hostname", "content": "host\n"})
	r := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/n1/config-push", &buf)
	r.Header.Set("Content-Type", "application/json")
	r = withNodeID(r, "n1")
	w := httptest.NewRecorder()
	h.ConfigPush(w, r)

	// We reach the Hub.IsConnected call (which returns false → 502).
	// What matters: it must not be 409 (gate must not have fired).
	if w.Code == http.StatusConflict {
		t.Errorf("non-dangerous target: must not get 409, got 409")
	}
}

func TestConfigPush_GateFlagOff_AllowsDangerous(t *testing.T) {
	// Provide a hub whose IsConnected returns false so we get 502 (not 409).
	// The test only verifies the dangerous-gate does not fire when the flag is off.
	h := &ClientdHandler{
		Hub:                  &fakeHubDangerous{connected: false}, // IsConnected=false → 502, not 409
		DangerousGateEnabled: false,                               // gate is off
		IsPluginDangerous:    func(_ string) bool { return true },
	}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]string{"target": "sssd", "content": "[sssd]\n"})
	r := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/n1/config-push", &buf)
	r.Header.Set("Content-Type", "application/json")
	r = withNodeID(r, "n1")
	w := httptest.NewRecorder()
	h.ConfigPush(w, r)

	// Gate is off → should not get 409 (we'll get 502 from IsConnected=false).
	if w.Code == http.StatusConflict {
		t.Errorf("gate disabled: should not get 409")
	}
}

// ─── SSSD metadata unit test ──────────────────────────────────────────────────

// TestSSSDPlugin_IsDangerous verifies the Sprint 41 Day 3 flip.
func TestSSSDPlugin_IsDangerous(t *testing.T) {
	meta := plugins.SSSDPlugin{}.Metadata()
	if !meta.Dangerous {
		t.Error("SSSDPlugin.Metadata().Dangerous should be true after Sprint 41 Day 3 flip")
	}
	if meta.DangerReason == "" {
		t.Error("SSSDPlugin.Metadata().DangerReason must be non-empty when Dangerous=true")
	}
	if err := config.ValidatePluginMetadata("sssd", meta); err != nil {
		t.Errorf("ValidatePluginMetadata: %v", err)
	}
}

// ─── Sprint 42 Day 3: ValidatePayload + Phase 2 re-validation tests ───────────

// fakePayloadValidator is a minimal Plugin + PayloadValidator that allows tests
// to inject controlled violation lists without touching the real registry.
type fakePayloadValidator struct {
	name       string
	violations []config.PayloadValidationError
}

func (f *fakePayloadValidator) Name() string                { return f.name }
func (f *fakePayloadValidator) WatchedKeys() []string       { return nil }
func (f *fakePayloadValidator) Render(_ config.ClusterState) ([]api.InstallInstruction, error) {
	return nil, nil
}
func (f *fakePayloadValidator) Metadata() config.PluginMetadata {
	return config.PluginMetadata{
		Priority:     80,
		Dangerous:    true,
		DangerReason: "test danger",
	}
}
func (f *fakePayloadValidator) ValidatePayload(_ []byte) []config.PayloadValidationError {
	return f.violations
}

// buildHandlerWithValidator returns a DangerousPushHandler wired so that the
// "sssd" plugin lookup returns the given validator (which may also be nil to
// simulate a plugin-gone scenario).
func buildHandlerWithValidator(fdb *fakeDangerousDB, hub *fakeHubDangerous, clusterName string, validator *fakePayloadValidator) *DangerousPushHandler {
	h := buildDangerousHandler(fdb, hub, clusterName)
	h.LookupPlugin = func(name string) (config.Plugin, bool) {
		if validator != nil && name == validator.name {
			return validator, true
		}
		// All other plugin names: not found (simulate plugin-gone).
		return nil, false
	}
	return h
}

// TestDangerousPush_Stage_SemanticValidation_Pass verifies that when
// ValidatePayload returns no violations the stage proceeds normally (202).
func TestDangerousPush_Stage_SemanticValidation_Pass(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	validator := &fakePayloadValidator{name: "sssd", violations: nil}
	h := buildHandlerWithValidator(fdb, hub, "mycluster", validator)

	// Send a stage request with a payload field so LookupPlugin + ValidatePayload are exercised.
	body := map[string]interface{}{
		"node_id":     "node-1",
		"plugin_name": "sssd",
		"payload":     json.RawMessage(`{"config":{"ldap_uri":"ldaps://clustr.local:636"}}`),
	}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("semantic validation pass: want 202, got %d (body: %s)", w.Code, w.Body.String())
	}
}

// TestDangerousPush_Stage_SemanticValidation_Fail verifies that when
// ValidatePayload returns violations the stage returns 400 with the
// MULTI-ERROR-ROLLUP shape and does not persist a pending row.
func TestDangerousPush_Stage_SemanticValidation_Fail(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{}
	validator := &fakePayloadValidator{
		name: "sssd",
		violations: []config.PayloadValidationError{
			{Path: "config.ldap_uri", Message: "scheme must be ldap or ldaps", Code: "invalid_uri"},
		},
	}
	h := buildHandlerWithValidator(fdb, hub, "mycluster", validator)

	body := map[string]interface{}{
		"node_id":     "node-1",
		"plugin_name": "sssd",
		"payload":     json.RawMessage(`{"config":{"ldap_uri":"http://bad"}}`),
	}
	r := dangerousJSONRequest(t, http.MethodPost, "/config/dangerous-push", body)
	w := httptest.NewRecorder()
	h.HandleStage(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("semantic validation fail: want 400, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Response must be the MULTI-ERROR-ROLLUP shape.
	var resp struct {
		Error      string                `json:"error"`
		Violations []ValidationViolation `json:"violations"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "validation_failed" {
		t.Errorf("error field: want validation_failed, got %q", resp.Error)
	}
	if len(resp.Violations) == 0 {
		t.Error("expected at least one violation in response")
	}
	if resp.Violations[0].Code != "invalid_uri" {
		t.Errorf("violation code: want invalid_uri, got %q", resp.Violations[0].Code)
	}

	// No pending row must have been inserted.
	if len(fdb.rows) != 0 {
		t.Errorf("expected no pending rows after validation failure, got %d", len(fdb.rows))
	}
}

// TestDangerousPush_Confirm_PluginGone verifies that when the plugin has been
// unregistered between stage and confirm the confirm endpoint returns 409 and
// marks the pending row as consumed.
func TestDangerousPush_Confirm_PluginGone(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{connected: true}
	clusterName := "mycluster"

	// Stage using the standard helper (no LookupPlugin wired yet).
	stageH := buildDangerousHandler(fdb, hub, clusterName)
	pendingID := stageSSSD(t, stageH)

	// Now build a confirm handler where the plugin is gone (LookupPlugin returns false).
	confirmH := buildDangerousHandler(fdb, hub, clusterName)
	confirmH.LookupPlugin = func(_ string) (config.Plugin, bool) {
		return nil, false // plugin no longer present
	}

	w := confirm(t, confirmH, pendingID, clusterName)
	if w.Code != http.StatusConflict {
		t.Fatalf("plugin-gone confirm: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp api.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "pending_invalidated" {
		t.Errorf("code: want pending_invalidated, got %q", resp.Code)
	}

	// Row must be consumed so it can't be retried.
	row, ok := fdb.rows[pendingID]
	if !ok {
		t.Fatal("pending row disappeared from DB (expected consumed=true, not deleted)")
	}
	if !row.Consumed {
		t.Error("expected pending row to be consumed after plugin-gone")
	}
}

// TestDangerousPush_Confirm_RevalidationFails verifies that when the plugin is
// still present but ValidatePayload now returns errors the confirm endpoint
// returns 409 and marks the pending row as consumed.
func TestDangerousPush_Confirm_RevalidationFails(t *testing.T) {
	fdb := newFakeDangerousDB()
	hub := &fakeHubDangerous{connected: true}
	clusterName := "mycluster"

	// Stage with a passing validator.
	stageValidator := &fakePayloadValidator{name: "sssd", violations: nil}
	stageH := buildHandlerWithValidator(fdb, hub, clusterName, stageValidator)
	pendingID := stageSSSD(t, stageH)

	// Confirm with a validator that now returns a violation.
	confirmValidator := &fakePayloadValidator{
		name: "sssd",
		violations: []config.PayloadValidationError{
			{Path: "config.ldap_uri", Message: "server decommissioned", Code: "invalid_uri"},
		},
	}
	confirmH := buildHandlerWithValidator(fdb, hub, clusterName, confirmValidator)
	w := confirm(t, confirmH, pendingID, clusterName)

	if w.Code != http.StatusConflict {
		t.Fatalf("re-validation fail: want 409, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp api.ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != "pending_invalidated" {
		t.Errorf("code: want pending_invalidated, got %q", resp.Code)
	}

	// Row must be consumed.
	row, ok := fdb.rows[pendingID]
	if !ok {
		t.Fatal("pending row disappeared from DB")
	}
	if !row.Consumed {
		t.Error("expected pending row to be consumed after re-validation failure")
	}
}

// TestVerbConfigDangerousPush verifies the permission verb constant.
func TestVerbConfigDangerousPush(t *testing.T) {
	if auth.VerbConfigDangerousPush == "" {
		t.Error("auth.VerbConfigDangerousPush must not be empty")
	}
	if auth.VerbConfigDangerousPush != "config.dangerous_push" {
		t.Errorf("VerbConfigDangerousPush: want config.dangerous_push, got %q", auth.VerbConfigDangerousPush)
	}
}
