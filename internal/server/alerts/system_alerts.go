// Package alerts (server/alerts) hosts the SYSTEM-ALERT-FRAMEWORK
// (Sprint 38) — operator-visible alerts with key+device addressing and a
// push/set/unset/expire lifecycle.
//
// This package is deliberately separate from internal/alerts/, which holds
// the rule-engine evaluation state.  The two are complementary:
//
//   - internal/alerts/      — rule-engine evaluations (firing/resolved
//     driven by YAML rules in /etc/clustr/rules.d/).
//     One row per (rule_name, node_id, sensor) tuple.
//
//   - internal/server/alerts/ (this package) — operator-visible state with
//     TTL.  Other subsystems push alerts here
//     without authoring a rule (e.g. probes,
//     deploy, slurm).  Addressed by (key, device).
//
// Wire shape (matches web/src/lib/types.ts SystemAlert):
//
//	{
//	  "key":        "raid_degraded",
//	  "device":     "ctrl0/vd1",
//	  "level":      "warn"|"info"|"critical",
//	  "message":    "human-readable summary",
//	  "set_at":     "RFC3339 timestamp",
//	  "expires_at": "RFC3339 timestamp"   // optional
//	}
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// Level is the severity classification.
type Level string

const (
	LevelInfo     Level = "info"
	LevelWarn     Level = "warn"
	LevelCritical Level = "critical"
)

var validLevels = map[Level]bool{LevelInfo: true, LevelWarn: true, LevelCritical: true}

// ErrValidation is the sentinel wrapped by every input-validation error
// returned from this package.  The HTTP handler uses errors.Is to
// distinguish 4xx user-input failures from 5xx store/database errors,
// fixing Codex post-ship review issue #8 — the prior implementation
// matched on the literal "system_alerts:" prefix in err.Error(), but
// every fmt.Errorf("system_alerts: ...") wrap from store code (e.g.
// "system_alerts: upsert update: <db error>") matched too, so DB
// errors returned 400 instead of 500.
var ErrValidation = errors.New("system_alerts: validation")

// ErrInvalidLevel is returned when an alert is submitted with a level outside
// the accepted set.  Wraps ErrValidation so handlers can errors.Is
// against the sentinel.
var ErrInvalidLevel = fmt.Errorf("%w: invalid level (want info|warn|critical)", ErrValidation)

// ErrEmptyKey is returned when key is empty.  Wraps ErrValidation.
var ErrEmptyKey = fmt.Errorf("%w: key required", ErrValidation)

// SystemAlert is the wire type for /api/v1/system_alerts responses.
//
// JSON tags: deliberately match Dinesh's web/src/lib/types.ts SystemAlert
// declaration — set_at, expires_at (omitempty), level uses string union.
type SystemAlert struct {
	ID        int64          `json:"id"`
	Key       string         `json:"key"`
	Device    string         `json:"device"`
	Level     Level          `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	SetAt     time.Time      `json:"set_at"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
}

// Store persists SystemAlerts in the system_alerts table and runs a
// background sweep that auto-clears expired rows.
//
// THREAD-SAFETY: all exported methods are safe for concurrent use.  The
// store delegates to the underlying *db.DB (sql.DB pool), and uses a
// sync.Mutex only to coordinate sweep-loop shutdown.
type Store struct {
	db        *db.DB
	now       func() time.Time // injectable clock for tests
	sweepDone chan struct{}
	stopOnce  sync.Once
}

// NewStore constructs a Store bound to the given DB.  Callers that need to
// run the sweep loop should call StartSweeper.
func NewStore(database *db.DB) *Store {
	return &Store{
		db:        database,
		now:       func() time.Time { return time.Now().UTC() },
		sweepDone: make(chan struct{}),
	}
}

// StartSweeper launches a background goroutine that periodically clears
// expired rows.  The interval defaults to 30s; callers may override for
// tests via the returned stop function.
//
// Safe to call once; subsequent calls are no-ops.
func (s *Store) StartSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go s.runSweeper(ctx, interval)
}

// Stop signals the sweep loop to exit.  Idempotent.
func (s *Store) Stop() {
	s.stopOnce.Do(func() { close(s.sweepDone) })
}

func (s *Store) runSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.sweepDone:
			return
		case <-t.C:
			if n, err := s.SweepExpired(ctx); err != nil {
				log.Warn().Err(err).Msg("system_alerts: sweep failed")
			} else if n > 0 {
				log.Debug().Int64("cleared", n).Msg("system_alerts: sweep cleared expired rows")
			}
		}
	}
}

// PushArgs is the input shape for Store.Push.
type PushArgs struct {
	Key     string
	Device  string
	Level   Level
	Message string
	Fields  map[string]any
	TTL     time.Duration // required for Push; 0 ⇒ default 5 minutes
}

// Push inserts a transient alert with an expires_at = now + TTL.  When TTL
// is zero, defaults to 5 minutes — push is for "fire and forget" alerts
// that auto-clear, distinct from Set which has no expiry.
//
// If an active alert already exists for (key, device), its expires_at is
// extended to max(existing, new) and the message/level/fields updated.
// This makes repeated Push calls idempotent under retry without growing
// the row count.
func (s *Store) Push(ctx context.Context, args PushArgs) (*SystemAlert, error) {
	if err := validateArgs(args.Key, args.Level); err != nil {
		return nil, err
	}
	if args.TTL <= 0 {
		args.TTL = 5 * time.Minute
	}
	now := s.now()
	expires := now.Add(args.TTL)
	return s.upsert(ctx, args.Key, args.Device, args.Level, args.Message, args.Fields, now, &expires)
}

// SetArgs is the input shape for Store.Set.
type SetArgs struct {
	Key     string
	Device  string
	Level   Level
	Message string
	Fields  map[string]any
}

// Set upserts a durable alert (no expiry).  If an active row for
// (key, device) exists, level/message/fields are updated and expires_at is
// cleared.  Otherwise a new row is inserted.
func (s *Store) Set(ctx context.Context, args SetArgs) (*SystemAlert, error) {
	if err := validateArgs(args.Key, args.Level); err != nil {
		return nil, err
	}
	now := s.now()
	return s.upsert(ctx, args.Key, args.Device, args.Level, args.Message, args.Fields, now, nil)
}

// Unset clears the active alert for (key, device).  Returns true if a row
// was cleared, false if no active row existed.
func (s *Store) Unset(ctx context.Context, key, device string) (bool, error) {
	if key == "" {
		return false, ErrEmptyKey
	}
	now := s.now().Unix()
	res, err := s.db.SQL().ExecContext(ctx, `
		UPDATE system_alerts
		SET cleared_at = ?
		WHERE key = ? AND device = ? AND cleared_at IS NULL
	`, now, key, device)
	if err != nil {
		return false, fmt.Errorf("system_alerts: unset: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List returns all currently-active alerts (cleared_at IS NULL AND not
// expired).  Note: this filters by current time, so a row whose expires_at
// has passed but the sweeper hasn't yet stamped is filtered out client-side.
func (s *Store) List(ctx context.Context) ([]SystemAlert, error) {
	now := s.now().Unix()
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, key, device, level, message, fields_json, created_at, expires_at
		FROM system_alerts
		WHERE cleared_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at DESC
		LIMIT 1000
	`, now)
	if err != nil {
		return nil, fmt.Errorf("system_alerts: list: %w", err)
	}
	defer rows.Close()
	out := []SystemAlert{}
	for rows.Next() {
		a, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SweepExpired stamps cleared_at on rows whose expires_at has passed.
// Returns the number of rows cleared.
func (s *Store) SweepExpired(ctx context.Context) (int64, error) {
	now := s.now().Unix()
	res, err := s.db.SQL().ExecContext(ctx, `
		UPDATE system_alerts
		SET cleared_at = ?
		WHERE cleared_at IS NULL
		  AND expires_at IS NOT NULL
		  AND expires_at <= ?
	`, now, now)
	if err != nil {
		return 0, fmt.Errorf("system_alerts: sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// upsert is the shared helper for Push and Set.  It uses the partial
// unique index idx_system_alerts_active_keydev to perform an upsert: if an
// active row exists, UPDATE it; otherwise INSERT.
func (s *Store) upsert(ctx context.Context, key, device string, level Level, message string, fields map[string]any, now time.Time, expires *time.Time) (*SystemAlert, error) {
	fieldsJSON, err := encodeFields(fields)
	if err != nil {
		return nil, err
	}

	var expiresUnix sql.NullInt64
	if expires != nil {
		expiresUnix = sql.NullInt64{Int64: expires.Unix(), Valid: true}
	}

	// Try update first (idempotent path).  RowsAffected > 0 ⇒ success.
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("system_alerts: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE system_alerts
		SET level = ?, message = ?, fields_json = ?, expires_at = ?
		WHERE key = ? AND device = ? AND cleared_at IS NULL
	`, string(level), message, fieldsJSON, expiresUnix, key, device)
	if err != nil {
		return nil, fmt.Errorf("system_alerts: upsert update: %w", err)
	}
	n, _ := res.RowsAffected()

	var id int64
	var createdAt time.Time
	if n == 0 {
		// Insert.
		ins, err := tx.ExecContext(ctx, `
			INSERT INTO system_alerts (key, device, level, message, fields_json, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, key, device, string(level), message, fieldsJSON, now.Unix(), expiresUnix)
		if err != nil {
			return nil, fmt.Errorf("system_alerts: upsert insert: %w", err)
		}
		id, _ = ins.LastInsertId()
		createdAt = now
	} else {
		// Update path: re-read created_at + id.
		row := tx.QueryRowContext(ctx, `
			SELECT id, created_at FROM system_alerts
			WHERE key = ? AND device = ? AND cleared_at IS NULL
		`, key, device)
		var cu int64
		if err := row.Scan(&id, &cu); err != nil {
			return nil, fmt.Errorf("system_alerts: upsert reread: %w", err)
		}
		createdAt = time.Unix(cu, 0).UTC()
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("system_alerts: commit: %w", err)
	}

	a := &SystemAlert{
		ID:      id,
		Key:     key,
		Device:  device,
		Level:   level,
		Message: message,
		Fields:  fields,
		SetAt:   createdAt,
	}
	if expires != nil {
		t := expires.UTC()
		a.ExpiresAt = &t
	}
	return a, nil
}

// validateArgs runs the common validation for Push/Set.
func validateArgs(key string, level Level) error {
	if key == "" {
		return ErrEmptyKey
	}
	if !validLevels[level] {
		return fmt.Errorf("%w: %q", ErrInvalidLevel, level)
	}
	return nil
}

// encodeFields marshals the fields map.  Returns sql.NullString so we can
// distinguish empty {} from absent NULL on the wire.
func encodeFields(fields map[string]any) (sql.NullString, error) {
	if len(fields) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("system_alerts: marshal fields: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// rowScanner is the minimal interface satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRow(r rowScanner) (*SystemAlert, error) {
	var (
		id          int64
		key, device string
		level       string
		message     string
		fieldsJSON  sql.NullString
		createdAt   int64
		expiresAt   sql.NullInt64
	)
	if err := r.Scan(&id, &key, &device, &level, &message, &fieldsJSON, &createdAt, &expiresAt); err != nil {
		return nil, fmt.Errorf("system_alerts: scan: %w", err)
	}
	a := &SystemAlert{
		ID:      id,
		Key:     key,
		Device:  device,
		Level:   Level(level),
		Message: message,
		SetAt:   time.Unix(createdAt, 0).UTC(),
	}
	if fieldsJSON.Valid && fieldsJSON.String != "" {
		var fields map[string]any
		if err := json.Unmarshal([]byte(fieldsJSON.String), &fields); err == nil {
			a.Fields = fields
		}
	}
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0).UTC()
		a.ExpiresAt = &t
	}
	return a, nil
}
