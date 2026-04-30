package server

// group_reimage_events.go — fan-out store for group reimage SSE events.
//
// GroupReimageEventStore publishes per-node and job-level reimage progress
// events to SSE subscribers. Mirrors the shape of ImageEventStore.

import (
	"sync"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// GroupReimageEventStore fans group reimage events out to SSE subscribers.
// It is safe for concurrent use.
type GroupReimageEventStore struct {
	subsMu      sync.RWMutex
	subscribers map[string]chan api.GroupReimageEvent
}

// NewGroupReimageEventStore creates a ready-to-use GroupReimageEventStore.
func NewGroupReimageEventStore() *GroupReimageEventStore {
	return &GroupReimageEventStore{
		subscribers: make(map[string]chan api.GroupReimageEvent),
	}
}

// Publish sends an event to all current subscribers.
// Non-blocking: slow consumers are dropped rather than blocking the caller.
func (s *GroupReimageEventStore) Publish(event api.GroupReimageEvent) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// Subscribe registers a new SSE subscriber. Returns a read-only event channel
// and a cancel function that unregisters the subscriber and closes the channel.
func (s *GroupReimageEventStore) Subscribe() (ch <-chan api.GroupReimageEvent, cancel func()) {
	id := uuid.New().String()
	internal := make(chan api.GroupReimageEvent, 128)

	s.subsMu.Lock()
	s.subscribers[id] = internal
	s.subsMu.Unlock()

	cancel = func() {
		s.subsMu.Lock()
		delete(s.subscribers, id)
		s.subsMu.Unlock()
		close(internal)
	}
	return internal, cancel
}
