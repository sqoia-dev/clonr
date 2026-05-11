package server_test

// testutil_stats_test.go — test helper for opening a temporary stats.db.
// Imported by all test files in the server_test package that call server.New().

import (
	"path/filepath"
	"testing"

	statsdb "github.com/sqoia-dev/clustr/internal/db/stats"
)

// openTestStatsDB opens a temporary stats.db in t.TempDir() for use in
// server.New() calls during tests. The database is automatically closed
// when the test ends.
func openTestStatsDB(t *testing.T) *statsdb.StatsDB {
	t.Helper()
	dir := t.TempDir()
	sdb, err := statsdb.Open(filepath.Join(dir, "stats.db"))
	if err != nil {
		t.Fatalf("open stats db: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })
	return sdb
}
