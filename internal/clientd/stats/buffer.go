package stats

import (
	"sync"
	"time"
)

// Batch groups samples from one plugin for one collection tick. It is the unit
// stored in the ring buffer and transmitted to the server.
type Batch struct {
	// BatchID is a client-generated UUID for idempotent server-side ingestion.
	BatchID string `json:"batch_id"`
	// Plugin is the plugin name (matches Plugin.Name()).
	Plugin string `json:"plugin"`
	// Samples are the measurements from one collection cycle.
	Samples []Sample `json:"samples"`
	// TSCollected is the wall-clock time when the batch was produced.
	TSCollected time.Time `json:"ts_collected"`
}

// Buffer is a bounded ring buffer for offline stat batches.
//
// Bounds: the buffer holds at most maxBatches entries. When full, the oldest
// batch is silently dropped (drop-oldest policy). At the default tick interval
// of 30s with 9 plugins, one minute of backlog produces ~18 batches. The
// default capacity of 5 minutes × 9 plugins = 90 batches ensures we survive
// short WS disconnects without losing data.
//
// Thread safety: all methods are safe for concurrent use.
type Buffer struct {
	mu         sync.Mutex
	ring       []Batch
	head       int // index of oldest entry
	tail       int // index of next write position
	count      int
	maxBatches int
}

// defaultBufferCapacity is 5 minutes of batches at the default 30s interval
// with 9 plugins. Adjust if the plugin count grows significantly.
//
// Bound documentation:
//   - Default tick: 30s, 9 plugins → 9 batches/minute → 45 batches for 5 minutes.
//   - We round up to 90 to absorb bursts and cover non-default tick rates.
const defaultBufferCapacity = 90

// NewBuffer creates a ring buffer with the given capacity.
// If capacity <= 0 the defaultBufferCapacity is used.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = defaultBufferCapacity
	}
	return &Buffer{
		ring:       make([]Batch, capacity),
		maxBatches: capacity,
	}
}

// Push adds a batch to the buffer. If the buffer is full, the oldest batch is
// dropped to make room (drop-oldest policy).
func (b *Buffer) Push(batch Batch) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == b.maxBatches {
		// Drop oldest: advance head.
		b.head = (b.head + 1) % b.maxBatches
		b.count--
	}
	b.ring[b.tail] = batch
	b.tail = (b.tail + 1) % b.maxBatches
	b.count++
}

// DrainAll removes and returns all buffered batches in FIFO order.
// The buffer is empty after this call.
func (b *Buffer) DrainAll() []Batch {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.count == 0 {
		return nil
	}
	out := make([]Batch, b.count)
	for i := range out {
		out[i] = b.ring[(b.head+i)%b.maxBatches]
	}
	b.head = 0
	b.tail = 0
	b.count = 0
	return out
}

// Len returns the number of batches currently in the buffer.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}
