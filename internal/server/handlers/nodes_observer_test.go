package handlers

// nodes_observer_test.go — Sprint 36 Day 2
//
// Tests that UpdateNode fires ConfigObserverNotify with the correct arguments
// when a hostname changes, and that it does NOT fire when the hostname is
// unchanged.

import (
	"net/http"
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
// calls ConfigObserverNotify with:
//   - the hostname watch-key ("nodes.*.hostname")
//   - the correct node ID
//   - the updated cfg with the new hostname
//
// when the PUT request changes the node's hostname.
func TestUpdateNodeConfig_FiresNotifyForHostnameChange(t *testing.T) {
	d := openTestDB(t)

	// Create a node with an initial hostname.
	const (
		mac          = "bb:cc:dd:ee:ff:01"
		oldHostname  = "compute-before"
		newHostname  = "compute-after"
	)
	node := makeTestNode(t, d, mac, oldHostname)

	cap := &captureNotifyArgs{}
	h := &NodesHandler{
		DB:                   d,
		ConfigObserverNotify: cap.notify,
	}

	w := putNodeRequest(t, h, node.ID, map[string]any{
		"hostname":    newHostname,
		"primary_mac": mac,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("UpdateNode: expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// ConfigObserverNotify must have been called exactly once.
	if got := cap.called.Load(); got != 1 {
		t.Fatalf("ConfigObserverNotify called %d times, want 1", got)
	}

	// Verify the watch key matches the hostname plugin's registered key.
	wantKey := plugins.WatchKey()
	if len(cap.changed) != 1 || cap.changed[0] != wantKey {
		t.Errorf("changed keys = %v, want [%q]", cap.changed, wantKey)
	}

	// Verify the node ID is correct.
	if cap.nodeID != node.ID {
		t.Errorf("nodeID = %q, want %q", cap.nodeID, node.ID)
	}

	// Verify the cfg carries the new hostname.
	if cap.cfg.Hostname != newHostname {
		t.Errorf("cfg.Hostname = %q, want %q", cap.cfg.Hostname, newHostname)
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
