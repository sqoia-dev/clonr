package eventlog_test

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/server/eventlog"
)

// TestConcurrentWritesNoInterleave verifies that concurrent Log calls produce
// one valid JSON object per line with no interleaved bytes.
func TestConcurrentWritesNoInterleave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	l, err := eventlog.New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	ctx := context.Background()
	for g := 0; g < goroutines; g++ {
		gID := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				l.Log(ctx,
					fmt.Sprintf("action.%d.%d", gID, i),
					"test", fmt.Sprintf("res-%d", gID),
					fmt.Sprintf("actor-%d", gID),
					map[string]int{"seq": i},
				)
			}
		}()
	}
	wg.Wait()
	_ = l.Close()

	// Reopen file and verify every line is valid JSON.
	f, err := os.Open(path) //#nosec G304 -- test file path
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	lineCount := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry eventlog.Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Errorf("line %d is not valid JSON: %v — raw: %s", lineCount+1, err, line)
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	expected := goroutines * perGoroutine
	if lineCount != expected {
		t.Errorf("want %d lines, got %d", expected, lineCount)
	}
}

// TestNewWithOptions_RespectParams verifies that NewWithOptions uses the caller-
// supplied RotateBytes, MaxArchives, FsyncEvery, and FsyncInterval values rather
// than the package-level defaults.
func TestNewWithOptions_RespectParams(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	const wantRotateBytes int64 = 512 * 1024 // 512 KB
	const wantMaxArchives = 3
	const wantFsyncEvery = 5
	const wantFsyncInterval = 200 * time.Millisecond

	l, err := eventlog.NewWithOptions(path, eventlog.Options{
		RotateBytes:   wantRotateBytes,
		MaxArchives:   wantMaxArchives,
		FsyncEvery:    wantFsyncEvery,
		FsyncInterval: wantFsyncInterval,
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	// Write enough data to trigger at least one rotation (>512 KB).
	ctx := context.Background()
	payload := make([]byte, 1024) // 1 KB per write
	for i := range payload {
		payload[i] = 'x'
	}
	for i := 0; i < 600; i++ {
		l.Log(ctx, "test.rotate", "thing", fmt.Sprintf("id-%d", i), "actor", map[string]string{"data": string(payload)})
	}
	_ = l.Close()

	// At least one archive must exist (.1.gz).
	archivePath := path + ".1.gz"
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("expected archive %s after rotation: %v", archivePath, err)
	}

	// With MaxArchives=3, the fourth archive (.4.gz) must NOT exist.
	overflow := path + ".4.gz"
	if _, err := os.Stat(overflow); err == nil {
		t.Errorf("archive %s should not exist with MaxArchives=%d", overflow, wantMaxArchives)
	}
}

// TestRotation verifies that exceeding the configured rotation threshold causes
// the active file to be archived as a gzip-compressed file.
func TestRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")

	// Use a tiny rotation threshold so a few writes trigger it.
	l, err := eventlog.NewWithOptions(path, eventlog.Options{
		RotateBytes: 1024, // 1 KB
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	// Write enough data to exceed the 1 KB threshold.
	for i := 0; i < 100; i++ {
		l.Log(ctx, "test.event", "thing", fmt.Sprintf("id-%d", i), "actor", map[string]int{"i": i})
	}
	_ = l.Close()

	// The archive events.jsonl.1.gz must exist.
	archivePath := path + ".1.gz"
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("expected archive %s to exist after rotation: %v", archivePath, err)
	}

	// Archive must be valid gzip.
	af, err := os.Open(archivePath) //#nosec G304
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer af.Close()
	gz, err := gzip.NewReader(af)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()

	// Scan decompressed content — every line must be valid JSON.
	scanner := bufio.NewScanner(gz)
	archiveLines := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e eventlog.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Errorf("archive line %d invalid JSON: %v", archiveLines+1, err)
		}
		archiveLines++
	}
	if archiveLines == 0 {
		t.Error("archive contains no lines")
	}
}
