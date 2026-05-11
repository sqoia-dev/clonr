package config

// observer_priority_test.go — Sprint 41 Day 2 (PLUGIN-PRIORITY)
//
// Tests that the observer fires plugins in ascending Priority order within a
// single coalesce batch, with stable registration-order tiebreaking.
//
// These tests are the primary acceptance criterion for PLUGIN-PRIORITY.
// The handler-level test in nodes_observer_priority_test.go verifies the
// hostname-before-hosts contract through the full HTTP path.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── orderedPlugin ────────────────────────────────────────────────────────────

// orderedPlugin is a test Plugin that records its call-sequence position in a
// shared atomic counter. Multiple orderedPlugins sharing the same counter
// produce a total ordering of their Render invocations without a mutex.
type orderedPlugin struct {
	pluginName string
	keys       []string
	priority   int // 0 = unset sentinel → EffectivePriority returns DefaultPriority (100)
	// counter is shared across all orderedPlugins in one test. Each Render
	// atomically increments it and stores the returned value in callPos.
	counter *atomic.Int64
	// callPos is the call-sequence position set by the most recent Render (1-based).
	callPos atomic.Int64
	// renderCalled counts Render invocations (used by waitForRender).
	renderCalled atomic.Int32
}

func (p *orderedPlugin) Name() string         { return p.pluginName }
func (p *orderedPlugin) WatchedKeys() []string { return p.keys }
func (p *orderedPlugin) Metadata() PluginMetadata {
	return PluginMetadata{Priority: p.priority}
}
func (p *orderedPlugin) Render(_ ClusterState) ([]api.InstallInstruction, error) {
	pos := p.counter.Add(1)
	p.callPos.Store(pos)
	p.renderCalled.Add(1)
	return nil, nil
}

// newSharedCounter creates a shared atomic counter for use across plugins in one test.
func newSharedCounter() *atomic.Int64 { return &atomic.Int64{} }

// waitForRender polls p.renderCalled until it reaches want or timeout expires.
func waitForRender(t *testing.T, p *orderedPlugin, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.renderCalled.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("plugin %q: renderCalled=%d, want >=%d after %v",
		p.pluginName, p.renderCalled.Load(), want, timeout)
}

// ─── PLUGIN-PRIORITY tests ────────────────────────────────────────────────────

// TestObserver_FiresPluginsInPriorityOrder registers four plugins with
// priorities 200/100/50/150, fires one Notify that hits all four, and asserts
// that Render was called in ascending priority order: 50→100→150→200.
// Registration order is intentionally NOT the priority order to prove sorting
// is by priority, not insertion.
func TestObserver_FiresPluginsInPriorityOrder(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	ctr := newSharedCounter()
	const key = "order.shared"

	p200 := &orderedPlugin{pluginName: "p200", keys: []string{key}, priority: 200, counter: ctr}
	p100 := &orderedPlugin{pluginName: "p100", keys: []string{key}, priority: 100, counter: ctr}
	p50 := &orderedPlugin{pluginName: "p50", keys: []string{key}, priority: 50, counter: ctr}
	p150 := &orderedPlugin{pluginName: "p150", keys: []string{key}, priority: 150, counter: ctr}

	// Register in a non-priority order to stress the sort.
	Register(p200)
	Register(p100)
	Register(p50)
	Register(p150)

	Notify([]string{key}, ClusterState{NodeID: "n1"})

	// Wait for all four renders to complete.
	waitForRender(t, p50, 1, 500*time.Millisecond)
	waitForRender(t, p100, 1, 500*time.Millisecond)
	waitForRender(t, p150, 1, 500*time.Millisecond)
	waitForRender(t, p200, 1, 500*time.Millisecond)

	pos50 := p50.callPos.Load()
	pos100 := p100.callPos.Load()
	pos150 := p150.callPos.Load()
	pos200 := p200.callPos.Load()

	if pos50 >= pos100 {
		t.Errorf("priority sort: p50.callPos=%d must be < p100.callPos=%d", pos50, pos100)
	}
	if pos100 >= pos150 {
		t.Errorf("priority sort: p100.callPos=%d must be < p150.callPos=%d", pos100, pos150)
	}
	if pos150 >= pos200 {
		t.Errorf("priority sort: p150.callPos=%d must be < p200.callPos=%d", pos150, pos200)
	}
}

// TestObserver_StableSortByRegistrationOrder registers two plugins with
// identical priority 100, in registration order A then B, and asserts that
// A.Render fires before B.Render (stable sort preserves insertion order on tie).
func TestObserver_StableSortByRegistrationOrder(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	ctr := newSharedCounter()
	const key = "order.stable"

	pA := &orderedPlugin{pluginName: "stable-A", keys: []string{key}, priority: 100, counter: ctr}
	pB := &orderedPlugin{pluginName: "stable-B", keys: []string{key}, priority: 100, counter: ctr}

	Register(pA) // registered first → must fire first on tie
	Register(pB)

	Notify([]string{key}, ClusterState{NodeID: "n1"})

	waitForRender(t, pA, 1, 400*time.Millisecond)
	waitForRender(t, pB, 1, 400*time.Millisecond)

	posA := pA.callPos.Load()
	posB := pB.callPos.Load()

	if posA >= posB {
		t.Errorf("stable sort violation: A (registered first, priority=100) callPos=%d, B callPos=%d; want A < B",
			posA, posB)
	}
}

// TestObserver_UnsetPriorityIsHundred registers a plugin with Priority=0 (the
// unset sentinel) flanked by plugins at Priority=50 and Priority=150. Asserts
// the sentinel plugin fires between them, i.e. at effective priority 100.
func TestObserver_UnsetPriorityIsHundred(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	ctr := newSharedCounter()
	const key = "order.sentinel"

	p50 := &orderedPlugin{pluginName: "sentinel-50", keys: []string{key}, priority: 50, counter: ctr}
	// Priority=0 is the unset sentinel; EffectivePriority promotes it to DefaultPriority (100).
	pUnset := &orderedPlugin{pluginName: "sentinel-unset", keys: []string{key}, priority: 0, counter: ctr}
	p150 := &orderedPlugin{pluginName: "sentinel-150", keys: []string{key}, priority: 150, counter: ctr}

	// Register in reverse priority order to stress the sort.
	Register(p150)
	Register(pUnset)
	Register(p50)

	Notify([]string{key}, ClusterState{NodeID: "n1"})

	waitForRender(t, p50, 1, 400*time.Millisecond)
	waitForRender(t, pUnset, 1, 400*time.Millisecond)
	waitForRender(t, p150, 1, 400*time.Millisecond)

	pos50 := p50.callPos.Load()
	posUnset := pUnset.callPos.Load()
	pos150 := p150.callPos.Load()

	// Expected fire order: p50 → unset(100) → p150
	if pos50 >= posUnset {
		t.Errorf("sentinel: p50.callPos=%d must be < unset.callPos=%d (effective 100)", pos50, posUnset)
	}
	if posUnset >= pos150 {
		t.Errorf("sentinel: unset.callPos=%d (effective 100) must be < p150.callPos=%d", posUnset, pos150)
	}
}

// TestObserver_PriorityOnlyAppliesPerBatch fires plugin A (priority=50) in
// batch 1, waits for it to complete, then fires plugin B (priority=10) in
// batch 2. Asserts that B.Render fires after A.Render despite B having lower
// priority — cross-batch ordering is by arrival time, not priority.
func TestObserver_PriorityOnlyAppliesPerBatch(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	ctr := newSharedCounter()
	const (
		keyA = "batch.key.a"
		keyB = "batch.key.b"
	)

	pA := &orderedPlugin{pluginName: "batch-A", keys: []string{keyA}, priority: 50, counter: ctr}
	pB := &orderedPlugin{pluginName: "batch-B", keys: []string{keyB}, priority: 10, counter: ctr}

	Register(pA)
	Register(pB)

	// Batch 1: fire A only.
	Notify([]string{keyA}, ClusterState{NodeID: "n1"})

	// Wait for A to complete before issuing batch 2, so there is no ambiguity
	// about which batch is "earlier".
	waitForRender(t, pA, 1, 400*time.Millisecond)

	// Batch 2: fire B only. B has lower priority (10) than A (50), but being
	// in a later batch it must fire after A regardless.
	Notify([]string{keyB}, ClusterState{NodeID: "n1"})

	waitForRender(t, pB, 1, 400*time.Millisecond)

	posA := pA.callPos.Load()
	posB := pB.callPos.Load()

	if posA == 0 {
		t.Fatal("batch-A: Render was never called")
	}
	if posB == 0 {
		t.Fatal("batch-B: Render was never called")
	}
	if posB <= posA {
		t.Errorf("cross-batch order: B (priority=10, batch 2) callPos=%d must be > A (priority=50, batch 1) callPos=%d",
			posB, posA)
	}
}
