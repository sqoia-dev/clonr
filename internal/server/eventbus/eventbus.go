// Package eventbus implements a typed multiplexed event bus for server-sent
// events (SSE). A single Bus instance receives published events from any
// internal producer (image lifecycle, node heartbeats, group reimage, build
// progress, etc.) and fans them out to all registered SSE subscribers.
//
// Each subscriber receives a filtered view: if it declared a topics filter, only
// matching topics are forwarded. An empty topic filter means "all topics".
//
// Publish is non-blocking — slow consumers are dropped rather than stalling the
// producer. Each subscriber channel is buffered (64 events) to absorb short
// bursts.
package eventbus

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

// Topic identifies the class of event. Callers use the Topic* constants.
type Topic string

const (
	TopicNodes    Topic = "nodes"
	TopicImages   Topic = "images"
	TopicProgress Topic = "progress"
	TopicAlerts   Topic = "alerts"
	TopicStats    Topic = "stats"
	TopicBundles  Topic = "bundles"
	TopicGroups   Topic = "groups"
	TopicPing     Topic = "ping"
)

// Event is a single bus event. Data is any JSON-serialisable value; it is
// serialised once by Publish and forwarded as raw bytes to all subscribers.
type Event struct {
	Topic Topic           `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

// subscriber is an internal per-connection subscription.
type subscriber struct {
	// topics is the set of topics this subscriber wants; empty == all topics.
	topics map[Topic]struct{}
	ch     chan Event
}

// Bus is the central event bus. Create with New; Publish from any goroutine.
// Subscribe / Unsubscribe are safe to call concurrently.
type Bus struct {
	mu   sync.RWMutex
	subs map[string]*subscriber
}

// New returns a ready Bus.
func New() *Bus {
	return &Bus{subs: make(map[string]*subscriber)}
}

// Publish encodes data as JSON and fans the event out to all matching subscribers.
// If data cannot be marshalled the event is silently dropped.
// This method is non-blocking; slow consumers lose events.
func (b *Bus) Publish(topic Topic, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	ev := Event{Topic: topic, Data: raw}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs {
		if len(sub.topics) > 0 {
			if _, ok := sub.topics[topic]; !ok {
				continue
			}
		}
		select {
		case sub.ch <- ev:
		default:
			// slow consumer — drop
		}
	}
}

// Subscribe registers a new subscriber interested in the given topics.
// Pass an empty slice to receive all topics. Returns a read-only channel and
// a cancel function; the caller must call cancel() when done.
func (b *Bus) Subscribe(topics []Topic) (<-chan Event, func()) {
	id := uuid.New().String()
	sub := &subscriber{
		topics: make(map[Topic]struct{}, len(topics)),
		ch:     make(chan Event, 64),
	}
	for _, t := range topics {
		sub.topics[t] = struct{}{}
	}

	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
		close(sub.ch)
	}
	return sub.ch, cancel
}
