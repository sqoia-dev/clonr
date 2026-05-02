package stats

import (
	"testing"
	"time"
)

func TestBuffer_PushAndDrain(t *testing.T) {
	buf := NewBuffer(5)

	for i := 0; i < 3; i++ {
		buf.Push(Batch{BatchID: string(rune('a' + i)), TSCollected: time.Now()})
	}

	if buf.Len() != 3 {
		t.Fatalf("Len: want 3 got %d", buf.Len())
	}

	drained := buf.DrainAll()
	if len(drained) != 3 {
		t.Fatalf("DrainAll: want 3 got %d", len(drained))
	}
	if buf.Len() != 0 {
		t.Fatalf("Len after drain: want 0 got %d", buf.Len())
	}
}

func TestBuffer_DropOldest(t *testing.T) {
	buf := NewBuffer(3)

	for i := 0; i < 5; i++ {
		buf.Push(Batch{BatchID: string(rune('a' + i)), TSCollected: time.Now()})
	}

	if buf.Len() != 3 {
		t.Fatalf("Len: want 3 got %d", buf.Len())
	}

	drained := buf.DrainAll()
	// The oldest two ('a', 'b') should have been dropped.
	if drained[0].BatchID != "c" {
		t.Errorf("oldest retained: want c got %q", drained[0].BatchID)
	}
	if drained[2].BatchID != "e" {
		t.Errorf("newest: want e got %q", drained[2].BatchID)
	}
}

func TestBuffer_DrainEmpty(t *testing.T) {
	buf := NewBuffer(10)
	if drained := buf.DrainAll(); drained != nil {
		t.Errorf("empty drain: want nil got %v", drained)
	}
}
