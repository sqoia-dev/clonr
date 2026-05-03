package server

// UX-4: LogBroker also bridges node-heartbeat component logs to the multiplexed
// event bus as TopicNodes events so the unified /api/v1/events stream carries
// node liveness signals.

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/server/eventbus"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// LogBroker is a simple in-process pub/sub bus for log entries.
// POST /api/v1/logs publishes to it; GET /api/v1/logs/stream subscribes.
type LogBroker struct {
	mu          sync.RWMutex
	subscribers map[string]*logSubscriber
	// dropped counts entries silently discarded due to full subscriber buffers.
	// Incremented atomically; read via Dropped().
	dropped uint64

	// bus, when set, receives a TopicNodes event for every node-heartbeat log
	// entry. This bridges the log stream into the multiplexed /api/v1/events SSE.
	bus *eventbus.Bus
}

// SetBus wires the multiplexed event bus into the log broker.
func (b *LogBroker) SetBus(eb *eventbus.Bus) {
	b.mu.Lock()
	b.bus = eb
	b.mu.Unlock()
}

type logSubscriber struct {
	filter api.LogFilter
	ch     chan api.LogEntry
}

// NewLogBroker creates an initialised LogBroker.
func NewLogBroker() *LogBroker {
	return &LogBroker{
		subscribers: make(map[string]*logSubscriber),
	}
}

// Subscribe registers a new subscriber with an optional filter.
// Returns a unique ID, a read-only channel of matching log entries, and a
// cancel function that removes the subscription and closes the channel.
func (b *LogBroker) Subscribe(filter api.LogFilter) (id string, ch <-chan api.LogEntry, cancel func()) {
	id = uuid.New().String()
	sub := &logSubscriber{
		filter: filter,
		ch:     make(chan api.LogEntry, 64), // buffered so Publish never blocks
	}

	b.mu.Lock()
	b.subscribers[id] = sub
	b.mu.Unlock()

	cancel = func() {
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
		close(sub.ch)
	}
	return id, sub.ch, cancel
}

// Dropped returns the total number of log entries dropped due to full
// subscriber buffers since the broker was created.
func (b *LogBroker) Dropped() uint64 {
	return atomic.LoadUint64(&b.dropped)
}

// Publish fans out entries to all subscribers whose filter matches.
// It never blocks — entries are dropped for a subscriber whose buffer is full.
// Drops are counted; a warning is logged every 100th drop to avoid log spam.
//
// UX-4: node-heartbeat entries also emit a TopicNodes event on the event bus
// so the unified /api/v1/events SSE stream carries node liveness signals.
func (b *LogBroker) Publish(entries []api.LogEntry) {
	b.mu.RLock()
	bus := b.bus
	defer b.mu.RUnlock()

	for _, entry := range entries {
		for _, sub := range b.subscribers {
			if !matchesFilter(entry, sub.filter) {
				continue
			}
			// Non-blocking send: slow consumers are dropped, not blocked.
			select {
			case sub.ch <- entry:
			default:
				n := atomic.AddUint64(&b.dropped, 1)
				// Log a warning every 100th drop to surface persistent slowness
				// without flooding the log when a subscriber falls far behind.
				if n%100 == 0 {
					log.Warn().Uint64("total_dropped", n).
						Msg("logbroker: subscriber buffer full — entries dropped (logged every 100th drop)")
				}
			}
		}
		// UX-4: bridge node-heartbeat logs to the multiplexed event bus so the
		// unified /api/v1/events stream replaces the per-page heartbeat SSE.
		if bus != nil && entry.Component == "node-heartbeat" {
			bus.Publish(eventbus.TopicNodes, map[string]string{
				"node_mac": entry.NodeMAC,
				"hostname": entry.Hostname,
			})
		}
	}
}

// matchesFilter returns true if entry satisfies all non-empty filter fields.
func matchesFilter(e api.LogEntry, f api.LogFilter) bool {
	if f.NodeMAC != "" && e.NodeMAC != f.NodeMAC {
		return false
	}
	if f.Hostname != "" && e.Hostname != f.Hostname {
		return false
	}
	if f.Level != "" && e.Level != f.Level {
		return false
	}
	if f.Component != "" && e.Component != f.Component {
		return false
	}
	if f.Since != nil && e.Timestamp.Before(*f.Since) {
		return false
	}
	return true
}
