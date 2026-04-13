package server

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clonr/pkg/api"
)

// BuildPhase re-exports the api constants for use in server-internal code.
// The canonical type and constants are defined in pkg/api.
const (
	PhaseDownloadingISO   = api.BuildPhaseDownloadingISO
	PhaseGeneratingConfig = api.BuildPhaseGeneratingConfig
	PhaseCreatingDisk     = api.BuildPhaseCreatingDisk
	PhaseLaunchingVM      = api.BuildPhaseLaunchingVM
	PhaseInstalling       = api.BuildPhaseInstalling
	PhaseExtracting       = api.BuildPhaseExtracting
	PhaseScrubbing        = api.BuildPhaseScrubbing
	PhaseFinalizing       = api.BuildPhaseFinalizing
	PhaseComplete         = api.BuildPhaseComplete
	PhaseFailed           = api.BuildPhaseFailed
	PhaseCanceled         = api.BuildPhaseCanceled
)

// serialRing is a fixed-capacity ring buffer for serial log lines.
type serialRing struct {
	buf []string
	cap int
	n   int
	pos int
}

func newSerialRing(capacity int) *serialRing {
	return &serialRing{buf: make([]string, capacity), cap: capacity}
}

func (r *serialRing) push(line string) {
	r.buf[r.pos%r.cap] = line
	r.pos++
	if r.n < r.cap {
		r.n++
	}
}

// snapshot returns all lines in insertion order.
func (r *serialRing) snapshot() []string {
	if r.n == 0 {
		return nil
	}
	out := make([]string, r.n)
	if r.n < r.cap {
		copy(out, r.buf[:r.n])
	} else {
		// Ring has wrapped: oldest entry is at pos%cap.
		start := r.pos % r.cap
		for i := 0; i < r.cap; i++ {
			out[i] = r.buf[(start+i)%r.cap]
		}
	}
	return out
}

// buildStateInternal is the mutable internal record (not exported over the wire).
type buildStateInternal struct {
	state      api.BuildState
	serialRing *serialRing
	stderrRing *serialRing
}

// BuildProgressStore tracks in-flight and recently completed ISO builds.
// It is safe for concurrent use from multiple goroutines.
type BuildProgressStore struct {
	mu      sync.RWMutex
	states  map[string]*buildStateInternal // keyed by image ID

	subsMu      sync.RWMutex
	subscribers map[string]chan api.BuildEvent
}

// NewBuildProgressStore creates a store and starts the background cleanup goroutine.
func NewBuildProgressStore() *BuildProgressStore {
	s := &BuildProgressStore{
		states:      make(map[string]*buildStateInternal),
		subscribers: make(map[string]chan api.BuildEvent),
	}
	go s.cleanupLoop()
	return s
}

// Start registers a new build for imageID, returning a *BuildHandle the factory uses
// to report progress. Calling Start for an imageID that already exists overwrites it.
func (s *BuildProgressStore) Start(imageID string) *BuildHandle {
	now := time.Now()
	internal := &buildStateInternal{
		state: api.BuildState{
			ImageID:   imageID,
			Phase:     PhaseDownloadingISO,
			StartedAt: now,
			UpdatedAt: now,
		},
		serialRing: newSerialRing(100),
		stderrRing: newSerialRing(50),
	}

	s.mu.Lock()
	s.states[imageID] = internal
	s.mu.Unlock()

	return &BuildHandle{store: s, imageID: imageID}
}

// Get returns a snapshot api.BuildState for imageID. Returns (zero, false) if not found.
func (s *BuildProgressStore) Get(imageID string) (api.BuildState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	internal, ok := s.states[imageID]
	if !ok {
		return api.BuildState{}, false
	}
	snap := internal.state
	snap.SerialTail = internal.serialRing.snapshot()
	snap.QEMUStderr = internal.stderrRing.snapshot()
	return snap, true
}

// Subscribe registers an SSE subscriber. Returns a read-only channel and a cancel func.
// The channel receives all BuildEvents for all image IDs; callers filter by ImageID.
func (s *BuildProgressStore) Subscribe() (<-chan api.BuildEvent, func()) {
	id := uuid.New().String()
	ch := make(chan api.BuildEvent, 256)

	s.subsMu.Lock()
	s.subscribers[id] = ch
	s.subsMu.Unlock()

	cancel := func() {
		s.subsMu.Lock()
		delete(s.subscribers, id)
		s.subsMu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// publish fans the event out to all subscribers; slow consumers are dropped.
func (s *BuildProgressStore) publish(ev api.BuildEvent) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

// setPhase updates the phase and publishes an event.
func (s *BuildProgressStore) setPhase(imageID, phase string) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.state.Phase = phase
		internal.state.UpdatedAt = time.Now()
		internal.state.ElapsedMS = time.Since(internal.state.StartedAt).Milliseconds()
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{
			ImageID:   imageID,
			Phase:     phase,
			ElapsedMS: internal.state.ElapsedMS,
		})
	}
}

// setProgress updates byte-level progress and publishes an event.
func (s *BuildProgressStore) setProgress(imageID string, done, total int64) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.state.BytesDone = done
		internal.state.BytesTotal = total
		internal.state.UpdatedAt = time.Now()
		internal.state.ElapsedMS = time.Since(internal.state.StartedAt).Milliseconds()
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{
			ImageID:    imageID,
			Phase:      internal.state.Phase,
			BytesDone:  done,
			BytesTotal: total,
			ElapsedMS:  internal.state.ElapsedMS,
		})
	}
}

// addSerialLine appends a line to the ring buffer and publishes a line event.
func (s *BuildProgressStore) addSerialLine(imageID, line string) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.serialRing.push(line)
		internal.state.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{ImageID: imageID, SerialLine: line})
	}
}

// addStderrLine appends a QEMU stderr line.
func (s *BuildProgressStore) addStderrLine(imageID, line string) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.stderrRing.push(line)
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{ImageID: imageID, StderrLine: line})
	}
}

// fail marks the build as failed with an error message.
func (s *BuildProgressStore) fail(imageID, msg string) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.state.Phase = PhaseFailed
		internal.state.ErrorMessage = msg
		internal.state.UpdatedAt = time.Now()
		internal.state.ElapsedMS = time.Since(internal.state.StartedAt).Milliseconds()
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{ImageID: imageID, Phase: PhaseFailed, Error: msg})
	}
}

// complete marks the build as done.
func (s *BuildProgressStore) complete(imageID string) {
	s.mu.Lock()
	internal, ok := s.states[imageID]
	if ok {
		internal.state.Phase = PhaseComplete
		internal.state.UpdatedAt = time.Now()
		internal.state.ElapsedMS = time.Since(internal.state.StartedAt).Milliseconds()
	}
	s.mu.Unlock()

	if ok {
		s.publish(api.BuildEvent{ImageID: imageID, Phase: PhaseComplete, ElapsedMS: internal.state.ElapsedMS})
	}
}

// cleanupLoop removes stale entries (terminal state, older than 2h) every 10 min.
func (s *BuildProgressStore) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-2 * time.Hour)
		s.mu.Lock()
		for id, internal := range s.states {
			phase := internal.state.Phase
			terminal := phase == PhaseComplete || phase == PhaseFailed || phase == PhaseCanceled
			if terminal && internal.state.UpdatedAt.Before(cutoff) {
				delete(s.states, id)
			}
		}
		s.mu.Unlock()
	}
}

// ─── BuildHandle ─────────────────────────────────────────────────────────────

// BuildHandle is the handle the Factory uses to report progress. It holds a
// reference to the store and the image ID so callers don't repeat themselves.
type BuildHandle struct {
	store   *BuildProgressStore
	imageID string
}

func (h *BuildHandle) SetPhase(phase string)      { h.store.setPhase(h.imageID, phase) }
func (h *BuildHandle) SetProgress(d, t int64)     { h.store.setProgress(h.imageID, d, t) }
func (h *BuildHandle) AddSerialLine(line string)   { h.store.addSerialLine(h.imageID, line) }
func (h *BuildHandle) AddStderrLine(line string)   { h.store.addStderrLine(h.imageID, line) }
func (h *BuildHandle) Fail(msg string)             { h.store.fail(h.imageID, msg) }
func (h *BuildHandle) Complete()                   { h.store.complete(h.imageID) }
func (h *BuildHandle) ImageID() string             { return h.imageID }
