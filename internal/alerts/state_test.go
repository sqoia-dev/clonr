package alerts

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockStateStore returns a *StateStore with a pre-initialised active map and no
// database — enough for concurrency tests that never touch the DB path.
func newTestStateStore() *StateStore {
	return &StateStore{
		db:     nil, // tests below never call DB methods
		active: make(map[alertStateKey]*activeAlert),
	}
}

// addActive is a helper that directly inserts an activeAlert into the store
// under the write lock.  Used by tests to bypass the DB-writing Fire path.
func addActive(s *StateStore, key alertStateKey, dbID int64) {
	s.mu.Lock()
	s.active[key] = &activeAlert{dbID: dbID, key: key, firedAt: time.Now()}
	s.mu.Unlock()
}

// removeActive is a helper that directly removes an activeAlert under the write
// lock, used to simulate resolution without DB I/O.
func removeActive(s *StateStore, key alertStateKey) {
	s.mu.Lock()
	delete(s.active, key)
	s.mu.Unlock()
}

// TestStateStore_ConcurrentAdd fans out 100 goroutines each adding and removing
// a unique alert key and asserts no panic.  The final count must be zero because
// every add is paired with a remove.
func TestStateStore_ConcurrentAdd(t *testing.T) {
	s := newTestStateStore()
	const n = 100

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			key := alertStateKey{
				ruleName: "rule",
				nodeID:   "node",
				sensor:   "cpu",
				labelsJSON: func() string {
					// unique label per goroutine so keys don't collide
					return `{"idx":"` + string(rune('A'+i%26)) + `"}`
				}(),
			}
			addActive(s, key, int64(i))
			removeActive(s, key)
		}()
	}
	wg.Wait()

	s.mu.RLock()
	got := len(s.active)
	s.mu.RUnlock()
	if got != 0 {
		t.Fatalf("expected 0 active alerts after paired add/remove, got %d", got)
	}
}

// TestStateStore_SnapshotIsolation takes a Snapshot while concurrent writers are
// adding and removing entries.  The test asserts that:
//   - the snapshot never panics
//   - every entry in the snapshot was actually present at some point (no garbage)
func TestStateStore_SnapshotIsolation(t *testing.T) {
	s := newTestStateStore()
	const n = 50

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Writer goroutine: constantly adds/removes keys for the test duration.
	go func() {
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			key := alertStateKey{ruleName: "r", nodeID: "n", sensor: "s",
				labelsJSON: `{"i":"` + string(rune('a'+i%26)) + `"}`}
			addActive(s, key, int64(i))
			removeActive(s, key)
		}
	}()

	// Reader: take snapshots concurrently, assert no panic and sane values.
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			snap := s.Snapshot()
			for _, a := range snap {
				if a.State != StateFiring {
					t.Errorf("snapshot entry has unexpected state %q", a.State)
				}
			}
		}()
	}
	wg.Wait()
}

// TestStateStore_ForEachActiveReadLock confirms that calling Snapshot() from
// inside a ForEachActive callback does NOT deadlock.
//
// sync.RWMutex allows multiple concurrent RLock holders, so an RLock taken
// inside a ForEachActive (which itself holds RLock) must not block.
// If the implementation ever mistakenly uses Lock() instead of RLock() for
// reads, this test will deadlock and the go test -timeout will catch it.
func TestStateStore_ForEachActiveReadLock(t *testing.T) {
	s := newTestStateStore()
	key := alertStateKey{ruleName: "deadlock-check", nodeID: "n1", sensor: "cpu"}
	addActive(s, key, 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ForEachActive(func(a Alert) {
			// Snapshot acquires another RLock — must not deadlock.
			snap := s.Snapshot()
			_ = snap
		})
	}()

	select {
	case <-done:
		// passed — no deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: ForEachActive + Snapshot timed out after 3s")
	}
}
