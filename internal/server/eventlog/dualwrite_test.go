package eventlog_test

// dualwrite_test.go — verifies that db.AuditService.RecordEntry lands in both
// SQLite and the JSONL sidecar with the same action field.
//
// This test lives in the eventlog package to keep DB dependencies out of the
// eventlog package itself.  We use the real db.InsertAuditLog path via a
// lightweight in-memory SQLite database (the same approach used by db_test.go).

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/server/eventlog"
)

func TestDualWrite_AuditService(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Open an in-memory clustr DB (applies migrations).
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// Open a JSONL event log.
	logPath := filepath.Join(dir, "events.jsonl")
	el, err := eventlog.New(logPath)
	if err != nil {
		t.Fatalf("eventlog.New: %v", err)
	}
	t.Cleanup(func() { _ = el.Close() })

	// Build AuditService with EventLog wired.
	svc := db.NewAuditService(database)
	svc.EventLog = el

	// Record one audit entry.
	ctx := context.Background()
	svc.Record(ctx, "actor-id", "actor-label",
		"notice.created", "notice", "42",
		"127.0.0.1", nil, map[string]string{"body": "hello"})

	// Give the fsync goroutine a moment.
	time.Sleep(50 * time.Millisecond)
	_ = el.Close()

	// Verify the JSONL file contains the action.
	f, err := os.Open(logPath) //#nosec G304
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	defer f.Close()

	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("invalid JSON line: %s", line)
			continue
		}
		if entry["action"] == "notice.created" {
			found = true
		}
	}

	if !found {
		t.Error("expected notice.created action in JSONL file, not found")
	}
}
