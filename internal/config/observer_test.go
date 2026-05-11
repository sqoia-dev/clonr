package config

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
	sysalerts "github.com/sqoia-dev/clustr/internal/server/alerts"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// resetRegistry tears down and reinitialises the global observer state so
// each test starts with a clean slate.
func resetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	plugins = map[string]*pluginQueue{}
	watchIndex = map[string][]*pluginQueue{}
	globalAlerts = nil
}

// countingPlugin is a test Plugin that counts how many times Render is called.
type countingPlugin struct {
	name    string
	keys    []string
	count   atomic.Int32
	lastErr error // if non-nil, Render returns this error
}

func (p *countingPlugin) Name() string                                     { return p.name }
func (p *countingPlugin) WatchedKeys() []string                            { return p.keys }
func (p *countingPlugin) RenderCount() int                                 { return int(p.count.Load()) }
func (p *countingPlugin) Render(_ ClusterState) ([]api.InstallInstruction, error) {
	p.count.Add(1)
	if p.lastErr != nil {
		return nil, p.lastErr
	}
	return nil, nil
}

// Metadata returns the zero-value metadata (DefaultPriority via EffectivePriority,
// Dangerous=false, Backup=nil). Test plugins have no ordering requirements.
func (p *countingPlugin) Metadata() PluginMetadata { return PluginMetadata{} }

// noopAlertWriter satisfies AlertWriter for tests that need one but don't
// assert on alert content.
type noopAlertWriter struct{}

func (n *noopAlertWriter) Set(_ context.Context, _ sysalerts.SetArgs) (*sysalerts.SystemAlert, error) {
	return &sysalerts.SystemAlert{}, nil
}
func (n *noopAlertWriter) Unset(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

// recordingAlertWriter records calls to Set so tests can assert on them.
type recordingAlertWriter struct {
	sets   []sysalerts.SetArgs
	unsets [][2]string
}

func (r *recordingAlertWriter) Set(_ context.Context, args sysalerts.SetArgs) (*sysalerts.SystemAlert, error) {
	r.sets = append(r.sets, args)
	return &sysalerts.SystemAlert{}, nil
}
func (r *recordingAlertWriter) Unset(_ context.Context, key, device string) (bool, error) {
	r.unsets = append(r.unsets, [2]string{key, device})
	return true, nil
}

// waitForCount polls p.RenderCount() until it reaches want or timeout expires.
func waitForCount(t *testing.T, p *countingPlugin, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.RenderCount() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("plugin %q: RenderCount=%d, want >=%d after %v",
		p.name, p.RenderCount(), want, timeout)
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestObserver_FiresOnWatchedKey verifies that a Notify on a watched key
// triggers Render within the expected window.
func TestObserver_FiresOnWatchedKey(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	p := &countingPlugin{name: "test-fires", keys: []string{"foo.bar"}}
	Register(p)

	Notify([]string{"foo.bar"}, ClusterState{NodeID: "n1"})

	waitForCount(t, p, 1, 200*time.Millisecond)

	if got := p.RenderCount(); got != 1 {
		t.Errorf("RenderCount = %d, want 1", got)
	}
}

// TestObserver_DoesNotFireOnUnwatchedKey verifies that a Notify for a key the
// plugin does not watch does NOT trigger Render.
func TestObserver_DoesNotFireOnUnwatchedKey(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	p := &countingPlugin{name: "test-no-fire", keys: []string{"foo.bar"}}
	Register(p)

	Notify([]string{"other.key"}, ClusterState{NodeID: "n1"})

	// Wait longer than the coalesce window to be sure nothing fires.
	time.Sleep(150 * time.Millisecond)

	if got := p.RenderCount(); got != 0 {
		t.Errorf("RenderCount = %d, want 0 (unwatched key must not trigger Render)", got)
	}
}

// TestObserver_CoalescesRapidNotifies fires 5 Notifies in quick succession
// (well within the 50 ms debounce window) and asserts Render is called exactly
// once with coalesced state.
func TestObserver_CoalescesRapidNotifies(t *testing.T) {
	resetRegistry()
	SetAlertWriter(&noopAlertWriter{})

	p := &countingPlugin{name: "test-coalesce", keys: []string{"net.ip"}}
	Register(p)

	for i := 0; i < 5; i++ {
		Notify([]string{"net.ip"}, ClusterState{NodeID: fmt.Sprintf("n%d", i)})
		time.Sleep(5 * time.Millisecond) // < coalesceWindow (50ms)
	}

	// Wait long enough for the debounce timer to fire exactly once.
	waitForCount(t, p, 1, 300*time.Millisecond)

	// Give a bit more time to ensure a second Render doesn't arrive.
	time.Sleep(100 * time.Millisecond)

	if got := p.RenderCount(); got != 1 {
		t.Errorf("RenderCount = %d, want 1 (5 rapid notifies must coalesce to one Render)", got)
	}
}

// TestObserver_FailureIsolation registers two plugins watching the same key.
// One returns an error from Render. The other must still run successfully,
// and the failing plugin must have written a system_alert.
func TestObserver_FailureIsolation(t *testing.T) {
	resetRegistry()

	aw := &recordingAlertWriter{}
	SetAlertWriter(aw)

	broken := &countingPlugin{
		name:    "test-broken",
		keys:    []string{"x.y"},
		lastErr: errors.New("render exploded"),
	}
	healthy := &countingPlugin{name: "test-healthy", keys: []string{"x.y"}}

	Register(broken)
	Register(healthy)

	Notify([]string{"x.y"}, ClusterState{NodeID: "n1"})

	// Both goroutines should have fired by the end of their debounce window.
	waitForCount(t, broken, 1, 300*time.Millisecond)
	waitForCount(t, healthy, 1, 300*time.Millisecond)

	if broken.RenderCount() == 0 {
		t.Error("broken plugin Render was never called")
	}
	if healthy.RenderCount() == 0 {
		t.Error("healthy plugin Render was never called — failure isolation violated")
	}

	// The broken plugin should have written a system_alert.
	if len(aw.sets) == 0 {
		t.Error("expected a system_alert Set call for the broken plugin, got none")
	}
	found := false
	for _, s := range aw.sets {
		if s.Key == "config_render_failed" && s.Fields["plugin"] == broken.Name() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Set(key=config_render_failed, fields.plugin=%q), got sets=%v", broken.Name(), aw.sets)
	}
}
