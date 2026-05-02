// Package multicast implements the server-side UDPCast multicast scheduler for
// clustr fleet reimages (#157).
//
// The scheduler batches reimage requests that share the same (image_id,
// layout_id) tuple within a configurable time window (default 60s), then fires
// a single udp-sender process that multicasts the image to all enrolled nodes
// simultaneously.  Nodes poll GET /api/v1/multicast/sessions/wait while the
// window is open; the scheduler returns the session descriptor once the sender
// is ready.
//
// Architecture summary (per Richard's design doc SPRINT-25-UDPCAST-DESIGN.md):
//
//	D1  — sender lives in serverd, fork/execs /usr/bin/udp-sender per session.
//	D2  — receiver discovery via kernel cmdline + iPXE menu "reimage-fleet".
//	D3  — state machine: staging → transmitting → complete | failed | partial.
//	D4  — multicast_sessions + multicast_session_members in SQLite (migration 093).
//	D5  — single global rate setting from multicast_config.rate_bps.
//	D6  — sender failure → all members fallback to unicast; partial ok.
//	D7  — CLI flag --multicast=auto|off|require on clustr deploy.
package multicast

import "time"

// State is the state of a multicast session.
type State string

const (
	StateStaging      State = "staging"
	StateTransmitting State = "transmitting"
	StateComplete     State = "complete"
	StateFailed       State = "failed"
	StatePartial      State = "partial"
)

// Outcome is the per-member result recorded after udp-receiver exits.
type Outcome string

const (
	OutcomeSuccess         Outcome = "success"
	OutcomeFailed          Outcome = "failed"
	OutcomeFellbackUnicast Outcome = "fellback_unicast"
)

// Session is the in-memory representation of a multicast_sessions row.
// The scheduler owns the canonical in-memory copy; DB is the source of truth
// for restarts (scheduler reconstructs from non-terminal rows on boot).
type Session struct {
	ID             string
	ImageID        string
	LayoutID       string // empty if no layout override
	State          State
	MulticastGroup string
	SenderPort     int
	RateBPS        int64
	StartedAt      time.Time
	FireAt         time.Time // StartedAt + window, or override for --multicast=require
	TransmitStartedAt *time.Time
	CompletedAt       *time.Time
	Error             string
	MemberCount    int
	SuccessCount   int
}

// Member is the in-memory representation of a multicast_session_members row.
type Member struct {
	SessionID  string
	NodeID     string
	JoinedAt   time.Time
	NotifiedAt *time.Time
	FinishedAt *time.Time
	Outcome    Outcome
}

// EnqueueRequest is the input to Scheduler.Enqueue.
type EnqueueRequest struct {
	ImageID  string
	LayoutID string // empty if no layout override
	NodeID   string
	// ForceImmediate, when true, fires the session immediately regardless of
	// the batching window (used for --multicast=require with single node when
	// threshold=1).
	ForceImmediate bool
}

// WaitResult is the output of Scheduler.Wait.
type WaitResult struct {
	// Descriptor is set when the session moves to transmitting.
	// The node uses this to invoke udp-receiver.
	Descriptor *SessionDescriptor
	// Fallback, when true, tells the node to fall back to unicast HTTP fetch.
	// Set when the sender fails or the session transitions to failed.
	Fallback bool
}

// SessionDescriptor carries the multicast stream parameters returned to each
// enrolled node after the batching window expires and the sender is ready.
type SessionDescriptor struct {
	SessionID      string `json:"session_id"`
	MulticastGroup string `json:"multicast_group"`
	SenderPort     int    `json:"sender_port"`
	RateBPS        int64  `json:"rate_bps"`
	ImageURL       string `json:"image_url"`
	LayoutID       string `json:"layout_id,omitempty"`
}

// Config holds the runtime configuration for the scheduler.
// Values are loaded from the multicast_config table and refreshed periodically.
type Config struct {
	Enabled       bool
	WindowSeconds int
	Threshold     int   // minimum members before firing; 0 or 1 = fire immediately
	RateBPS       int64
	GroupBase     string // e.g. "239.255.42.0"
}

// DefaultConfig returns the default multicast configuration matching
// the values seeded into multicast_config by migration 093.
func DefaultConfig() Config {
	return Config{
		Enabled:       true,
		WindowSeconds: 60,
		Threshold:     2,
		RateBPS:       100_000_000,
		GroupBase:     "239.255.42.0",
	}
}
