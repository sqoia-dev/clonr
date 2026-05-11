// Package stats owns the separate SQLite handle for clustr's stats domain
// (node_stats, node_external_stats). It is intentionally isolated from the
// main clustr.db so that high-churn stats writes cannot contend with the
// auth/RBAC tables on the main WAL journal.
//
// Concurrency invariant: StatsDB.sql is a *sql.DB opened with
// SetMaxOpenConns(1). All mutations go through this single writer.
// No maps are held on StatsDB; no external mutex is required.
package stats

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

//go:embed migrations/*.sql
//go:embed migrations/lookup.yml
var migrationsFS embed.FS

// StatsDB wraps sql.DB with typed stats operations.
// It is safe to use concurrently; all writes go through the single
// sql.DB handle (SQLite WAL, max 1 open connection).
type StatsDB struct {
	sql *sql.DB
}

// Open opens (or creates) the stats SQLite database at path, applies all
// pending migrations from the stats chain, and returns a ready StatsDB.
//
// The DSN is tuned for high-write workload:
//   - WAL journal mode (parallel reads, single writer — same as clustr.db)
//   - busy_timeout 10 000 ms (longer than clustr.db's 5 000 ms because
//     stats writes are bursty and should retry rather than fail)
//   - synchronous=NORMAL (safe with WAL; faster than FULL for time-series)
//
// Foreign keys are not used in the stats schema, so FK enforcement is
// left at the SQLite default (off).
func Open(dbPath string) (*StatsDB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL",
		dbPath,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("stats: open %s: %w", dbPath, err)
	}
	// Single writer — WAL handles concurrent readers natively.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("stats: ping %s: %w", dbPath, err)
	}

	db := &StatsDB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("stats: migrate: %w", err)
	}
	return db, nil
}

// Close checkpoints the WAL and closes the connection.
func (db *StatsDB) Close() error {
	// Checkpoint so that -wal/-shm side-files are removed on clean shutdown.
	_, _ = db.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return db.sql.Close()
}

// Ping verifies the database connection is alive.
func (db *StatsDB) Ping(ctx context.Context) error {
	return db.sql.PingContext(ctx)
}

// SQL returns the raw *sql.DB handle. Used by tests and adapters that need
// direct query access without going through StatsDB's typed methods.
func (db *StatsDB) SQL() *sql.DB {
	return db.sql
}

// migrate applies all pending migrations from the stats chain.
// Uses the same schema_migrations tracking table as clustr.db but in the
// separate stats.db file, so the namespaces are fully independent.
func (db *StatsDB) migrate() error {
	// Load and validate the lookup manifest first.
	manifest, err := loadLookup(migrationsFS)
	if err != nil {
		return fmt.Errorf("stats migrate: load lookup: %w", err)
	}
	if err := validateLookup(migrationsFS, manifest); err != nil {
		return fmt.Errorf("stats migrate: validate lookup: %w", err)
	}

	// Ensure tracking table.
	if _, err := db.sql.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("stats migrate: create schema_migrations: %w", err)
	}

	// Build a set of already-applied migration names.
	rows, err := db.sql.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("stats migrate: query applied: %w", err)
	}
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("stats migrate: scan applied: %w", err)
		}
		applied[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("stats migrate: applied rows: %w", err)
	}

	// Track which IDs have been applied (for requires checking).
	appliedIDs := make(map[int]bool)
	for _, entry := range manifest {
		if applied[entry.Filename] {
			appliedIDs[entry.ID] = true
		}
	}

	// Apply migrations in lookup order.
	for _, entry := range manifest {
		if applied[entry.Filename] {
			continue
		}

		// Check requires are satisfied.
		for _, reqID := range entry.Requires {
			if !appliedIDs[reqID] {
				return fmt.Errorf("stats migrate: migration %d (%s) requires %d which has not been applied",
					entry.ID, entry.Filename, reqID)
			}
		}

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + entry.Filename)
		if err != nil {
			return fmt.Errorf("stats migrate: read %s: %w", entry.Filename, err)
		}

		tx, err := db.sql.Begin()
		if err != nil {
			return fmt.Errorf("stats migrate: begin tx for %s: %w", entry.Filename, err)
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("stats migrate: apply %s: %w", entry.Filename, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			entry.Filename, time.Now().Unix(),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("stats migrate: record %s: %w", entry.Filename, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("stats migrate: commit %s: %w", entry.Filename, err)
		}

		appliedIDs[entry.ID] = true
		fmt.Printf("stats migrate: applied %d %s — %s\n", entry.ID, entry.Filename, entry.Description)
	}
	return nil
}

// readDirSQLFiles returns all .sql filenames in the given embed.FS directory.
func readDirSQLFiles(fsys embed.FS, dir string) (map[string]bool, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	out := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			out[e.Name()] = true
		}
	}
	return out, nil
}
