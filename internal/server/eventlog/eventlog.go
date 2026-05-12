// Package eventlog provides a file-backed JSONL structured audit-event mirror.
//
// It is a sidecar to the SQL audit_log table: every row inserted there is also
// appended here so external tools (Vector, Fluent Bit, Promtail, journalctl-like
// tail) can consume events without querying SQLite.
//
// File layout:
//
//	/var/lib/clustr/log/events.jsonl          ← active log (O_APPEND)
//	/var/lib/clustr/log/events.jsonl.1.gz     ← most recent archive
//	...
//	/var/lib/clustr/log/events.jsonl.10.gz    ← oldest archive kept
//
// Rotation is triggered when the active file reaches 100 MB.
// After rotation the old active file is gzip-compressed and renamed.
// At most 10 archives are kept; the oldest is deleted on overflow.
//
// Write safety: each Log call holds mu while building the JSON line and
// writing it to the underlying file. This prevents interleaving across
// concurrent callers. fsync is called every 100 writes or every 1 second,
// whichever comes first, via a background flusher goroutine.
package eventlog

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// rotateBytes is the file size threshold that triggers log rotation (100 MB).
	rotateBytes = 100 * 1024 * 1024

	// maxArchives is the number of compressed archives retained after rotation.
	maxArchives = 10

	// defaultFsyncEvery is the maximum number of writes between forced fsyncs.
	defaultFsyncEvery = 100

	// defaultFsyncInterval is the maximum elapsed time between forced fsyncs.
	defaultFsyncInterval = 1 * time.Second
)

// Entry is one event written to the JSONL file.
type Entry struct {
	Timestamp    string          `json:"ts"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type,omitempty"`
	ResourceID   string          `json:"resource_id,omitempty"`
	ActorID      string          `json:"actor_id,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

// Logger is the interface satisfied by Logger and its no-op counterpart.
type Logger interface {
	// Log appends one structured event line. Non-fatal: errors are silently
	// dropped because audit log writes must not fail the caller's workflow.
	Log(ctx context.Context, action, resourceType, resourceID, actorID string, payload interface{})
	// Close flushes pending writes and closes the underlying file.
	Close() error
}

// FileLogger is the file-backed JSONL implementation.
// Zero value is not usable; construct with New.
type FileLogger struct {
	path string
	mu   sync.Mutex
	file *os.File

	writesSinceFsync int
	fsyncEvery       int
	lastFsync        time.Time
	fsyncInterval    time.Duration

	// writtenSinceRotate tracks bytes written since the last rotation check.
	// It is an approximation — we do not stat on every write for performance.
	writtenSinceRotate int64

	// rotateSize is the threshold for rotation (default rotateBytes).
	rotateSize int64

	// archiveCount is the number of compressed archives to keep (default maxArchives).
	archiveCount int

	// closed is set to 1 when Close is called so the background flusher exits.
	closed atomic.Int32
	wg     sync.WaitGroup
}

// Options configures a FileLogger. Zero values use the defaults documented in
// the package constants.
type Options struct {
	// RotateBytes overrides the rotation threshold. Default: 100 MB.
	RotateBytes int64
	// MaxArchives overrides the number of compressed archives to retain. Default: 10.
	MaxArchives int
	// FsyncEvery overrides the per-write fsync cadence. Default: 100.
	FsyncEvery int
	// FsyncInterval overrides the timed fsync cadence. Default: 1s.
	FsyncInterval time.Duration
}

// New opens (or creates) the JSONL event log at path and starts the background
// fsync goroutine. The caller must call Close() when the process exits.
func New(path string) (*FileLogger, error) {
	return NewWithOptions(path, Options{})
}

// NewWithOptions is like New but accepts an Options struct for test overrides.
func NewWithOptions(path string, opts Options) (*FileLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("eventlog: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640) //#nosec G304 -- operator-configured log path
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}

	rotSz := int64(rotateBytes)
	if opts.RotateBytes > 0 {
		rotSz = opts.RotateBytes
	}
	archCount := maxArchives
	if opts.MaxArchives > 0 {
		archCount = opts.MaxArchives
	}
	fsyncEvery := defaultFsyncEvery
	if opts.FsyncEvery > 0 {
		fsyncEvery = opts.FsyncEvery
	}
	fsyncInterval := defaultFsyncInterval
	if opts.FsyncInterval > 0 {
		fsyncInterval = opts.FsyncInterval
	}

	l := &FileLogger{
		path:          path,
		file:          f,
		fsyncEvery:    fsyncEvery,
		lastFsync:     time.Now(),
		fsyncInterval: fsyncInterval,
		rotateSize:    rotSz,
		archiveCount:  archCount,
	}
	l.wg.Add(1)
	go l.bgFlusher()
	return l, nil
}

// Log implements Logger. Concurrent-safe; never blocks callers on fsync.
func (l *FileLogger) Log(_ context.Context, action, resourceType, resourceID, actorID string, payload interface{}) {
	var rawPayload json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err == nil {
			rawPayload = b
		}
	}

	e := Entry{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ActorID:      actorID,
		Payload:      rawPayload,
	}

	line, err := json.Marshal(e)
	if err != nil {
		// Should never happen — Entry contains only JSON-safe types.
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return
	}

	_, _ = l.file.Write(line)
	l.writtenSinceRotate += int64(len(line))
	l.writesSinceFsync++

	// Inline fsync when threshold is reached (the bg flusher handles interval).
	if l.writesSinceFsync >= l.fsyncEvery {
		_ = l.file.Sync()
		l.writesSinceFsync = 0
		l.lastFsync = time.Now()
	}

	// Check rotation threshold.
	if l.writtenSinceRotate >= l.rotateSize {
		l.rotate() // holds mu
		l.writtenSinceRotate = 0
	}
}

// bgFlusher periodically fsyncs and is the only goroutine that may rotate
// based on elapsed time. It exits when closed is set.
func (l *FileLogger) bgFlusher() {
	defer l.wg.Done()
	ticker := time.NewTicker(l.fsyncInterval / 2)
	defer ticker.Stop()

	for {
		<-ticker.C
		if l.closed.Load() == 1 {
			return
		}
		l.mu.Lock()
		if l.file != nil && time.Since(l.lastFsync) >= l.fsyncInterval {
			_ = l.file.Sync()
			l.writesSinceFsync = 0
			l.lastFsync = time.Now()
		}
		l.mu.Unlock()
	}
}

// rotate renames the current file to a numbered archive, gzip-compresses it,
// and opens a fresh active file. Must be called with l.mu held.
func (l *FileLogger) rotate() {
	if l.file == nil {
		return
	}
	_ = l.file.Sync()
	_ = l.file.Close()
	l.file = nil

	// Shift existing archives: events.jsonl.(N-1).gz → .N.gz, etc.
	for i := l.archiveCount - 1; i >= 1; i-- {
		old := l.path + "." + strconv.Itoa(i) + ".gz"
		new := l.path + "." + strconv.Itoa(i+1) + ".gz"
		_ = os.Rename(old, new)
	}
	// Delete overflow archive.
	_ = os.Remove(l.path + "." + strconv.Itoa(l.archiveCount+1) + ".gz")

	// Compress the current active file to .1.gz
	archivePath := l.path + ".1.gz"
	_ = gzipFile(l.path, archivePath)
	_ = os.Remove(l.path)

	// Open a fresh active file.
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640) //#nosec G304 -- operator-configured log path
	if err == nil {
		l.file = f
	}
}

// Close flushes, fsyncs, and closes the file. Safe to call more than once.
func (l *FileLogger) Close() error {
	l.closed.Store(1)
	l.wg.Wait()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	_ = l.file.Sync()
	err := l.file.Close()
	l.file = nil
	return err
}

// gzipFile compresses src to dst. src is not removed.
func gzipFile(src, dst string) error {
	in, err := os.Open(src) //#nosec G304 -- internal call with operator-controlled log path
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640) //#nosec G304
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		return err
	}
	return gz.Close()
}

// Nop is a no-op Logger used in tests where a real file is not needed.
type Nop struct{}

// Log does nothing.
func (Nop) Log(_ context.Context, _, _, _, _ string, _ interface{}) {}

// Close does nothing.
func (Nop) Close() error { return nil }
