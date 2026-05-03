package multicast

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// DB is the subset of the clustr DB interface required by the scheduler.
// Using an interface keeps the scheduler testable without the real DB.
type DB interface {
	// MulticastInsertSession persists a new session row in state=staging.
	MulticastInsertSession(ctx context.Context, s Session) error
	// MulticastUpdateSessionState persists a state transition.
	MulticastUpdateSessionState(ctx context.Context, id string, state State, extra SessionUpdate) error
	// MulticastInsertMember adds a member row for a node joining a session.
	MulticastInsertMember(ctx context.Context, m Member) error
	// MulticastUpdateMember records per-node outcome.
	MulticastUpdateMember(ctx context.Context, sessionID, nodeID string, u MemberUpdate) error
	// MulticastListActive returns all non-terminal sessions for scheduler recovery.
	MulticastListActive(ctx context.Context) ([]Session, error)
	// MulticastGetConfig reads the multicast_config table into a Config.
	MulticastGetConfig(ctx context.Context) (Config, error)
}

// SessionUpdate carries optional fields set alongside a state change.
type SessionUpdate struct {
	TransmitStartedAt *time.Time
	CompletedAt       *time.Time
	Error             string
	MemberCount       *int
	SuccessCount      *int
}

// MemberUpdate carries fields written when a member's outcome is recorded.
type MemberUpdate struct {
	NotifiedAt *time.Time
	FinishedAt *time.Time
	Outcome    Outcome
}

// SenderFunc is the function signature the scheduler calls to actually transmit
// an image over multicast.  In production this is sender.Run; in tests it is a
// controllable stub.
type SenderFunc func(ctx context.Context, s Session) error

// Scheduler batches reimage requests within a time window and fires one
// udp-sender process per (image_id, layout_id) tuple.
//
// Thread safety: all exported methods are safe for concurrent use.
type Scheduler struct {
	db     DB
	sender SenderFunc
	ports  *portAlloc
	cfg    Config
	// serverURL is the base URL of clustr-serverd, used to build image_url in
	// session descriptors returned to nodes.
	serverURL string

	mu       sync.Mutex
	sessions map[string]*sessionEntry // session ID → entry
}

// sessionEntry is the in-memory representation of an active session, including
// the channels used to notify waiting nodes.
type sessionEntry struct {
	Session

	// members is the list of node IDs enrolled in this session.
	members []string

	// ready is closed by the scheduler goroutine when the session moves to
	// transmitting (descriptor available) or failed (fallback required).
	ready chan struct{}

	// descriptor is set before ready is closed on success.
	descriptor *SessionDescriptor
	// failed is set to true before ready is closed on sender failure.
	failed bool

	// cancelWindow cancels the window timer goroutine.
	cancelWindow context.CancelFunc
}

// NewScheduler creates a Scheduler with the given DB and sender.
// Call Start to begin background goroutines.
func NewScheduler(db DB, sender SenderFunc, serverURL string) *Scheduler {
	cfg := DefaultConfig()
	return &Scheduler{
		db:        db,
		sender:    sender,
		ports:     newPortAlloc(),
		cfg:       cfg,
		serverURL: serverURL,
		sessions:  make(map[string]*sessionEntry),
	}
}

// SetConfig replaces the runtime configuration.  Safe to call before Start.
// Used in tests to inject controlled Config values.
func (sc *Scheduler) SetConfig(cfg Config) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cfg = cfg
}

// Start loads the multicast config from DB and recovers any non-terminal
// sessions that were left behind by a previous serverd run (marking them
// failed so nodes fall back to unicast).
func (sc *Scheduler) Start(ctx context.Context) error {
	cfg, err := sc.db.MulticastGetConfig(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("multicast: could not load config, using defaults")
	} else {
		sc.mu.Lock()
		sc.cfg = cfg
		sc.mu.Unlock()
	}

	// Recover non-terminal sessions: mark them failed so any nodes still
	// polling will get the fallback-unicast signal on the next wait request.
	active, err := sc.db.MulticastListActive(ctx)
	if err != nil {
		return fmt.Errorf("multicast: recover active sessions: %w", err)
	}
	for _, s := range active {
		log.Warn().
			Str("session_id", s.ID).
			Str("state", string(s.State)).
			Msg("multicast: marking orphaned session failed on startup")
		errMsg := "serverd restarted while session was in-flight"
		_ = sc.db.MulticastUpdateSessionState(ctx, s.ID, StateFailed, SessionUpdate{Error: errMsg})
	}
	return nil
}

// Enqueue enrolls a node in a multicast session for the given (image_id, layout_id).
//
// If a staging session already exists for this tuple and has not yet fired, the
// node is attached to it.  Otherwise a new session is created.
//
// Returns the session ID the node was enrolled in.
func (sc *Scheduler) Enqueue(ctx context.Context, req EnqueueRequest) (string, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Look for a compatible staging session.
	for _, entry := range sc.sessions {
		if entry.State == StateStaging &&
			entry.ImageID == req.ImageID &&
			entry.LayoutID == req.LayoutID {
			// Attach this node.
			if err := sc.attachMember(ctx, entry, req.NodeID); err != nil {
				return "", err
			}
			return entry.ID, nil
		}
	}

	// No compatible session found; create one.
	return sc.createSession(ctx, req)
}

// createSession creates a new staging session and starts the window timer.
// Caller must hold sc.mu.
func (sc *Scheduler) createSession(ctx context.Context, req EnqueueRequest) (string, error) {
	sessionID := uuid.New().String()
	group, err := groupForSession(sessionID, sc.cfg.GroupBase)
	if err != nil {
		return "", fmt.Errorf("multicast: group for session: %w", err)
	}
	port, err := sc.ports.Acquire(sessionID)
	if err != nil {
		return "", fmt.Errorf("multicast: acquire port: %w", err)
	}

	now := time.Now()
	windowDur := time.Duration(sc.cfg.WindowSeconds) * time.Second
	fireAt := now.Add(windowDur)
	if req.ForceImmediate {
		fireAt = now
	}

	s := Session{
		ID:             sessionID,
		ImageID:        req.ImageID,
		LayoutID:       req.LayoutID,
		State:          StateStaging,
		MulticastGroup: group,
		SenderPort:     port,
		RateBPS:        sc.cfg.RateBPS,
		StartedAt:      now,
		FireAt:         fireAt,
		MemberCount:    1,
	}
	if err := sc.db.MulticastInsertSession(ctx, s); err != nil {
		sc.ports.Release(port)
		return "", fmt.Errorf("multicast: insert session: %w", err)
	}

	winCtx, cancelWindow := context.WithCancel(context.Background())
	entry := &sessionEntry{
		Session:      s,
		members:      []string{req.NodeID},
		ready:        make(chan struct{}),
		cancelWindow: cancelWindow,
	}
	sc.sessions[sessionID] = entry

	// Persist the first member.
	if err := sc.db.MulticastInsertMember(ctx, Member{
		SessionID: sessionID,
		NodeID:    req.NodeID,
		JoinedAt:  now,
	}); err != nil {
		log.Warn().Err(err).Str("session_id", sessionID).Str("node_id", req.NodeID).
			Msg("multicast: insert member failed (non-fatal)")
	}

	log.Info().
		Str("session_id", sessionID).
		Str("image_id", req.ImageID).
		Str("layout_id", req.LayoutID).
		Str("first_node", req.NodeID).
		Time("fire_at", fireAt).
		Msg("multicast: new session created")

	go sc.runWindow(winCtx, entry)
	return sessionID, nil
}

// attachMember adds nodeID to an existing staging session entry.
// Caller must hold sc.mu.
func (sc *Scheduler) attachMember(ctx context.Context, entry *sessionEntry, nodeID string) error {
	entry.members = append(entry.members, nodeID)
	entry.MemberCount++
	now := time.Now()

	mc := entry.MemberCount
	_ = sc.db.MulticastUpdateSessionState(ctx, entry.ID, StateStaging, SessionUpdate{MemberCount: &mc})
	if err := sc.db.MulticastInsertMember(ctx, Member{
		SessionID: entry.ID,
		NodeID:    nodeID,
		JoinedAt:  now,
	}); err != nil {
		log.Warn().Err(err).Str("session_id", entry.ID).Str("node_id", nodeID).
			Msg("multicast: insert member failed (non-fatal)")
	}

	log.Info().
		Str("session_id", entry.ID).
		Str("node_id", nodeID).
		Int("member_count", entry.MemberCount).
		Msg("multicast: node attached to existing staging session")
	return nil
}

// runWindow waits until FireAt, then fires the session.
func (sc *Scheduler) runWindow(ctx context.Context, entry *sessionEntry) {
	delay := time.Until(entry.FireAt)
	if delay > 0 {
		select {
		case <-ctx.Done():
			return // session was cancelled
		case <-time.After(delay):
		}
	}

	sc.mu.Lock()
	// Re-read from the map; session may have been removed if cancelled.
	live, ok := sc.sessions[entry.ID]
	if !ok {
		sc.mu.Unlock()
		return
	}
	// Check threshold: if below threshold and no ForceImmediate, fall back to unicast.
	if live.MemberCount < sc.cfg.Threshold {
		log.Info().
			Str("session_id", live.ID).
			Int("member_count", live.MemberCount).
			Int("threshold", sc.cfg.Threshold).
			Msg("multicast: below threshold at fire time — falling back to unicast")
		live.failed = true
		close(live.ready)
		sc.mu.Unlock()
		sc.cleanupSession(live, StateFailed, "below threshold at fire time")
		return
	}

	live.State = StateTransmitting
	now := time.Now()
	live.TransmitStartedAt = &now
	sc.mu.Unlock()

	bgCtx := context.Background()
	if err := sc.db.MulticastUpdateSessionState(bgCtx, live.ID, StateTransmitting, SessionUpdate{
		TransmitStartedAt: &now,
	}); err != nil {
		log.Error().Err(err).Str("session_id", live.ID).Msg("multicast: persist transmitting state")
	}

	// Build the descriptor now so Wait() can return it immediately.
	desc := &SessionDescriptor{
		SessionID:      live.ID,
		MulticastGroup: live.MulticastGroup,
		SenderPort:     live.SenderPort,
		RateBPS:        live.RateBPS,
		ImageURL:       live.buildImageURL(sc.serverURL),
		LayoutID:       live.LayoutID,
	}
	sc.mu.Lock()
	live.descriptor = desc
	sc.mu.Unlock()

	log.Info().
		Str("session_id", live.ID).
		Str("group", live.MulticastGroup).
		Int("port", live.SenderPort).
		Int("members", live.MemberCount).
		Msg("multicast: session fired — invoking sender")

	// Close ready so waiting nodes get the descriptor and can start udp-receiver.
	close(live.ready)

	// Run the sender.  In Commit 1 this is a stub; replaced in Commit 2.
	sendErr := sc.sender(bgCtx, live.Session)
	if sendErr != nil {
		log.Error().Err(sendErr).Str("session_id", live.ID).Msg("multicast: sender failed")
		sc.cleanupSession(live, StateFailed, sendErr.Error())
		return
	}

	sc.cleanupSession(live, StateComplete, "")
}

// cleanupSession persists a terminal state and removes the session from
// the in-memory map.
func (sc *Scheduler) cleanupSession(entry *sessionEntry, state State, errMsg string) {
	entry.cancelWindow()
	sc.ports.Release(entry.SenderPort)

	sc.mu.Lock()
	delete(sc.sessions, entry.ID)
	sc.mu.Unlock()

	now := time.Now()
	_ = sc.db.MulticastUpdateSessionState(context.Background(), entry.ID, state, SessionUpdate{
		CompletedAt: &now,
		Error:       errMsg,
	})
	log.Info().
		Str("session_id", entry.ID).
		Str("final_state", string(state)).
		Msg("multicast: session complete")
}

// Wait blocks until the session transitions out of staging (descriptor
// available or fallback required), or ctx is cancelled.
//
// Returns immediately if the session is already in a terminal or transmitting
// state.  Returns WaitResult{Fallback: true} for unknown session IDs — these
// are sessions that completed before the node joined, or sessions that were
// cleaned up on restart.
func (sc *Scheduler) Wait(ctx context.Context, sessionID, nodeID string) (WaitResult, error) {
	sc.mu.Lock()
	entry, ok := sc.sessions[sessionID]
	if !ok {
		sc.mu.Unlock()
		// Unknown session: tell the node to fall back to unicast.
		return WaitResult{Fallback: true}, nil
	}
	ready := entry.ready
	sc.mu.Unlock()

	select {
	case <-ctx.Done():
		return WaitResult{}, ctx.Err()
	case <-ready:
	}

	sc.mu.Lock()
	failed := entry.failed
	desc := entry.descriptor
	sc.mu.Unlock()

	if failed {
		return WaitResult{Fallback: true}, nil
	}
	// Record notified_at for this member.
	now := time.Now()
	_ = sc.db.MulticastUpdateMember(context.Background(), sessionID, nodeID, MemberUpdate{
		NotifiedAt: &now,
	})
	return WaitResult{Descriptor: desc}, nil
}

// RecordOutcome persists the per-node outcome after udp-receiver exits.
// Called by the outcome endpoint handler.
func (sc *Scheduler) RecordOutcome(ctx context.Context, sessionID, nodeID string, outcome Outcome) error {
	now := time.Now()
	if err := sc.db.MulticastUpdateMember(ctx, sessionID, nodeID, MemberUpdate{
		FinishedAt: &now,
		Outcome:    outcome,
	}); err != nil {
		return fmt.Errorf("multicast: record outcome: %w", err)
	}

	// If any member reports failure, transition the session to partial.
	if outcome == OutcomeFailed || outcome == OutcomeFellbackUnicast {
		_ = sc.db.MulticastUpdateSessionState(ctx, sessionID, StatePartial, SessionUpdate{
			Error: fmt.Sprintf("node %s reported outcome=%s", nodeID, outcome),
		})
	}
	return nil
}

// buildImageURL constructs the blob URL for the image in this session.
func (entry *sessionEntry) buildImageURL(serverURL string) string {
	return fmt.Sprintf("%s/api/v1/images/%s/blob", serverURL, entry.ImageID)
}
