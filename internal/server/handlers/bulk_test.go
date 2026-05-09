package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/power"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── stubs ────────────────────────────────────────────────────────────────────

// stubProvider records calls and returns canned errors. Used to drive the
// fan-out without exec'ing real ipmitool.
type stubProvider struct {
	name string
	// onPowerOn et al. are checked in order. Nil = success.
	failPowerCycle bool
	failPowerOn    bool
	failPowerOff   bool
	failReset      bool
	failNetboot    bool
	calls          int32 // atomic — fan-out goroutines hit this concurrently
	// inflight tracks max-concurrent for the concurrency-cap test.
	inflight    int32
	maxInflight *int32
	delay       time.Duration
}

func (p *stubProvider) Name() string { return p.name }
func (p *stubProvider) Status(_ context.Context) (power.PowerStatus, error) {
	return power.PowerOn, nil
}
func (p *stubProvider) PowerOn(_ context.Context) error  { return p.tick(p.failPowerOn) }
func (p *stubProvider) PowerOff(_ context.Context) error { return p.tick(p.failPowerOff) }
func (p *stubProvider) PowerCycle(_ context.Context) error {
	return p.tick(p.failPowerCycle)
}
func (p *stubProvider) Reset(_ context.Context) error { return p.tick(p.failReset) }
func (p *stubProvider) SetNextBoot(_ context.Context, _ power.BootDevice) error {
	return p.tick(p.failNetboot)
}
func (p *stubProvider) SetPersistentBootOrder(_ context.Context, _ []power.BootDevice) error {
	return power.ErrNotSupported
}

func (p *stubProvider) tick(fail bool) error {
	atomic.AddInt32(&p.calls, 1)
	now := atomic.AddInt32(&p.inflight, 1)
	defer atomic.AddInt32(&p.inflight, -1)
	if p.maxInflight != nil {
		// CAS-style update of max — simple: keep retrying until current max
		// is at least 'now'.
		for {
			cur := atomic.LoadInt32(p.maxInflight)
			if now <= cur {
				break
			}
			if atomic.CompareAndSwapInt32(p.maxInflight, cur, now) {
				break
			}
		}
	}
	if p.delay > 0 {
		time.Sleep(p.delay)
	}
	if fail {
		return errors.New("provider unreachable")
	}
	return nil
}

// stubBulkExec satisfies BulkExecRunner.
type stubBulkExec struct {
	mu     sync.Mutex
	calls  []stubExecCall
	exit   int
	output string
	err    error
}

type stubExecCall struct {
	NodeID  string
	Command string
	Args    []string
	Timeout int
}

func (s *stubBulkExec) ExecOne(_ context.Context, nodeID, command string, args []string, timeoutSec int) (int, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubExecCall{NodeID: nodeID, Command: command, Args: append([]string(nil), args...), Timeout: timeoutSec})
	return s.exit, s.output, s.err
}

// stubBulkReimage satisfies BulkReimageRunner.
type stubBulkReimage struct {
	mu       sync.Mutex
	calls    []stubReimageCall
	idPrefix string
	failFor  map[string]bool
}

type stubReimageCall struct {
	NodeID      string
	ImageID     string
	Force       bool
	RequestedBy string
}

func (s *stubBulkReimage) StartReimage(_ context.Context, nodeID, imageID string, force bool, requestedBy string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubReimageCall{NodeID: nodeID, ImageID: imageID, Force: force, RequestedBy: requestedBy})
	if s.failFor[nodeID] {
		return "", errors.New("reimage runner failed")
	}
	if s.idPrefix == "" {
		s.idPrefix = "rid-"
	}
	return s.idPrefix + nodeID, nil
}

// fakeBulkDB satisfies BulkDB.
type fakeBulkDB struct {
	mu    sync.Mutex
	nodes map[string]api.NodeConfig
}

func (f *fakeBulkDB) GetNodeConfig(_ context.Context, id string) (api.NodeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.nodes[id]
	if !ok {
		return api.NodeConfig{}, api.ErrNotFound
	}
	return cfg, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func newBulkRouter(h *BulkHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/v1/nodes/bulk/power/{action}", h.HandleBulkPower)
	r.Post("/api/v1/nodes/bulk/reimage", h.HandleBulkReimage)
	r.Post("/api/v1/nodes/bulk/drain", h.HandleBulkDrain)
	r.Post("/api/v1/nodes/bulk/netboot", h.HandleBulkNetboot)
	r.Post("/api/v1/nodes/bulk/exec", h.HandleBulkExec)
	return r
}

func makeBulkDB(n int) *fakeBulkDB {
	db := &fakeBulkDB{nodes: map[string]api.NodeConfig{}}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("node-%02d", i)
		db.nodes[id] = api.NodeConfig{
			ID:       id,
			Hostname: id,
			BMC: &api.BMCNodeConfig{
				IPAddress: fmt.Sprintf("192.168.10.%d", 10+i),
				Username:  "admin",
				Password:  "secret",
			},
		}
	}
	return db
}

// ─── bulk power tests ─────────────────────────────────────────────────────────

func TestBulkPower_FanoutReturnsResultPerNode(t *testing.T) {
	db := makeBulkDB(4)
	prov := &stubProvider{name: "ipmi"}
	h := &BulkHandler{
		DB:              db,
		ProviderFactory: func(_ api.NodeConfig) (power.Provider, error) { return prov, nil },
	}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00", "node-01", "node-02", "node-03"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/cycle", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp BulkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 4 {
		t.Fatalf("results len = %d, want 4: %+v", len(resp.Results), resp.Results)
	}
	wantIDs := map[string]bool{"node-00": true, "node-01": true, "node-02": true, "node-03": true}
	for _, res := range resp.Results {
		if !res.OK {
			t.Errorf("node %s: %s", res.NodeID, res.Error)
		}
		if !wantIDs[res.NodeID] {
			t.Errorf("unexpected node id %q", res.NodeID)
		}
		delete(wantIDs, res.NodeID)
	}
	if got := atomic.LoadInt32(&prov.calls); got != 4 {
		t.Errorf("provider calls = %d, want 4", got)
	}
}

func TestBulkPower_PartialFailureDoesNotFailBatch(t *testing.T) {
	db := makeBulkDB(3)
	// Per-node provider — node-01 fails, others succeed.
	failNode := "node-01"
	h := &BulkHandler{
		DB: db,
		ProviderFactory: func(cfg api.NodeConfig) (power.Provider, error) {
			return &stubProvider{name: "ipmi", failPowerCycle: cfg.ID == failNode}, nil
		},
	}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00", "node-01", "node-02"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/cycle", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp BulkResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	if len(resp.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(resp.Results))
	}
	byID := map[string]BulkNodeResult{}
	for _, r := range resp.Results {
		byID[r.NodeID] = r
	}
	if !byID["node-00"].OK || !byID["node-02"].OK {
		t.Errorf("expected node-00 and node-02 ok, got %+v", byID)
	}
	if byID["node-01"].OK {
		t.Errorf("expected node-01 to fail, got ok")
	}
	if byID["node-01"].Error == "" {
		t.Errorf("node-01 missing error string")
	}
}

func TestBulkPower_ConcurrencyCapEnforced(t *testing.T) {
	t.Setenv("CLUSTR_BULK_CONCURRENCY", "4")

	db := makeBulkDB(20)
	var maxInflight int32
	prov := &stubProvider{name: "ipmi", maxInflight: &maxInflight, delay: 30 * time.Millisecond}

	h := &BulkHandler{
		DB:              db,
		ProviderFactory: func(_ api.NodeConfig) (power.Provider, error) { return prov, nil },
	}
	r := newBulkRouter(h)

	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("node-%02d", i)
	}
	body, _ := json.Marshal(BulkRequest{NodeIDs: ids})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/on", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := atomic.LoadInt32(&maxInflight); got > 4 {
		t.Errorf("max in-flight = %d, want <= 4 (cap=4)", got)
	}
	if got := atomic.LoadInt32(&prov.calls); got != 20 {
		t.Errorf("calls = %d, want 20", got)
	}
}

func TestBulkPower_InvalidActionRejected(t *testing.T) {
	h := &BulkHandler{DB: makeBulkDB(1)}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/explode", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBulkPower_EmptyNodeIDsRejected(t *testing.T) {
	h := &BulkHandler{DB: makeBulkDB(1)}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/cycle", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBulkPower_DuplicatesCollapsed(t *testing.T) {
	db := makeBulkDB(2)
	prov := &stubProvider{name: "ipmi"}
	h := &BulkHandler{
		DB:              db,
		ProviderFactory: func(_ api.NodeConfig) (power.Provider, error) { return prov, nil },
	}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00", "node-00", "node-01", "  ", ""}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/power/on", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp BulkResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 2 {
		t.Errorf("dedupe failed: %d rows, want 2: %+v", len(resp.Results), resp.Results)
	}
}

// ─── bulk reimage / drain / netboot / exec tests ──────────────────────────────

func TestBulkReimage_FanoutCallsRunner(t *testing.T) {
	db := makeBulkDB(3)
	rn := &stubBulkReimage{}
	h := &BulkHandler{DB: db, Reimage: rn}
	r := newBulkRouter(h)

	body, _ := json.Marshal(map[string]any{
		"node_ids": []string{"node-00", "node-01", "node-02"},
		"image_id": "img-7",
		"force":    true,
	})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/reimage", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(rn.calls) != 3 {
		t.Errorf("reimage runner calls = %d, want 3: %+v", len(rn.calls), rn.calls)
	}
	for _, c := range rn.calls {
		if c.ImageID != "img-7" || !c.Force {
			t.Errorf("call dispatched with image_id=%q force=%v", c.ImageID, c.Force)
		}
	}
}

func TestBulkReimage_RunnerNotConfigured(t *testing.T) {
	h := &BulkHandler{DB: makeBulkDB(1)}
	r := newBulkRouter(h)
	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/reimage", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", w.Code)
	}
}

// stubDrainer satisfies BulkDrainRunner with a recorded call list and a
// canned error.
type stubDrainer struct {
	mu    sync.Mutex
	calls []stubDrainCall
	err   error
}

type stubDrainCall struct {
	NodeIDs []string
	Reason  string
}

func (s *stubDrainer) DrainNodes(_ context.Context, nodeIDs []string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubDrainCall{NodeIDs: append([]string(nil), nodeIDs...), Reason: reason})
	return s.err
}

// TestBulkDrain_DispatchesViaController locks down Codex post-ship
// review issue #7: HandleBulkDrain previously called Exec.ExecOne per
// target ("scontrol" on each target node), but the exec runner refuses
// disconnected targets.  Drain is a slurmctld action — dispatch one
// RPC to the controller via h.Drain regardless of target connectivity.
func TestBulkDrain_DispatchesViaController(t *testing.T) {
	db := makeBulkDB(2)
	drainer := &stubDrainer{}
	h := &BulkHandler{DB: db, Drain: drainer}
	r := newBulkRouter(h)

	body, _ := json.Marshal(map[string]any{
		"node_ids": []string{"node-00", "node-01"},
		"reason":   "kernel-panic-investigate",
	})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/drain", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	if len(drainer.calls) != 1 {
		t.Fatalf("drainer was invoked %d times, want exactly 1 (single controller RPC for the batch)", len(drainer.calls))
	}
	got := drainer.calls[0]
	if got.Reason != "kernel-panic-investigate" {
		t.Errorf("reason = %q", got.Reason)
	}
	if len(got.NodeIDs) != 2 || got.NodeIDs[0] != "node-00" || got.NodeIDs[1] != "node-01" {
		t.Errorf("node ids = %v", got.NodeIDs)
	}

	// Per-node response entries must still be emitted so the UI can
	// render one row per selected node.
	var resp BulkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(resp.Results))
	}
	for _, row := range resp.Results {
		if !row.OK {
			t.Errorf("row %s: ok=false (%s)", row.NodeID, row.Error)
		}
	}
}

// TestBulkDrain_OfflineTargetsStillDrained — the whole point of issue
// #7 is that operators want to drain nodes that aren't connected to
// the clientd hub.  The new path dispatches via the controller, so
// target connectivity is irrelevant.  We exercise that by NOT wiring an
// Exec runner (the old path required it) and confirming drain still
// works through h.Drain.
func TestBulkDrain_OfflineTargetsStillDrained(t *testing.T) {
	db := makeBulkDB(1)
	drainer := &stubDrainer{}
	h := &BulkHandler{DB: db, Drain: drainer /* Exec deliberately nil */}
	r := newBulkRouter(h)

	body, _ := json.Marshal(map[string]any{"node_ids": []string{"node-00"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/drain", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(drainer.calls) != 1 {
		t.Fatalf("drain not dispatched (target was offline but should not block controller path)")
	}
}

// TestBulkDrain_NoRunnerWired503 verifies the operator-friendly 503
// response when the slurm manager isn't configured (no controller
// node, brand-new install, etc.).
func TestBulkDrain_NoRunnerWired503(t *testing.T) {
	db := makeBulkDB(1)
	h := &BulkHandler{DB: db /* Drain deliberately nil */}
	r := newBulkRouter(h)
	body, _ := json.Marshal(map[string]any{"node_ids": []string{"node-00"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/drain", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", w.Code)
	}
}

func TestBulkNetboot_SetsNextBootPXE(t *testing.T) {
	db := makeBulkDB(2)
	prov := &stubProvider{name: "ipmi"}
	h := &BulkHandler{
		DB:              db,
		ProviderFactory: func(_ api.NodeConfig) (power.Provider, error) { return prov, nil },
	}
	r := newBulkRouter(h)

	body, _ := json.Marshal(BulkRequest{NodeIDs: []string{"node-00", "node-01"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/netboot", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&prov.calls); got != 2 {
		t.Errorf("provider SetNextBoot calls = %d, want 2", got)
	}
}

func TestBulkExec_RequiresCommand(t *testing.T) {
	h := &BulkHandler{DB: makeBulkDB(1), Exec: &stubBulkExec{}}
	r := newBulkRouter(h)
	body, _ := json.Marshal(map[string]any{"node_ids": []string{"node-00"}})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestBulkExec_PropagatesExitAndOutput(t *testing.T) {
	db := makeBulkDB(2)
	exec := &stubBulkExec{exit: 0, output: "hello"}
	h := &BulkHandler{DB: db, Exec: exec}
	r := newBulkRouter(h)

	body, _ := json.Marshal(map[string]any{
		"node_ids": []string{"node-00", "node-01"},
		"command":  "uname",
		"args":     []string{"-a"},
	})
	req := httptest.NewRequest("POST", "/api/v1/nodes/bulk/exec", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp BulkResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Results) != 2 {
		t.Fatalf("results len = %d", len(resp.Results))
	}
	for _, res := range resp.Results {
		if !res.OK {
			t.Errorf("node %s failed: %s", res.NodeID, res.Error)
		}
		if got := res.Detail["output"]; got != "hello" {
			t.Errorf("output = %v, want hello", got)
		}
		if got := res.Detail["exit_code"]; got != float64(0) && got != 0 {
			t.Errorf("exit_code = %v", got)
		}
	}
}
