// Package config manages clustr runtime configuration and the reactive
// config-observer (Sprint 36).
//
// # Batch plugin dispatch ordering
//
// Within a single coalesce batch, plugins fire in ascending Priority order
// (default 100 when Priority is unset). Ties break by registration order
// (stable sort via sort.SliceStable). Cross-batch ordering is determined by
// event arrival time, not priority — a plugin in batch 2 fires after all
// plugins in batch 1 regardless of its declared priority.
//
// Implementation note: Notify feeds all affected plugins into a single shared
// batchQueue that debounces and then fires plugins sequentially in priority
// order. Plugins that are affected by different Notify calls at different times
// each produce separate batches. Rapid Notify calls within the coalesce window
// are merged into one batch.
//
// This contract is tested in observer_priority_test.go (PLUGIN-PRIORITY,
// Sprint 41 Day 2).
package config

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	sysalerts "github.com/sqoia-dev/clustr/internal/server/alerts"
	"github.com/sqoia-dev/clustr/pkg/api"
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

// pluginQueue holds the per-plugin state used by the shared batch coordinator.
//
// Dispatch is now managed by globalBatch (notifyBatch → fireBatch) rather than
// per-plugin timers, so pluginQueue is a lightweight struct that carries only
// what the batch runner needs: the plugin itself, the alert writer for Render
// error surfacing, and the registration sequence number for stable-sort tiebreaking.
//
// THREAD-SAFETY: render is called sequentially within each batch; no mutex is
// needed on pluginQueue itself. The globalBatch.mu guards the set of queues
// that will be dispatched in the next batch.
type pluginQueue struct {
	plugin Plugin
	// alerts is the system-alert store used to surface Render failures.
	// May be nil in tests that don't exercise the alert path.
	alerts AlertWriter
	// regOrder is the registration sequence number used for stable sort
	// tiebreaking when two plugins share the same effective priority.
	regOrder int
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

// registryMu guards plugins, watchIndex, and pluginOrder. All exported
// entry-points acquire this lock before reading or mutating any of them.
//
// THREAD-SAFETY invariant: plugins, watchIndex, and pluginOrder are only
// written during Register (startup, single-goroutine). They are read-only
// thereafter. The Notify path acquires registryMu (read lock) to get a
// stable snapshot, then releases it before enqueuing.
var registryMu sync.RWMutex

// plugins holds all registered plugins keyed by Name().
var plugins = map[string]*pluginQueue{}

// pluginOrder records insertion order so stable sort can use it as a
// tiebreaker when two plugins declare the same effective priority.
var pluginOrder []*pluginQueue

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
// Calling Register with invalid Metadata() panics to catch misconfigured plugins.
func Register(p Plugin) {
	registryMu.Lock()
	defer registryMu.Unlock()

	name := p.Name()
	if _, dup := plugins[name]; dup {
		panic("config.Register: duplicate plugin name: " + name)
	}

	// Validate metadata at registration time — caught at startup, not in production.
	if err := ValidatePluginMetadata(name, p.Metadata()); err != nil {
		panic("config.Register: " + err.Error())
	}

	q := &pluginQueue{
		plugin:   p,
		alerts:   globalAlerts,
		regOrder: len(pluginOrder),
	}
	plugins[name] = q
	pluginOrder = append(pluginOrder, q)

	for _, key := range p.WatchedKeys() {
		watchIndex[key] = append(watchIndex[key], q)
	}
}

// Notify tells the observer that the given config-tree paths have changed,
// along with the latest cluster state. The observer fans out to every plugin
// whose WatchedKeys intersect changed, coalescing rapid calls within a 50 ms
// window.
//
// Plugins affected by the same Notify call are dispatched in ascending
// Priority order (stable sort by registration order for ties) within that
// coalesce batch. Cross-batch ordering is by arrival time only — a plugin
// in an earlier batch fires before a plugin in a later batch regardless of
// declared priority.
//
// Notify is non-blocking: it enqueues the event and returns immediately.
// The actual Render dispatch happens asynchronously in the shared batch goroutine.
func Notify(changed []string, state ClusterState) {
	if len(changed) == 0 {
		return
	}

	// Build the set of affected queues from the watch index.
	registryMu.RLock()
	var affected []*pluginQueue
	seen := make(map[*pluginQueue]struct{})
	for _, key := range changed {
		for _, q := range watchIndex[key] {
			if _, ok := seen[q]; !ok {
				seen[q] = struct{}{}
				affected = append(affected, q)
			}
		}
	}
	registryMu.RUnlock()

	if len(affected) == 0 {
		return
	}

	ev := dirtyEvent{changed: changed, state: state}
	notifyBatch(affected, ev)
}

// Stop drains the in-flight batch coalesce timer and cancels pending renders.
// It is a best-effort shutdown — it does not wait for in-progress Render calls
// to complete.
func Stop() {
	globalBatch.mu.Lock()
	if globalBatch.timer != nil {
		globalBatch.timer.Stop()
		globalBatch.timer = nil
	}
	globalBatch.queues = nil
	globalBatch.pending = nil
	globalBatch.mu.Unlock()
}

// ─── per-plugin render ────────────────────────────────────────────────────────

// render calls the plugin's Render with a hard timeout, logs failures, and
// writes a system_alert on error — without blocking other plugins.
func (q *pluginQueue) render(ctx context.Context, state ClusterState) {
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()

	instrs, err := q.plugin.Render(state)
	name := q.plugin.Name()
	meta := q.plugin.Metadata()
	priority := EffectivePriority(meta)

	if err != nil {
		log.Error().
			Err(err).
			Str("plugin", name).
			Str("node_id", state.NodeID).
			Int("priority", priority).
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

	// Log the successful render with plugin name and priority for observability.
	// Priority is carried in ConfigPushPayload (wired in reactive_push.go) so
	// operators and audit tools can correlate push order with declared priority.
	log.Debug().
		Str("plugin", name).
		Str("node_id", state.NodeID).
		Int("instruction_count", len(instrs)).
		Int("priority", priority).
		Msg("config.observer: Render succeeded")
}

// ─── intra-batch priority sort ────────────────────────────────────────────────

// sortedByPriority returns qs sorted in ascending EffectivePriority order.
// The sort is stable: queues with identical effective priorities retain their
// relative registration order (i.e. the order they were passed to Register).
//
// This function is called by the shared batch coordinator (batchQueue) after
// the coalesce window expires, before dispatching Render calls.
func sortedByPriority(qs []*pluginQueue) []*pluginQueue {
	out := make([]*pluginQueue, len(qs))
	copy(out, qs)
	sort.SliceStable(out, func(i, j int) bool {
		pi := EffectivePriority(out[i].plugin.Metadata())
		pj := EffectivePriority(out[j].plugin.Metadata())
		if pi != pj {
			return pi < pj
		}
		// Equal effective priority: preserve registration order.
		return out[i].regOrder < out[j].regOrder
	})
	return out
}

// RenderByName calls the registered plugin with the given name against state
// and returns the rendered instructions plus their hash. Returns an error when
// no plugin with that name is registered or the plugin's Render fails.
//
// This is the hook used by the dangerous-push handler (Sprint 41 Day 3) to
// produce the staged payload without going through the full observer pipeline.
func RenderByName(ctx context.Context, name string, state ClusterState) ([]api.InstallInstruction, string, error) {
	registryMu.RLock()
	q, ok := plugins[name]
	registryMu.RUnlock()
	if !ok {
		return nil, "", fmt.Errorf("config.RenderByName: plugin %q is not registered", name)
	}

	instrs, err := q.plugin.Render(state)
	if err != nil {
		return nil, "", fmt.Errorf("config.RenderByName: plugin %q Render failed: %w", name, err)
	}

	hash, err := HashInstructions(instrs)
	if err != nil {
		return nil, "", fmt.Errorf("config.RenderByName: HashInstructions for plugin %q failed: %w", name, err)
	}
	return instrs, hash, nil
}

// PluginMetadataByName returns the Metadata() of a registered plugin by its
// name, or (PluginMetadata{}, false) if no plugin with that name is registered.
// Safe for concurrent use. Used by the dangerous-push handler to verify that a
// plugin is registered and check its Dangerous flag at request time.
func PluginMetadataByName(name string) (PluginMetadata, bool) {
	registryMu.RLock()
	q, ok := plugins[name]
	registryMu.RUnlock()
	if !ok {
		return PluginMetadata{}, false
	}
	return q.plugin.Metadata(), true
}

// SortPluginsByPriorityForTest sorts a slice of Plugin values by ascending
// EffectivePriority, using slice index as the tiebreaker (stable). This
// exposes the same ordering logic used by the batch dispatcher so integration
// tests in other packages (e.g. internal/server/handlers) can verify the
// hostname-before-hosts contract without touching the global registry.
//
// This function is exported for testing only. Production code must use the
// observer batch path (Notify → notifyBatch → fireBatch → sortedByPriority).
func SortPluginsByPriorityForTest(ps []Plugin) []Plugin {
	out := make([]Plugin, len(ps))
	copy(out, ps)
	sort.SliceStable(out, func(i, j int) bool {
		pi := EffectivePriority(out[i].Metadata())
		pj := EffectivePriority(out[j].Metadata())
		return pi < pj
	})
	return out
}

// ─── shared batch coordinator ────────────────────────────────────────────────
//
// batchQueue is the shared coalescer for multi-plugin batches. When Notify
// hits multiple plugins simultaneously, they are all enqueued into a single
// batchQueue entry. After the coalesce window, the batch fires all affected
// plugins in ascending priority order (stable by registration).
//
// A batchQueue is created per-Notify-call-group: rapid Notify calls within
// the coalesce window are merged into the same batch. This mirrors the
// per-plugin pluginQueue debounce but at the batch level.

// batchState accumulates dirty events across rapid Notify calls.
type batchState struct {
	mu      sync.Mutex
	queues  map[*pluginQueue]struct{} // deduplicated set of affected plugin queues
	pending []dirtyEvent
	timer   *time.Timer
}

// globalBatch is the single shared batch coalescer. All Notify calls funnel
// affected queues into this coalescer so they fire together in priority order.
//
// THREAD-SAFETY: globalBatch.mu guards all fields. The AfterFunc goroutine
// swaps out the fields under the lock before dispatching, so concurrent Notify
// calls either join the existing batch or start a new one.
var globalBatch = &batchState{}

// notifyBatch replaces the per-queue enqueue path: it adds all affected queues
// and the event into the global batch coalescer, (re)starting the debounce timer.
//
// Called by Notify instead of per-queue enqueue when the caller wants
// priority-ordered intra-batch firing. After the coalesce window, the batch
// fires all collected plugins in priority order sequentially.
func notifyBatch(queues []*pluginQueue, ev dirtyEvent) {
	globalBatch.mu.Lock()
	defer globalBatch.mu.Unlock()

	if globalBatch.queues == nil {
		globalBatch.queues = make(map[*pluginQueue]struct{})
	}
	for _, q := range queues {
		globalBatch.queues[q] = struct{}{}
	}
	globalBatch.pending = append(globalBatch.pending, ev)

	if globalBatch.timer != nil {
		globalBatch.timer.Reset(coalesceWindow)
	} else {
		globalBatch.timer = time.AfterFunc(coalesceWindow, func() {
			fireBatch()
		})
	}
}

// fireBatch is called by the AfterFunc goroutine for the global batch queue.
// It drains the accumulated queues and events, coalesces the events, then
// fires each plugin's render in ascending priority order (stable by registration).
func fireBatch() {
	globalBatch.mu.Lock()
	affected := globalBatch.queues
	pending := globalBatch.pending
	globalBatch.queues = nil
	globalBatch.pending = nil
	globalBatch.timer = nil
	globalBatch.mu.Unlock()

	if len(affected) == 0 || len(pending) == 0 {
		return
	}

	// Coalesce: latest state snapshot across all pending events.
	var latestState ClusterState
	for i, ev := range pending {
		if i == len(pending)-1 {
			latestState = ev.state
		}
	}

	// Collect affected queues into a slice for sorting.
	qs := make([]*pluginQueue, 0, len(affected))
	for q := range affected {
		qs = append(qs, q)
	}

	// Sort by effective priority ascending, stable by registration order.
	sorted := sortedByPriority(qs)

	// Dispatch Render calls in priority order, sequentially within the batch.
	for _, q := range sorted {
		q.render(context.Background(), latestState)
	}
}
