package config

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	sysalerts "github.com/sqoia-dev/clustr/internal/server/alerts"
)

// coalesceWindow is the debounce duration between a Notify call and the
// actual Render dispatch. Founder-approved at 50 ms (see reactive-config.md §6).
const coalesceWindow = 50 * time.Millisecond

// renderTimeout is the per-Render hard deadline. A plugin that exceeds this
// is treated as a Render error (see reactive-config.md §11.2 Q4).
const renderTimeout = 250 * time.Millisecond

// dirtyEvent carries a set of changed config-tree paths and the cluster state
// snapshot to render against.
type dirtyEvent struct {
	changed []string
	state   ClusterState
}

// pluginQueue is the per-plugin coalesce queue.
//
// THREAD-SAFETY: mu guards all fields. The single render goroutine (run by
// start) is the only writer to the accumulated state; Notify is the only
// writer to pending. Both hold mu during mutation.
type pluginQueue struct {
	mu      sync.Mutex
	plugin  Plugin
	pending []dirtyEvent
	timer   *time.Timer
	// alerts is the system-alert store used to surface Render failures.
	// May be nil in tests that don't exercise the alert path.
	alerts AlertWriter
}

// AlertWriter is the subset of *sysalerts.Store that the observer uses.
// Defined as an interface so tests can inject a no-op implementation.
//
// THREAD-SAFETY: implementations must be safe for concurrent use.
type AlertWriter interface {
	// Set upserts a durable alert keyed by (key, device).
	Set(ctx context.Context, args sysalerts.SetArgs) (*sysalerts.SystemAlert, error)
	// Unset clears an active alert for (key, device).
	Unset(ctx context.Context, key, device string) (bool, error)
}

// ─── global registry ─────────────────────────────────────────────────────────

// registryMu guards plugins and watchIndex. All exported entry-points acquire
// this lock before reading or mutating either map.
//
// THREAD-SAFETY invariant: plugins and watchIndex are only written during
// Register (startup, single-goroutine). They are read-only thereafter,
// accessed without a lock from Notify. The Notify path acquires registryMu
// to get a stable snapshot of the per-queue pointers, then releases it before
// enqueuing — avoiding lock inversion with pluginQueue.mu.
var registryMu sync.RWMutex

// plugins holds all registered plugins keyed by Name().
var plugins = map[string]*pluginQueue{}

// watchIndex maps a config-tree path to the list of plugin queues that
// declared that path in WatchedKeys.
var watchIndex = map[string][]*pluginQueue{}

// globalAlerts is injected by SetAlertWriter and forwarded to every new queue.
var globalAlerts AlertWriter

// SetAlertWriter injects the system-alert store. Call this once at startup,
// before any Register calls, so every plugin queue shares the same writer.
func SetAlertWriter(aw AlertWriter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	globalAlerts = aw
}

// Register adds p to the observer registry and starts its coalesce goroutine.
// Must be called once per plugin at server startup, before any Notify calls.
// Calling Register with a duplicate Name() panics to catch programmer error.
func Register(p Plugin) {
	registryMu.Lock()
	defer registryMu.Unlock()

	name := p.Name()
	if _, dup := plugins[name]; dup {
		panic("config.Register: duplicate plugin name: " + name)
	}

	q := &pluginQueue{
		plugin: p,
		alerts: globalAlerts,
	}
	plugins[name] = q

	for _, key := range p.WatchedKeys() {
		watchIndex[key] = append(watchIndex[key], q)
	}
}

// Notify tells the observer that the given config-tree paths have changed,
// along with the latest cluster state. The observer fans out to every plugin
// whose WatchedKeys intersect changed, coalescing rapid calls within a 50 ms
// window.
//
// Notify is non-blocking: it enqueues the event and returns immediately.
// The actual Render dispatch happens asynchronously in each plugin's goroutine.
func Notify(changed []string, state ClusterState) {
	if len(changed) == 0 {
		return
	}

	// Build the set of affected queues from the watch index.
	registryMu.RLock()
	affected := make(map[*pluginQueue]struct{})
	for _, key := range changed {
		for _, q := range watchIndex[key] {
			affected[q] = struct{}{}
		}
	}
	registryMu.RUnlock()

	ev := dirtyEvent{changed: changed, state: state}
	for q := range affected {
		q.enqueue(ev)
	}
}

// Stop drains all in-flight coalesce timers and cancels pending renders. It
// is a best-effort shutdown — it does not wait for in-progress Render calls
// to complete. Pass the server context so renders can be cancelled.
func Stop() {
	registryMu.RLock()
	defer registryMu.RUnlock()

	for _, q := range plugins {
		q.mu.Lock()
		if q.timer != nil {
			q.timer.Stop()
			q.timer = nil
		}
		q.pending = nil
		q.mu.Unlock()
	}
}

// ─── per-plugin coalesce logic ────────────────────────────────────────────────

// enqueue appends ev to the queue and (re)starts the debounce timer.
func (q *pluginQueue) enqueue(ev dirtyEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.pending = append(q.pending, ev)

	if q.timer != nil {
		q.timer.Reset(coalesceWindow)
	} else {
		q.timer = time.AfterFunc(coalesceWindow, func() {
			q.fire()
		})
	}
}

// fire is called by the AfterFunc goroutine after the debounce window expires.
// It drains the pending queue, coalesces all events, and calls Render once.
func (q *pluginQueue) fire() {
	q.mu.Lock()
	pending := q.pending
	q.pending = nil
	q.timer = nil
	q.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	// Coalesce: union of all changed keys, latest state snapshot.
	changedSet := map[string]struct{}{}
	var latestState ClusterState
	for i, ev := range pending {
		for _, k := range ev.changed {
			changedSet[k] = struct{}{}
		}
		if i == len(pending)-1 {
			latestState = ev.state
		}
	}

	q.render(context.Background(), latestState)
}

// render calls the plugin's Render with a hard timeout, logs failures, and
// writes a system_alert on error — without blocking other plugins.
func (q *pluginQueue) render(ctx context.Context, state ClusterState) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	instrs, err := q.plugin.Render(state)
	name := q.plugin.Name()

	if err != nil {
		log.Error().
			Err(err).
			Str("plugin", name).
			Str("node_id", state.NodeID).
			Msg("config.observer: Render failed")

		if q.alerts != nil {
			_, alertErr := q.alerts.Set(ctx, sysalerts.SetArgs{
				Key:     "config_render_failed",
				Device:  state.NodeID,
				Level:   sysalerts.LevelCritical,
				Message: err.Error(),
				Fields:  map[string]any{"plugin": name},
			})
			if alertErr != nil {
				log.Warn().Err(alertErr).Str("plugin", name).Msg("config.observer: failed to write system_alert")
			}
		}
		return
	}

	// Successful render: clear any outstanding error alert.
	if q.alerts != nil {
		_, _ = q.alerts.Unset(ctx, "config_render_failed", state.NodeID)
	}

	// Log the successful render for visibility during Day 1 (no push yet —
	// plugins will hook into the push path on Day 2+).
	log.Debug().
		Str("plugin", name).
		Str("node_id", state.NodeID).
		Int("instruction_count", len(instrs)).
		Msg("config.observer: Render succeeded (push path not yet wired — Day 2)")
}
