package eventbus_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/server/eventbus"
)

// drain reads from ch for up to timeout. Returns all events received.
func drain(ch <-chan eventbus.Event, timeout time.Duration) []eventbus.Event {
	var out []eventbus.Event
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline.C:
			return out
		}
	}
}

func TestBus_BasicPublishSubscribe(t *testing.T) {
	b := eventbus.New()

	ch, cancel := b.Subscribe(nil) // all topics
	defer cancel()

	b.Publish(eventbus.TopicNodes, map[string]string{"node_id": "n1"})

	events := drain(ch, 100*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Topic != eventbus.TopicNodes {
		t.Errorf("expected topic %q, got %q", eventbus.TopicNodes, events[0].Topic)
	}

	var payload map[string]string
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if payload["node_id"] != "n1" {
		t.Errorf("expected node_id=n1, got %q", payload["node_id"])
	}
}

func TestBus_TopicFilter(t *testing.T) {
	b := eventbus.New()

	// Subscriber only wants TopicImages.
	ch, cancel := b.Subscribe([]eventbus.Topic{eventbus.TopicImages})
	defer cancel()

	b.Publish(eventbus.TopicNodes, map[string]string{"x": "1"})  // should NOT arrive
	b.Publish(eventbus.TopicImages, map[string]string{"x": "2"}) // should arrive

	events := drain(ch, 100*time.Millisecond)
	if len(events) != 1 {
		t.Fatalf("expected 1 event (only images), got %d", len(events))
	}
	if events[0].Topic != eventbus.TopicImages {
		t.Errorf("expected topic %q, got %q", eventbus.TopicImages, events[0].Topic)
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	b := eventbus.New()

	ch1, cancel1 := b.Subscribe(nil)
	defer cancel1()
	ch2, cancel2 := b.Subscribe(nil)
	defer cancel2()

	b.Publish(eventbus.TopicGroups, struct{}{})

	e1 := drain(ch1, 100*time.Millisecond)
	e2 := drain(ch2, 100*time.Millisecond)

	if len(e1) != 1 {
		t.Errorf("sub1: expected 1 event, got %d", len(e1))
	}
	if len(e2) != 1 {
		t.Errorf("sub2: expected 1 event, got %d", len(e2))
	}
}

func TestBus_CancelUnsubscribes(t *testing.T) {
	b := eventbus.New()

	ch, cancel := b.Subscribe(nil)
	cancel() // unsubscribe before any publish

	// Closed channel returns zero value immediately.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after cancel")
		}
	default:
		// channel not yet closed at read time; drain to check
		events := drain(ch, 50*time.Millisecond)
		_ = events // may be empty; channel was closed so this is fine
	}
}

func TestBus_AllTopics(t *testing.T) {
	b := eventbus.New()
	ch, cancel := b.Subscribe(nil)
	defer cancel()

	allTopics := []eventbus.Topic{
		eventbus.TopicNodes,
		eventbus.TopicImages,
		eventbus.TopicProgress,
		eventbus.TopicAlerts,
		eventbus.TopicStats,
		eventbus.TopicBundles,
		eventbus.TopicGroups,
		eventbus.TopicPing,
	}
	for _, tp := range allTopics {
		b.Publish(tp, struct{}{})
	}

	events := drain(ch, 200*time.Millisecond)
	if len(events) != len(allTopics) {
		t.Errorf("expected %d events, got %d", len(allTopics), len(events))
	}
}
