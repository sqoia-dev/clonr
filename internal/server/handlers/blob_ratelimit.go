package handlers

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// ── Token bucket (no external dep) ───────────────────────────────────────────
//
// tokenBucket provides a simple per-stream byte rate limiter.
// The bucket refills at `rate` bytes/second up to a capacity of `rate` bytes.
// When writing N bytes would exceed the available tokens, the writer sleeps
// until enough tokens have accumulated.
//
// Thread safety: each tokenBucket is used by a single goroutine (one HTTP
// handler); no mutex needed.

type tokenBucket struct {
	rate     int64     // bytes per second
	tokens   int64     // current token count
	capacity int64     // max tokens (= rate, capped at 1 burst per second)
	lastFill time.Time // last refill timestamp
}

// newTokenBucket creates a token bucket that refills at bytesPerSec.
// Returns nil when bytesPerSec == 0 (unlimited).
func newTokenBucket(bytesPerSec int64) *tokenBucket {
	if bytesPerSec <= 0 {
		return nil
	}
	now := time.Now()
	return &tokenBucket{
		rate:     bytesPerSec,
		tokens:   bytesPerSec, // start full
		capacity: bytesPerSec,
		lastFill: now,
	}
}

// consume blocks until n tokens are available, consuming them.
// ctx is checked between sleeps so the caller can cancel.
func (tb *tokenBucket) consume(ctx context.Context, n int64) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tb.refill()
		if tb.tokens >= n {
			tb.tokens -= n
			return nil
		}
		// Calculate how long to wait for enough tokens.
		needed := n - tb.tokens
		waitSec := float64(needed) / float64(tb.rate)
		waitDur := time.Duration(waitSec * float64(time.Second))
		// Clamp wait to a reasonable maximum to stay responsive to context cancellation.
		if waitDur > 200*time.Millisecond {
			waitDur = 200 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDur):
		}
	}
}

func (tb *tokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastFill)
	newTokens := int64(elapsed.Seconds() * float64(tb.rate))
	if newTokens > 0 {
		tb.tokens += newTokens
		if tb.tokens > tb.capacity {
			tb.tokens = tb.capacity
		}
		tb.lastFill = now
	}
}

// ── Rate-limited ResponseWriter ────────────────────────────────────────────────
//
// rateLimitedResponseWriter wraps http.ResponseWriter and throttles writes
// through the token bucket. Implements http.ResponseWriter and http.Flusher.
// http.Hijacker is not wrapped (not needed for blob streaming).

type rateLimitedResponseWriter struct {
	http.ResponseWriter
	ctx context.Context
	tb  *tokenBucket
}

func newRateLimitedResponseWriter(w http.ResponseWriter, ctx context.Context, tb *tokenBucket) http.ResponseWriter {
	if tb == nil {
		return w
	}
	return &rateLimitedResponseWriter{ResponseWriter: w, ctx: ctx, tb: tb}
}

func (rw *rateLimitedResponseWriter) Write(b []byte) (int, error) {
	// Write in chunks up to rate bytes at a time to keep latency low.
	const chunkSize = 32 * 1024 // 32 KB chunks
	total := 0
	for len(b) > 0 {
		n := len(b)
		if int64(n) > int64(chunkSize) {
			n = chunkSize
		}
		if err := rw.tb.consume(rw.ctx, int64(n)); err != nil {
			return total, err
		}
		written, err := rw.ResponseWriter.Write(b[:n])
		total += written
		if err != nil {
			return total, err
		}
		b = b[written:]
	}
	return total, nil
}

// Unwrap allows http.ResponseController to access the underlying writer.
func (rw *rateLimitedResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// ── BPS env helper ────────────────────────────────────────────────────────────

// blobMaxBPS returns the effective per-stream byte-rate cap from the environment.
// CLUSTR_BLOB_MAX_BPS accepts integer bytes/sec (e.g. 10485760 for 10 MB/s).
// Returns 0 when unset or zero — caller should treat 0 as unlimited.
func blobMaxBPS() int64 {
	if v := os.Getenv("CLUSTR_BLOB_MAX_BPS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// ── Concurrency env helper ────────────────────────────────────────────────────

// globalBlobSem is the shared concurrency semaphore for all blob streams.
// It is initialised once via globalBlobSemOnce.
var (
	globalBlobSem     chan struct{}
	globalBlobSemOnce sync.Once
)

// blobConcurrencyLimit returns the effective concurrency cap.
// Checks CLUSTR_BLOB_MAX_CONCURRENCY (new name) then falls back to
// CLUSTR_BLOB_MAX_CONCURRENT (legacy) then defaultBlobMaxConcurrent.
func blobConcurrencyLimit() int {
	if v := os.Getenv("CLUSTR_BLOB_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	if v := os.Getenv("CLUSTR_BLOB_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultBlobMaxConcurrent
}

// ── rateLimitedWriter ─────────────────────────────────────────────────────────
// Used by tests and non-ResponseWriter write paths.

type rateLimitedWriter struct {
	w   io.Writer
	ctx context.Context
	tb  *tokenBucket
}

func (rw *rateLimitedWriter) Write(b []byte) (int, error) {
	const chunkSize = 32 * 1024
	total := 0
	for len(b) > 0 {
		n := len(b)
		if int64(n) > int64(chunkSize) {
			n = chunkSize
		}
		if err := rw.tb.consume(rw.ctx, int64(n)); err != nil {
			return total, err
		}
		written, err := rw.w.Write(b[:n])
		total += written
		if err != nil {
			return total, err
		}
		b = b[written:]
	}
	return total, nil
}
