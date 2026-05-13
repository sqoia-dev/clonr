package handlers

// TestHandleHello_LDAPOnConnectNoDeadlock verifies that handleHello fires
// LDAPOnConnect in a goroutine rather than blocking the read loop.
//
// Root cause (fix/v0.2.0-blockers-deadlock-clientd):
//   handleHello is called from dispatchClientMessage, which runs inline in the
//   WS read loop. LDAPOnConnect → pushLDAPToNode → sendConfigPush blocks for
//   up to 30 s waiting for a WS ack. The ack arrives as a WS message — but the
//   read loop is blocked inside handleHello. The ack is never read; push always
//   times out. Fix: `go h.LDAPOnConnect(...)`.
//
// Test strategy:
//   1. Install an LDAPOnConnect callback that blocks until "ack" is sent on a
//      side channel — simulating the 30 s wait.
//   2. Call handleHello synchronously (from the test goroutine, standing in for
//      the read loop).
//   3. In parallel, send the ack from a second goroutine — simulating what the
//      WS read loop would do once it is unblocked.
//   4. Assert that handleHello returns promptly (well under the ack delay)
//      and that LDAPOnConnect eventually completes.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/clientd"
)

func TestHandleHello_LDAPOnConnectNoDeadlock(t *testing.T) {
	// ackCh simulates the WS ack arriving after handleHello returns.
	// LDAPOnConnect blocks until this channel is closed.
	ackCh := make(chan struct{})
	// doneCh is closed when LDAPOnConnect finishes (push completed).
	doneCh := make(chan struct{})

	var mu sync.Mutex
	var capturedNodeID string
	var callCount int

	ldapOnConnect := func(_ context.Context, nodeID string) {
		mu.Lock()
		capturedNodeID = nodeID
		callCount++
		mu.Unlock()

		// Block until the simulated ack arrives — this is what sendConfigPush
		// does while waiting for a WS ack round-trip.
		<-ackCh
		close(doneCh)
	}

	h := &ClientdHandler{
		DB:            &fakeLDAPDB{},
		ServerCtx:     context.Background(),
		LDAPOnConnect: ldapOnConnect,
	}

	payload, _ := json.Marshal(clientd.HelloPayload{
		Hostname:      "test-node",
		KernelVersion: "6.1.0",
	})
	msg := clientd.ClientMessage{
		Type:    "hello",
		Payload: payload,
	}

	// Call handleHello from the "read loop" goroutine (this test goroutine).
	// It must return promptly — before we release the ack — because
	// LDAPOnConnect now runs detached.
	start := time.Now()
	h.handleHello(context.Background(), "node-abc", msg)
	elapsed := time.Since(start)

	// handleHello must return in well under 1 s. The ack channel is not
	// released until after this assertion, so any blocking inside handleHello
	// would exceed this deadline.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handleHello blocked for %v — LDAPOnConnect must run in a goroutine", elapsed)
	}

	// Now simulate the ack arriving (WS read loop processed it).
	close(ackCh)

	// Wait for the goroutine to finish; fail if it never completes.
	select {
	case <-doneCh:
		// push completed — good
	case <-time.After(5 * time.Second):
		t.Fatal("LDAPOnConnect goroutine did not complete within 5 s after ack was released")
	}

	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Fatalf("expected LDAPOnConnect to be called exactly once, got %d", callCount)
	}
	if capturedNodeID != "node-abc" {
		t.Fatalf("expected node_id=node-abc, got %q", capturedNodeID)
	}
}

// TestHandleHello_NilLDAPOnConnect verifies that handleHello is safe when
// LDAPOnConnect is nil (dev/test setups without an LDAP manager wired).
func TestHandleHello_NilLDAPOnConnect(t *testing.T) {
	h := &ClientdHandler{
		DB:            &fakeLDAPDB{},
		LDAPOnConnect: nil,
	}

	payload, _ := json.Marshal(clientd.HelloPayload{
		Hostname: "test-node",
	})
	msg := clientd.ClientMessage{
		Type:    "hello",
		Payload: payload,
	}

	// Must not panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleHello(context.Background(), "node-xyz", msg)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleHello with nil LDAPOnConnect did not return")
	}
}
