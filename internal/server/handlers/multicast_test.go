package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sqoia-dev/clustr/internal/multicast"
	"github.com/sqoia-dev/clustr/internal/server/handlers"
)

// mcTestDB satisfies both multicast.DB and handlers.MulticastDB for handler tests.
type mcTestDB struct {
	mu       sync.Mutex
	sessions map[string]multicast.Session
	members  map[string][]multicast.Member
	cfg      multicast.Config
}

func newMCTestDB() *mcTestDB {
	return &mcTestDB{
		sessions: make(map[string]multicast.Session),
		members:  make(map[string][]multicast.Member),
		cfg:      multicast.DefaultConfig(),
	}
}

func (f *mcTestDB) MulticastInsertSession(_ context.Context, s multicast.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	return nil
}

func (f *mcTestDB) MulticastUpdateSessionState(_ context.Context, id string, state multicast.State, extra multicast.SessionUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	s.State = state
	if extra.Error != "" {
		s.Error = extra.Error
	}
	f.sessions[id] = s
	return nil
}

func (f *mcTestDB) MulticastInsertMember(_ context.Context, m multicast.Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[m.SessionID] = append(f.members[m.SessionID], m)
	return nil
}

func (f *mcTestDB) MulticastUpdateMember(_ context.Context, sessionID, nodeID string, u multicast.MemberUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, m := range f.members[sessionID] {
		if m.NodeID == nodeID {
			if u.Outcome != "" {
				f.members[sessionID][i].Outcome = u.Outcome
			}
		}
	}
	return nil
}

func (f *mcTestDB) MulticastListActive(_ context.Context) ([]multicast.Session, error) {
	return nil, nil
}

func (f *mcTestDB) MulticastGetConfig(_ context.Context) (multicast.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

func (f *mcTestDB) MulticastGetSession(_ context.Context, id string) (multicast.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return multicast.Session{}, fmt.Errorf("not found")
	}
	return s, nil
}

// newMCScheduler builds a Scheduler with the given fake DB and config.
func newMCScheduler(db *mcTestDB, threshold int) *multicast.Scheduler {
	sender := func(_ context.Context, _ multicast.Session) error { return nil }
	cfg := multicast.DefaultConfig()
	cfg.Threshold = threshold
	sc := multicast.NewScheduler(db, sender, "http://localhost:8080")
	sc.SetConfig(cfg)
	return sc
}

func TestMulticastEnqueue_OK(t *testing.T) {
	db := newMCTestDB()
	sc := newMCScheduler(db, 1)
	h := &handlers.MulticastHandler{Scheduler: sc, DB: db}

	body, _ := json.Marshal(handlers.MulticastEnqueueRequest{
		ImageID: "img-abc",
		NodeID:  "node-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multicast/enqueue", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Enqueue(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp handlers.MulticastEnqueueResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty session_id")
	}
}

func TestMulticastEnqueue_MissingImageID(t *testing.T) {
	db := newMCTestDB()
	sc := newMCScheduler(db, 1)
	h := &handlers.MulticastHandler{Scheduler: sc, DB: db}

	body, _ := json.Marshal(handlers.MulticastEnqueueRequest{NodeID: "node-1"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multicast/enqueue", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.Enqueue(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestMulticastWait_ReturnsAcceptedWhileStaging(t *testing.T) {
	db := newMCTestDB()
	cfg := multicast.DefaultConfig()
	cfg.Threshold = 99 // won't fire during 5s poll
	cfg.WindowSeconds = 60
	sc := newMCSchedulerWithCfg(db, cfg)

	ctx := context.Background()
	sessionID, _ := sc.Enqueue(ctx, multicast.EnqueueRequest{ImageID: "img-wait", NodeID: "node-w"})

	h := &handlers.MulticastHandler{Scheduler: sc, DB: db}
	r := chi.NewRouter()
	r.Get("/api/v1/multicast/sessions/{id}/wait", h.Wait)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multicast/sessions/"+sessionID+"/wait?node_id=node-w", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// Session still staging when the 5s poll timeout hits → 202.
	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 while staging, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 202")
	}
}

func TestMulticastOutcome_OK(t *testing.T) {
	db := newMCTestDB()
	sc := newMCScheduler(db, 1)
	h := &handlers.MulticastHandler{Scheduler: sc, DB: db}

	ctx := context.Background()
	sessionID, _ := sc.Enqueue(ctx, multicast.EnqueueRequest{
		ImageID: "img-out", NodeID: "node-o", ForceImmediate: true,
	})

	r := chi.NewRouter()
	r.Post("/api/v1/multicast/sessions/{id}/members/{node_id}/outcome", h.RecordOutcome)

	body, _ := json.Marshal(handlers.MulticastOutcomeRequest{Outcome: "success"})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/multicast/sessions/"+sessionID+"/members/node-o/outcome",
		bytes.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// newMCSchedulerWithCfg builds a Scheduler with a specific Config.
func newMCSchedulerWithCfg(db *mcTestDB, cfg multicast.Config) *multicast.Scheduler {
	sender := func(_ context.Context, _ multicast.Session) error { return nil }
	sc := multicast.NewScheduler(db, sender, "http://localhost:8080")
	sc.SetConfig(cfg)
	return sc
}
