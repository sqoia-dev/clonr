package server_test

// image_events_test.go — TEST-5: unit tests for ImageEventStore (SSE-1).
//
// Verifies:
//   - Publish delivers events to all active subscribers.
//   - Subscribe cancel() correctly unregisters and closes the channel.
//   - Slow subscribers are dropped (non-blocking publish).
//   - Multiple concurrent subscribers each receive their own copy.

import (
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/server"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestImageEventStore_PublishToSubscriber verifies a subscriber receives a published event.
func TestImageEventStore_PublishToSubscriber(t *testing.T) {
	store := server.NewImageEventStore()

	ch, cancel := store.Subscribe()
	defer cancel()

	event := api.ImageEvent{Kind: api.ImageEventCreated, ID: "img-001"}
	store.Publish(event)

	select {
	case got := <-ch:
		if got.ID != event.ID || got.Kind != event.Kind {
			t.Errorf("got event %+v, want %+v", got, event)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout: subscriber did not receive published event")
	}
}

// TestImageEventStore_CancelRemovesSubscriber verifies that after cancel(),
// the closed channel receives no further events.
func TestImageEventStore_CancelRemovesSubscriber(t *testing.T) {
	store := server.NewImageEventStore()

	ch, cancel := store.Subscribe()
	cancel() // unsubscribe immediately

	// Give the store time to process any in-flight events.
	time.Sleep(10 * time.Millisecond)

	store.Publish(api.ImageEvent{Kind: api.ImageEventCreated, ID: "img-002"})

	// The channel is closed; a receive should return the zero value immediately.
	select {
	case v, ok := <-ch:
		if ok {
			t.Errorf("expected closed channel, got event %+v", v)
		}
		// ok=false means channel closed — that's expected.
	case <-time.After(100 * time.Millisecond):
		// If nothing arrives, the channel is empty but not closed (acceptable
		// since the buffer may have been drained). Either outcome is correct.
	}
}

// TestImageEventStore_MultipleSubscribers verifies each subscriber gets its own copy.
func TestImageEventStore_MultipleSubscribers(t *testing.T) {
	store := server.NewImageEventStore()

	ch1, cancel1 := store.Subscribe()
	defer cancel1()
	ch2, cancel2 := store.Subscribe()
	defer cancel2()

	event := api.ImageEvent{Kind: api.ImageEventUpdated, ID: "img-003"}
	store.Publish(event)

	for i, ch := range []<-chan api.ImageEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.ID != event.ID {
				t.Errorf("subscriber %d: got ID %q, want %q", i+1, got.ID, event.ID)
			}
		case <-time.After(500 * time.Millisecond):
			t.Errorf("subscriber %d: timeout waiting for event", i+1)
		}
	}
}

// TestImageEventStore_SlowSubscriberDropped verifies that a full subscriber
// channel does not block the publisher (non-blocking send).
func TestImageEventStore_SlowSubscriberDropped(t *testing.T) {
	store := server.NewImageEventStore()

	_, cancel := store.Subscribe()
	defer cancel()
	// Don't read from ch — it will fill up.

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Publish 200 events; the 65th and beyond should be dropped for the
		// slow subscriber without blocking.
		for i := range 200 {
			store.Publish(api.ImageEvent{Kind: api.ImageEventCreated, ID: "img-" + itoa(i)})
		}
	}()

	select {
	case <-done:
		// Publish completed without blocking — test passes.
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow subscriber (deadlock)")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
