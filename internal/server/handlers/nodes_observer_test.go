package handlers

// nodes_observer_test.go — Sprint 36 Day 2–3
//
// Tests that UpdateNode fires ConfigObserverNotify with the correct arguments
// when a hostname or tags change, and that it does NOT fire when the relevant
// fields are unchanged.

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/config/plugins"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// captureNotifyArgs records the most recent ConfigObserverNotify call args.
type captureNotifyArgs struct {
	called  atomic.Int32
	changed []string
	nodeID  string
	cfg     api.NodeConfig
}

func (c *captureNotifyArgs) notify(changed []string, nodeID string, cfg api.NodeConfig) {
	c.called.Add(1)
	c.changed = changed
	c.nodeID = nodeID
	c.cfg = cfg
}

// TestUpdateNodeConfig_FiresNotifyForHostnameChange verifies that UpdateNode
// calls ConfigObserverNotify at least once with the hostname watch-key when
// the PUT request changes the node's hostname. Day 3 note: a hostname change
// now fires a second call for the cluster_hosts watch-key (hosts plugin), so
// this test asserts "at least once" rather than "exactly once".
func TestUpdateNodeConfig_FiresNotifyForHostnameChange(t *testing.T) {
	d := openTestDB(t)

	// Create a node with an initial hostname.
	const (
		mac          = "bb:cc:dd:ee:ff:01"
		oldHostname  = "compute-before"
		newHostname  = "compute-after"
	)
	node := makeTestNode(t, d, mac, oldHostname)

	// Track all notified keys.
	var mu sync.Mutex
	allNotifiedKeys := []string{}
	var notifyCalled atomic.Int32
	var lastNodeID string
	var lastCfg api.NodeConfig

	h := &NodesHandler{
		DB: d,
		ConfigObserverNotify: func(changed []string, nodeID string, cfg api.NodeConfig) {
			notifyCalled.Add(1)
			mu.Lock()
			defer mu.Unlock()
			allNotifiedKeys = append(allNotifiedKeys, changed...)
			lastNodeID = nodeID
			lastCfg = cfg
		},
	}

	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    newHostname,
		"primary_mac": mac,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// ConfigObserverNotify must have been called at least once.
	if got := notifyCalled.Load(); got == 0 {
		t.Fatal("ConfigObserverNotify was not called for a hostname change")
	}

	mu.Lock()
	defer mu.Unlock()

	// The hostname watch-key must appear in the notified keys.
	wantKey := plugins.WatchKey()
	found := false
	for _, k := range allNotifiedKeys {
		if k == wantKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("hostname watch-key %q not in notified keys: %v", wantKey, allNotifiedKeys)
	}

	// Verify the node ID is correct.
	if lastNodeID != node.ID {
		t.Errorf("nodeID = %q, want %q", lastNodeID, node.ID)
	}

	// Verify the cfg carries the new hostname.
	if lastCfg.Hostname != newHostname {
		t.Errorf("cfg.Hostname = %q, want %q", lastCfg.Hostname, newHostname)
	}
}

// TestUpdateNodeConfig_DoesNotFireWhenHostnameUnchanged verifies that
// ConfigObserverNotify is NOT called when the PUT request does not change
// the hostname. Changing a non-hostname field (e.g. kernel_args) must not
// trigger the hostname observer.
func TestUpdateNodeConfig_DoesNotFireWhenHostnameUnchanged(t *testing.T) {
	d := openTestDB(t)

	const (
		mac      = "bb:cc:dd:ee:ff:02"
		hostname = "compute-stable"
	)
	node := makeTestNode(t, d, mac, hostname)

	cap := &captureNotifyArgs{}
	h := &NodesHandler{
		DB:                   d,
		ConfigObserverNotify: cap.notify,
	}

	// PUT with the same hostname — simulates changing only kernel_args.
	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    hostname, // unchanged
		"primary_mac": mac,
		"kernel_args": "console=ttyS0",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// Allow a brief window for any spurious async call (there should be none).
	time.Sleep(10 * time.Millisecond)

	if got := cap.called.Load(); got != 0 {
		t.Errorf("ConfigObserverNotify was called %d times for an unchanged hostname, want 0", got)
	}
}

// TestUpdateNodeConfig_FiresNotifyNilSafe verifies that UpdateNode does not
// panic when ConfigObserverNotify is nil (servers that have not yet wired the
// observer — e.g. tests that use newNodesHandler without the field).
func TestUpdateNodeConfig_FiresNotifyNilSafe(t *testing.T) {
	d := openTestDB(t)

	const (
		mac      = "bb:cc:dd:ee:ff:03"
		hostname = "compute-nil-test"
	)
	node := makeTestNode(t, d, mac, hostname)

	// Handler with nil ConfigObserverNotify — must not panic.
	h := newNodesHandler(d)

	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    "compute-nil-test-new",
		"primary_mac": mac,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200 with nil ConfigObserverNotify, got %d; body: %s",
			w.Code, w.Body.String())
	}
}

// ─── Sprint 36 Day 3: sssd, hosts, limits plugin observer tests ──────────────

// TestUpdateNodeConfig_FiresHostsNotifyOnHostnameChange verifies that
// UpdateNode fires ConfigObserverNotify with the cluster_hosts watch-key
// when the hostname changes (since hostname changes alter the cluster-wide
// host map that /etc/hosts is built from).
func TestUpdateNodeConfig_FiresHostsNotifyOnHostnameChange(t *testing.T) {
	d := openTestDB(t)

	const (
		mac         = "cc:dd:ee:ff:00:01"
		oldHostname = "hosts-before"
		newHostname = "hosts-after"
	)
	node := makeTestNode(t, d, mac, oldHostname)

	// Track all notify calls: keys may appear multiple times (hostname + hosts).
	var mu sync.Mutex // protect notifiedKeys
	notifiedKeys := map[string]bool{}
	h := &NodesHandler{
		DB: d,
		ConfigObserverNotify: func(changed []string, nodeID string, cfg api.NodeConfig) {
			mu.Lock()
			defer mu.Unlock()
			for _, k := range changed {
				notifiedKeys[k] = true
			}
		},
	}

	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    newHostname,
		"primary_mac": mac,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()

	wantKey := plugins.HostsWatchKey()
	if !notifiedKeys[wantKey] {
		t.Errorf("hosts watch-key %q was not notified; got keys: %v", wantKey, notifiedKeys)
	}
}

// TestUpdateNodeConfig_FiresLimitsNotifyOnTagsChange verifies that UpdateNode
// fires ConfigObserverNotify with the limits watch-key when node tags change.
func TestUpdateNodeConfig_FiresLimitsNotifyOnTagsChange(t *testing.T) {
	d := openTestDB(t)

	const (
		mac      = "cc:dd:ee:ff:00:02"
		hostname = "limits-test-node"
	)
	node := makeTestNode(t, d, mac, hostname)

	cap := &captureNotifyArgs{}
	h := &NodesHandler{
		DB:                   d,
		ConfigObserverNotify: cap.notify,
	}

	// PUT with new tags — simulates changing the node role from "compute" to "gpu".
	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    hostname,
		"primary_mac": mac,
		"tags":        []string{"gpu"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	if got := cap.called.Load(); got == 0 {
		t.Fatal("ConfigObserverNotify was not called for a tags change")
	}

	wantKey := plugins.LimitsWatchKey()
	found := false
	for _, k := range cap.changed {
		if k == wantKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("limits watch-key %q not in notified keys: %v", wantKey, cap.changed)
	}
}

// TestUpdateNodeConfig_DoesNotFireLimitsWhenTagsUnchanged verifies that
// ConfigObserverNotify is NOT called with the limits key when the PUT
// request does not change the node's tags.
func TestUpdateNodeConfig_DoesNotFireLimitsWhenTagsUnchanged(t *testing.T) {
	d := openTestDB(t)

	const (
		mac      = "cc:dd:ee:ff:00:03"
		hostname = "limits-stable"
	)
	node := makeTestNode(t, d, mac, hostname)

	// Track only limits-key notifications.
	limitsNotified := &atomic.Int32{}
	h := &NodesHandler{
		DB: d,
		ConfigObserverNotify: func(changed []string, nodeID string, cfg api.NodeConfig) {
			for _, k := range changed {
				if k == plugins.LimitsWatchKey() {
					limitsNotified.Add(1)
				}
			}
		},
	}

	// PUT with the same (empty) tags and a hostname change only.
	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    hostname + "-new",
		"primary_mac": mac,
		// tags omitted → defaults to [] on both sides → no change
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	time.Sleep(10 * time.Millisecond)

	if got := limitsNotified.Load(); got != 0 {
		t.Errorf("limits observer notified %d times for unchanged tags, want 0", got)
	}
}

// TestUpdateNodeConfig_DoesNotFireHostsOrLimitsForUnrelatedChange verifies
// that an update to a field unrelated to hosts or limits (e.g. kernel_args)
// does NOT trigger those plugins' watch-keys.
func TestUpdateNodeConfig_DoesNotFireHostsOrLimitsForUnrelatedChange(t *testing.T) {
	d := openTestDB(t)

	const (
		mac      = "cc:dd:ee:ff:00:04"
		hostname = "unrelated-change"
	)
	node := makeTestNode(t, d, mac, hostname)

	// Collect all notified keys.
	var mu sync.Mutex
	var notifiedKeys []string
	h := &NodesHandler{
		DB: d,
		ConfigObserverNotify: func(changed []string, nodeID string, cfg api.NodeConfig) {
			mu.Lock()
			defer mu.Unlock()
			notifiedKeys = append(notifiedKeys, changed...)
		},
	}

	// PUT changing only kernel_args — hostname and tags are unchanged.
	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    hostname,
		"primary_mac": mac,
		"kernel_args": "console=ttyS0",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	for _, k := range notifiedKeys {
		if k == plugins.HostsWatchKey() {
			t.Errorf("hosts watch-key fired unexpectedly for a kernel_args-only change")
		}
		if k == plugins.LimitsWatchKey() {
			t.Errorf("limits watch-key fired unexpectedly for a kernel_args-only change")
		}
		if k == plugins.SSSDWatchKey() {
			t.Errorf("sssd watch-key fired unexpectedly — sssd is not wired through UpdateNode")
		}
	}
}
