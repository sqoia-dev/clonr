package multicast

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeDB is an in-memory DB implementation for scheduler tests.
type fakeDB struct {
	mu       sync.Mutex
	sessions map[string]Session
	members  map[string][]Member
	cfg      Config
}

func newFakeDB() *fakeDB {
	return &fakeDB{
		sessions: make(map[string]Session),
		members:  make(map[string][]Member),
		cfg:      DefaultConfig(),
	}
}

func (f *fakeDB) MulticastInsertSession(_ context.Context, s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeDB) MulticastUpdateSessionState(_ context.Context, id string, state State, extra SessionUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return nil
	}
	s.State = state
	if extra.TransmitStartedAt != nil {
		s.TransmitStartedAt = extra.TransmitStartedAt
	}
	if extra.CompletedAt != nil {
		s.CompletedAt = extra.CompletedAt
	}
	if extra.Error != "" {
		s.Error = extra.Error
	}
	if extra.MemberCount != nil {
		s.MemberCount = *extra.MemberCount
	}
	if extra.SuccessCount != nil {
		s.SuccessCount = *extra.SuccessCount
	}
	f.sessions[id] = s
	return nil
}

func (f *fakeDB) MulticastInsertMember(_ context.Context, m Member) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[m.SessionID] = append(f.members[m.SessionID], m)
	return nil
}

func (f *fakeDB) MulticastUpdateMember(_ context.Context, sessionID, nodeID string, u MemberUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	members := f.members[sessionID]
	for i, m := range members {
		if m.NodeID == nodeID {
			if u.NotifiedAt != nil {
				members[i].NotifiedAt = u.NotifiedAt
			}
			if u.FinishedAt != nil {
				members[i].FinishedAt = u.FinishedAt
			}
			if u.Outcome != "" {
				members[i].Outcome = u.Outcome
			}
		}
	}
	f.members[sessionID] = members
	return nil
}

func (f *fakeDB) MulticastListActive(_ context.Context) ([]Session, error) {
	return nil, nil
}

func (f *fakeDB) MulticastGetConfig(_ context.Context) (Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg, nil
}

// noopSender returns nil immediately.
func noopSender(_ context.Context, _ Session) error {
	return nil
}

// TestSchedulerBatching verifies that two Enqueue calls for the same
// (image_id, layout_id) attach to the same session rather than creating two.
func TestSchedulerBatching(t *testing.T) {
	db := newFakeDB()
	cfg := DefaultConfig()
	cfg.Threshold = 1

	sc := NewScheduler(db, noopSender, "http://localhost:8080")
	sc.SetConfig(cfg)

	ctx := context.Background()
	sid1, err := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-1", NodeID: "node-a"})
	if err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	sid2, err := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-1", NodeID: "node-b"})
	if err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}
	if sid1 != sid2 {
		t.Errorf("expected both nodes to share session %q, got %q", sid1, sid2)
	}

	sc.mu.Lock()
	entry, ok := sc.sessions[sid1]
	sc.mu.Unlock()
	if !ok {
		t.Fatalf("session %q not found in scheduler map", sid1)
	}
	if entry.MemberCount != 2 {
		t.Errorf("expected MemberCount=2, got %d", entry.MemberCount)
	}
}

// TestSchedulerDifferentImageNewSession verifies that two Enqueue calls for
// different image IDs create separate sessions.
func TestSchedulerDifferentImageNewSession(t *testing.T) {
	db := newFakeDB()
	cfg := DefaultConfig()
	cfg.Threshold = 1
	sc := NewScheduler(db, noopSender, "http://localhost:8080")
	sc.SetConfig(cfg)

	ctx := context.Background()
	sid1, _ := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-1", NodeID: "node-a"})
	sid2, _ := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-2", NodeID: "node-b"})
	if sid1 == sid2 {
		t.Errorf("expected different sessions for different images, got same %q", sid1)
	}
}

// TestSchedulerWindowExpiryAndTransmit verifies the state machine transitions
// staging → transmitting → complete using synctest for deterministic timers.
func TestSchedulerWindowExpiryAndTransmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := newFakeDB()
		cfg := DefaultConfig()
		cfg.WindowSeconds = 5
		cfg.Threshold = 1

		doneCh := make(chan struct{})
		sender := func(_ context.Context, s Session) error {
			close(doneCh)
			return nil
		}

		sc := NewScheduler(db, sender, "http://localhost:8080")
		sc.SetConfig(cfg)

		ctx := context.Background()
		sessionID, err := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-fire", NodeID: "node-x"})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		// Advance fake time past the 5s window.
		time.Sleep(6 * time.Second)
		synctest.Wait()

		// Sender goroutine should have been called.
		select {
		case <-doneCh:
		default:
			t.Fatal("sender not called after window expiry")
		}

		// Session should be cleaned up from in-memory map.
		synctest.Wait()
		sc.mu.Lock()
		_, still := sc.sessions[sessionID]
		sc.mu.Unlock()
		if still {
			t.Errorf("session %q still in memory after completion", sessionID)
		}

		// DB record should be complete.
		db.mu.Lock()
		s := db.sessions[sessionID]
		db.mu.Unlock()
		if s.State != StateComplete {
			t.Errorf("expected state=complete in DB, got %q", s.State)
		}
	})
}

// TestSchedulerBelowThresholdFallback verifies that a session with fewer
// members than the threshold falls back to unicast at fire time.
func TestSchedulerBelowThresholdFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := newFakeDB()
		cfg := DefaultConfig()
		cfg.WindowSeconds = 5
		cfg.Threshold = 3 // require 3 nodes; only 1 will join

		senderCalled := false
		sender := func(_ context.Context, s Session) error {
			senderCalled = true
			return nil
		}

		sc := NewScheduler(db, sender, "http://localhost:8080")
		sc.SetConfig(cfg)

		ctx := context.Background()
		sessionID, err := sc.Enqueue(ctx, EnqueueRequest{ImageID: "img-thresh", NodeID: "node-alone"})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		waitResult := make(chan WaitResult, 1)
		go func() {
			res, _ := sc.Wait(context.Background(), sessionID, "node-alone")
			waitResult <- res
		}()

		// Advance past the window.
		time.Sleep(6 * time.Second)
		synctest.Wait()

		select {
		case res := <-waitResult:
			if !res.Fallback {
				t.Errorf("expected Fallback=true when below threshold")
			}
		default:
			t.Fatal("Wait did not return after window expiry")
		}

		if senderCalled {
			t.Errorf("sender should not be called when below threshold")
		}
	})
}

// TestSchedulerWaitDescriptor verifies that Wait returns a valid descriptor
// when ForceImmediate=true fires the session immediately.
func TestSchedulerWaitDescriptor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := newFakeDB()
		cfg := DefaultConfig()
		cfg.WindowSeconds = 60
		cfg.Threshold = 1

		sender := func(_ context.Context, s Session) error {
			return nil
		}

		sc := NewScheduler(db, sender, "http://localhost:8080")
		sc.SetConfig(cfg)

		ctx := context.Background()
		sessionID, err := sc.Enqueue(ctx, EnqueueRequest{
			ImageID:        "img-imm",
			NodeID:         "node-y",
			ForceImmediate: true,
		})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		waitResult := make(chan WaitResult, 1)
		go func() {
			res, err := sc.Wait(context.Background(), sessionID, "node-y")
			if err != nil {
				t.Errorf("Wait error: %v", err)
			}
			waitResult <- res
		}()

		// Allow the window goroutine to fire (ForceImmediate → delay=0).
		synctest.Wait()

		select {
		case result := <-waitResult:
			if result.Fallback {
				t.Error("expected Fallback=false when sender succeeds")
			}
			if result.Descriptor == nil {
				t.Fatal("expected non-nil Descriptor")
			}
			if result.Descriptor.SessionID != sessionID {
				t.Errorf("descriptor session_id mismatch: want %q got %q", sessionID, result.Descriptor.SessionID)
			}
			if result.Descriptor.MulticastGroup == "" {
				t.Error("expected non-empty multicast group")
			}
		default:
			t.Fatal("Wait did not return after ForceImmediate session fired")
		}
	})
}

// TestSchedulerUnknownSessionFallback verifies that Wait with a nonexistent
// session ID immediately returns Fallback=true.
func TestSchedulerUnknownSessionFallback(t *testing.T) {
	db := newFakeDB()
	sc := NewScheduler(db, noopSender, "http://localhost:8080")

	result, err := sc.Wait(context.Background(), "nonexistent-id", "node-z")
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !result.Fallback {
		t.Error("expected Fallback=true for unknown session ID")
	}
}
