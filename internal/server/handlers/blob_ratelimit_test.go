package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── tokenBucket unit tests ────────────────────────────────────────────────────

func TestTokenBucket_UnlimitedWhenZero(t *testing.T) {
	tb := newTokenBucket(0)
	if tb != nil {
		t.Errorf("expected nil bucket for bytesPerSec=0, got non-nil")
	}
}

func TestTokenBucket_NegativeReturnsNil(t *testing.T) {
	tb := newTokenBucket(-1)
	if tb != nil {
		t.Errorf("expected nil bucket for negative rate, got non-nil")
	}
}

func TestTokenBucket_ConsumeCancel(t *testing.T) {
	// Rate of 1 byte/sec — consuming 1 000 bytes should block essentially forever.
	tb := newTokenBucket(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := tb.consume(ctx, 1000)
	if err == nil {
		t.Error("expected context cancellation error, got nil")
	}
}

func TestTokenBucket_FastConsume(t *testing.T) {
	// 10 MB/s bucket consuming 1 byte should return instantly.
	tb := newTokenBucket(10 * 1024 * 1024)
	ctx := context.Background()
	if err := tb.consume(ctx, 1); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── BPS throughput test ───────────────────────────────────────────────────────
//
// Assert that writing 5 MB through a 1 MB/s cap takes at least 4 seconds.
// We use rateLimitedWriter (io.Writer wrapper) so we don't need an HTTP server.

func TestRateLimitedWriter_ThroughputCap(t *testing.T) {
	const rate = 1 * 1024 * 1024 // 1 MB/s
	const dataSize = 5 * 1024 * 1024 // 5 MB
	const minDuration = 4 * time.Second

	tb := newTokenBucket(rate)
	sink := io.Discard
	rlw := &rateLimitedWriter{w: sink, ctx: context.Background(), tb: tb}

	data := make([]byte, dataSize)

	start := time.Now()
	n, err := rlw.Write(data)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != dataSize {
		t.Errorf("wrote %d bytes, want %d", n, dataSize)
	}
	if elapsed < minDuration {
		t.Errorf("5 MB at 1 MB/s finished in %v, want >= %v (rate limiter not working)", elapsed, minDuration)
	}
	t.Logf("5 MB at 1 MB/s: elapsed=%v (expected >= %v)", elapsed, minDuration)
}

// ── rateLimitedResponseWriter tests ──────────────────────────────────────────

func TestRateLimitedResponseWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	tb := newTokenBucket(1024 * 1024)
	wrapped := newRateLimitedResponseWriter(rec, context.Background(), tb)

	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}
	uw, ok := wrapped.(unwrapper)
	if !ok {
		t.Fatal("rateLimitedResponseWriter does not implement Unwrap()")
	}
	if uw.Unwrap() != rec {
		t.Error("Unwrap() returned wrong underlying ResponseWriter")
	}
}

func TestRateLimitedResponseWriter_NilBucketPassThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	result := newRateLimitedResponseWriter(rec, context.Background(), nil)
	// Should return the original writer, not a wrapper.
	if result != http.ResponseWriter(rec) {
		t.Error("nil bucket should return the original ResponseWriter unchanged")
	}
}

func TestRateLimitedResponseWriter_DataIntegrity(t *testing.T) {
	// Write 256 KB through the limiter at 10 MB/s (near-instant) and assert
	// all bytes arrive at the underlying writer without corruption.
	const size = 256 * 1024
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i % 257)
	}

	var buf bytes.Buffer
	rec := httptest.NewRecorder()
	rec.Body = &buf

	tb := newTokenBucket(10 * 1024 * 1024) // 10 MB/s — should finish fast
	wrapped := newRateLimitedResponseWriter(rec, context.Background(), tb)

	n, err := wrapped.Write(payload)
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != size {
		t.Errorf("wrote %d bytes, want %d", n, size)
	}
	// httptest.Recorder appends to Body on Write.
	got := buf.Bytes()
	if len(got) != size {
		t.Fatalf("received %d bytes, want %d", len(got), size)
	}
	for i, b := range got {
		if b != payload[i] {
			t.Fatalf("data corruption at byte %d: got %d, want %d", i, b, payload[i])
		}
	}
}

// ── Concurrency semaphore test ────────────────────────────────────────────────
//
// Fire 10 goroutines each trying to acquire a semaphore of size 2.
// Assert that at most 2 are in-flight simultaneously.

func TestBlobSemaphore_MaxConcurrency(t *testing.T) {
	const maxConcurrent = 2
	const totalRequests = 10

	sem := make(chan struct{}, maxConcurrent)
	var (
		mu        sync.Mutex
		maxSeen   int
		inFlight  int
	)

	var wg sync.WaitGroup
	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Acquire.
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			inFlight++
			if inFlight > maxSeen {
				maxSeen = inFlight
			}
			mu.Unlock()

			// Simulate work.
			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxSeen > maxConcurrent {
		t.Errorf("max in-flight was %d, want <= %d", maxSeen, maxConcurrent)
	}
}

// ── blobConcurrencyLimit env parsing ─────────────────────────────────────────

func TestBlobConcurrencyLimit_Default(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENCY", "")
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENT", "")
	got := blobConcurrencyLimit()
	if got != defaultBlobMaxConcurrent {
		t.Errorf("got %d, want default %d", got, defaultBlobMaxConcurrent)
	}
}

func TestBlobConcurrencyLimit_NewEnvVar(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENCY", "4")
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENT", "99")
	got := blobConcurrencyLimit()
	if got != 4 {
		t.Errorf("got %d, want 4 (new env var should win)", got)
	}
}

func TestBlobConcurrencyLimit_LegacyFallback(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENCY", "")
	t.Setenv("CLUSTR_BLOB_MAX_CONCURRENT", "3")
	got := blobConcurrencyLimit()
	if got != 3 {
		t.Errorf("got %d, want 3 (legacy env var fallback)", got)
	}
}

// ── blobMaxBPS env parsing ────────────────────────────────────────────────────

func TestBlobMaxBPS_Unset(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_BPS", "")
	if got := blobMaxBPS(); got != 0 {
		t.Errorf("got %d, want 0 when unset", got)
	}
}

func TestBlobMaxBPS_Set(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_BPS", "10485760") // 10 MB/s
	if got := blobMaxBPS(); got != 10485760 {
		t.Errorf("got %d, want 10485760", got)
	}
}

func TestBlobMaxBPS_Invalid(t *testing.T) {
	t.Setenv("CLUSTR_BLOB_MAX_BPS", "not-a-number")
	if got := blobMaxBPS(); got != 0 {
		t.Errorf("got %d, want 0 for invalid input", got)
	}
}

// ── Context cancellation propagates through writer ────────────────────────────

func TestRateLimitedWriter_ContextCancelPropagates(t *testing.T) {
	// 1 byte/s — writing 1 MB should trigger context timeout well before completion.
	tb := newTokenBucket(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rlw := &rateLimitedWriter{w: io.Discard, ctx: ctx, tb: tb}
	data := make([]byte, 1*1024*1024)

	_, err := rlw.Write(data)
	if err == nil {
		t.Error("expected context error, got nil")
	}
}

// ── Parallel BPS cap: multiple streams each get independent quota ─────────────

func TestRateLimitedWriter_ParallelStreamsIndependent(t *testing.T) {
	// Two independent writers at 1 MB/s each writing 2 MB.
	// Each should take ~2s independently; together still ~2s (not 4s).
	const rate = 1 * 1024 * 1024
	const dataSize = 2 * 1024 * 1024
	const maxDuration = 4 * time.Second // 2 parallel × 2s each → still ~2s

	data := make([]byte, dataSize)

	var (
		wg      sync.WaitGroup
		maxTime atomic.Int64
	)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tb := newTokenBucket(rate)
			rlw := &rateLimitedWriter{w: io.Discard, ctx: context.Background(), tb: tb}
			start := time.Now()
			rlw.Write(data)
			elapsed := time.Since(start).Nanoseconds()
			for {
				old := maxTime.Load()
				if elapsed <= old {
					break
				}
				if maxTime.CompareAndSwap(old, elapsed) {
					break
				}
			}
		}()
	}
	wg.Wait()

	elapsed := time.Duration(maxTime.Load())
	if elapsed >= maxDuration {
		t.Errorf("parallel streams took %v, want < %v (streams should be independent)", elapsed, maxDuration)
	}
}
