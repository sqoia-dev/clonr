// bios_apply_test.go — unit tests for BiosApplyOnNode (Sprint 26).
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sqoia-dev/clustr/internal/bios"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── fake BiosDB ──────────────────────────────────────────────────────────────

type fakeBiosApplyDB struct {
	profiles     map[string]api.BiosProfile
	nodeBindings map[string]api.NodeBiosProfile
	recordedHash string
}

func newFakeBiosApplyDB() *fakeBiosApplyDB {
	return &fakeBiosApplyDB{
		profiles:     make(map[string]api.BiosProfile),
		nodeBindings: make(map[string]api.NodeBiosProfile),
	}
}

func (f *fakeBiosApplyDB) GetNodeBiosProfile(_ context.Context, nodeID string) (api.NodeBiosProfile, error) {
	b, ok := f.nodeBindings[nodeID]
	if !ok {
		return api.NodeBiosProfile{}, api.ErrNotFound
	}
	return b, nil
}

func (f *fakeBiosApplyDB) GetBiosProfile(_ context.Context, id string) (api.BiosProfile, error) {
	p, ok := f.profiles[id]
	if !ok {
		return api.BiosProfile{}, api.ErrNotFound
	}
	return p, nil
}

func (f *fakeBiosApplyDB) RecordBiosApply(_ context.Context, nodeID, hash string) error {
	f.recordedHash = hash
	return nil
}

// ─── fake hub for bios apply ─────────────────────────────────────────────────

type fakeBiosApplyHub struct {
	connected   bool
	biosApplyCh chan clientd.BiosApplyResultPayload
	biosReadCh  chan clientd.BiosReadResultPayload
	lastSent    clientd.ServerMessage
}

func (h *fakeBiosApplyHub) RegisterConn(nodeID string, conn *websocket.Conn, send chan []byte, cancel context.CancelFunc) {
}
func (h *fakeBiosApplyHub) Unregister(nodeID string)           {}
func (h *fakeBiosApplyHub) ConnectedNodes() []string           { return nil }
func (h *fakeBiosApplyHub) IsConnected(nodeID string) bool     { return h.connected }
func (h *fakeBiosApplyHub) AppendJournalEntries(_ string, _ []api.LogEntry) {}

func (h *fakeBiosApplyHub) Send(nodeID string, msg clientd.ServerMessage) error {
	h.lastSent = msg
	return nil
}
func (h *fakeBiosApplyHub) RegisterAck(msgID string) <-chan clientd.AckPayload { return nil }
func (h *fakeBiosApplyHub) UnregisterAck(msgID string)                          {}
func (h *fakeBiosApplyHub) DeliverAck(msgID string, payload clientd.AckPayload) bool { return false }
func (h *fakeBiosApplyHub) RegisterExec(msgID string) <-chan clientd.ExecResultPayload {
	return nil
}
func (h *fakeBiosApplyHub) UnregisterExec(msgID string) {}
func (h *fakeBiosApplyHub) DeliverExecResult(msgID string, payload clientd.ExecResultPayload) bool {
	return false
}
func (h *fakeBiosApplyHub) RegisterOperatorExec(msgID string) <-chan clientd.OperatorExecResultPayload {
	return nil
}
func (h *fakeBiosApplyHub) UnregisterOperatorExec(msgID string) {}
func (h *fakeBiosApplyHub) DeliverOperatorExecResult(msgID string, payload clientd.OperatorExecResultPayload) bool {
	return false
}
func (h *fakeBiosApplyHub) RegisterDiskCapture(msgID string) <-chan clientd.DiskCaptureResultPayload {
	return nil
}
func (h *fakeBiosApplyHub) UnregisterDiskCapture(msgID string) {}
func (h *fakeBiosApplyHub) DeliverDiskCaptureResult(msgID string, payload clientd.DiskCaptureResultPayload) bool {
	return false
}
func (h *fakeBiosApplyHub) RegisterBiosRead(msgID string) <-chan clientd.BiosReadResultPayload {
	return h.biosReadCh
}
func (h *fakeBiosApplyHub) UnregisterBiosRead(msgID string) {}
func (h *fakeBiosApplyHub) DeliverBiosReadResult(msgID string, payload clientd.BiosReadResultPayload) bool {
	return false
}
func (h *fakeBiosApplyHub) RegisterBiosApply(msgID string) <-chan clientd.BiosApplyResultPayload {
	return h.biosApplyCh
}
func (h *fakeBiosApplyHub) UnregisterBiosApply(msgID string) {}
func (h *fakeBiosApplyHub) DeliverBiosApplyResult(msgID string, payload clientd.BiosApplyResultPayload) bool {
	return false
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newBiosApplyRouter(h *ClientdHandler) http.Handler {
	r := chi.NewRouter()
	r.Post("/nodes/{id}/bios/apply", h.BiosApplyOnNode)
	r.Post("/nodes/{id}/bios/read", h.ReadBiosOnNode)
	return r
}

// seedBiosApplyFixtures creates a profile and node binding in the DB.
func seedBiosApplyFixtures(db *fakeBiosApplyDB) (nodeID, profileID string) {
	profileID = uuid.NewString()
	nodeID = uuid.NewString()
	now := time.Now().UTC()
	db.profiles[profileID] = api.BiosProfile{
		ID:           profileID,
		Name:         "test-profile",
		Vendor:       "intel",
		SettingsJSON: `{"Intel(R) Hyper-Threading Technology":"Disable"}`,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	db.nodeBindings[nodeID] = api.NodeBiosProfile{
		NodeID:    nodeID,
		ProfileID: profileID,
	}
	return nodeID, profileID
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestBiosApplyOnNode_NoBiosDB verifies 503 when BiosDB is not wired.
func TestBiosApplyOnNode_NoBiosDB(t *testing.T) {
	h := &ClientdHandler{
		Hub: &fakeBiosApplyHub{connected: true},
	}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/some-node/bios/apply", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestBiosApplyOnNode_NodeOffline verifies 502 when node is not connected.
func TestBiosApplyOnNode_NodeOffline(t *testing.T) {
	hub := &fakeBiosApplyHub{connected: false}
	h := &ClientdHandler{
		Hub:    hub,
		BiosDB: newFakeBiosApplyDB(),
	}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/some-node/bios/apply", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestBiosApplyOnNode_NoProfile verifies 404 when node has no profile assigned.
func TestBiosApplyOnNode_NoProfile(t *testing.T) {
	db := newFakeBiosApplyDB()
	hub := &fakeBiosApplyHub{
		connected:  true,
		biosReadCh: make(chan clientd.BiosReadResultPayload, 1),
	}
	h := &ClientdHandler{Hub: hub, BiosDB: db}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/no-profile-node/bios/apply", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for node with no profile, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestBiosApplyOnNode_NoChanges verifies the "no changes" path when current
// settings already match the profile.
func TestBiosApplyOnNode_NoChanges(t *testing.T) {
	// Stub biosLookup to return a provider whose Diff always returns empty.
	origLookup := biosLookup
	defer func() { biosLookup = origLookup }()
	biosLookup = func(vendor string) (bios.Provider, error) {
		return &stubBiosProvider{diffResult: []bios.Change{}}, nil
	}

	db := newFakeBiosApplyDB()
	nodeID, _ := seedBiosApplyFixtures(db)

	readCh := make(chan clientd.BiosReadResultPayload, 1)
	readCh <- clientd.BiosReadResultPayload{
		RefMsgID: "read-msg",
		Vendor:   "intel",
		Settings: []clientd.BiosSetting{
			{Name: "Intel(R) Hyper-Threading Technology", Value: "Disable"},
		},
	}

	hub := &fakeBiosApplyHub{
		connected:  true,
		biosReadCh: readCh,
		biosApplyCh: make(chan clientd.BiosApplyResultPayload, 1),
	}
	h := &ClientdHandler{Hub: hub, BiosDB: db}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/bios/apply", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.BiosApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Applied != 0 {
		t.Errorf("applied = %d, want 0 for no-changes path", resp.Applied)
	}
}

// TestBiosApplyOnNode_ApplySuccess tests the happy path: diff finds changes,
// node returns a successful bios_apply_result.
func TestBiosApplyOnNode_ApplySuccess(t *testing.T) {
	origLookup := biosLookup
	defer func() { biosLookup = origLookup }()
	biosLookup = func(vendor string) (bios.Provider, error) {
		return &stubBiosProvider{
			diffResult: []bios.Change{
				{Setting: bios.Setting{Name: "Intel(R) Hyper-Threading Technology", Value: "Disable"}, From: "Enable", To: "Disable"},
			},
		}, nil
	}

	db := newFakeBiosApplyDB()
	nodeID, profileID := seedBiosApplyFixtures(db)

	readCh := make(chan clientd.BiosReadResultPayload, 1)
	readCh <- clientd.BiosReadResultPayload{
		RefMsgID: "read-msg",
		Vendor:   "intel",
		Settings: []clientd.BiosSetting{
			{Name: "Intel(R) Hyper-Threading Technology", Value: "Enable"},
		},
	}
	applyCh := make(chan clientd.BiosApplyResultPayload, 1)
	applyCh <- clientd.BiosApplyResultPayload{
		RefMsgID:     "apply-msg",
		ProfileID:    profileID,
		OK:           true,
		AppliedCount: 1,
	}

	hub := &fakeBiosApplyHub{
		connected:   true,
		biosReadCh:  readCh,
		biosApplyCh: applyCh,
	}
	h := &ClientdHandler{Hub: hub, BiosDB: db}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/bios/apply",
		bytes.NewReader([]byte{}))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp api.BiosApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Applied != 1 {
		t.Errorf("applied = %d, want 1", resp.Applied)
	}
	if resp.Message == "" {
		t.Error("expected non-empty message in apply response")
	}
	// Verify settings hash was recorded.
	if db.recordedHash == "" {
		t.Error("expected RecordBiosApply to be called with a hash")
	}
}

// TestBiosApplyOnNode_NodeReportsError tests that a node-side apply failure
// is surfaced as a 502.
func TestBiosApplyOnNode_NodeReportsError(t *testing.T) {
	origLookup := biosLookup
	defer func() { biosLookup = origLookup }()
	biosLookup = func(vendor string) (bios.Provider, error) {
		return &stubBiosProvider{
			diffResult: []bios.Change{
				{Setting: bios.Setting{Name: "HT", Value: "Disable"}, From: "Enable", To: "Disable"},
			},
		}, nil
	}

	db := newFakeBiosApplyDB()
	nodeID, _ := seedBiosApplyFixtures(db)

	readCh := make(chan clientd.BiosReadResultPayload, 1)
	readCh <- clientd.BiosReadResultPayload{
		Vendor:   "intel",
		Settings: []clientd.BiosSetting{{Name: "HT", Value: "Enable"}},
	}
	applyCh := make(chan clientd.BiosApplyResultPayload, 1)
	applyCh <- clientd.BiosApplyResultPayload{
		OK:    false,
		Error: "syscfg: binary not found",
	}

	hub := &fakeBiosApplyHub{
		connected:   true,
		biosReadCh:  readCh,
		biosApplyCh: applyCh,
	}
	h := &ClientdHandler{Hub: hub, BiosDB: db}
	router := newBiosApplyRouter(h)

	req := httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/bios/apply", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ─── stubBiosProvider ─────────────────────────────────────────────────────────

// stubBiosProvider is a bios.Provider that returns pre-configured diff results.
type stubBiosProvider struct {
	diffResult []bios.Change
	diffErr    error
}

func (s *stubBiosProvider) Vendor() string { return "intel" }
func (s *stubBiosProvider) ReadCurrent(_ context.Context) ([]bios.Setting, error) {
	return nil, nil
}
func (s *stubBiosProvider) Diff(desired, current []bios.Setting) ([]bios.Change, error) {
	return s.diffResult, s.diffErr
}
func (s *stubBiosProvider) Apply(_ context.Context, changes []bios.Change) ([]bios.Change, error) {
	return changes, nil
}
func (s *stubBiosProvider) SupportedSettings(_ context.Context) ([]string, error) {
	return nil, nil
}
