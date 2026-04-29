package server

// image_events.go — fan-out store for image lifecycle events (SSE-1).
//
// ImageEventStore publishes image lifecycle events (create, update, delete,
// ref-count change) to all SSE subscribers. It mirrors the shape of
// ProgressStore but carries api.ImageEvent payloads instead of DeployProgress.

import (
	"sync"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// ImageEventStore fans image lifecycle events out to SSE subscribers.
// It is safe for concurrent use.
type ImageEventStore struct {
	subsMu      sync.RWMutex
	subscribers map[string]chan api.ImageEvent
}

// NewImageEventStore creates a ready-to-use ImageEventStore.
func NewImageEventStore() *ImageEventStore {
	return &ImageEventStore{
		subscribers: make(map[string]chan api.ImageEvent),
	}
}

// Publish sends an event to all current subscribers.
// Non-blocking: slow consumers are dropped rather than blocking the caller.
func (s *ImageEventStore) Publish(event api.ImageEvent) {
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
func (s *ImageEventStore) Subscribe() (ch <-chan api.ImageEvent, cancel func()) {
	id := uuid.New().String()
	internal := make(chan api.ImageEvent, 64)

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
