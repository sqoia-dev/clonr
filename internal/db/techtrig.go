package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TechTrigName is the canonical identifier for a TECH-TRIG signal (D27 Bucket 2).
type TechTrigName string

const (
	// TechTrigPostgreSQL (T1) fires when the SQLite cluster reaches scale limits:
	// node count >= 500 OR write contention >= 5 events/sec sustained over a 5-minute window.
	// Decision: 500 nodes is 10x the expected single-cluster size for our Persona A/B target;
	// 5 contention events/sec sustained for 5 minutes suggests actual write saturation, not
	// transient spikes. Both thresholds are documented in docs/tech-triggers.md.
	TechTrigPostgreSQL TechTrigName = "t1_postgresql"

	// TechTrigFramework (T2) fires when the frontend exceeds the Alpine/vanilla JS ceiling.
	// Metric A: total LOC across static/*.js files (excluding vendor/) >= 5000.
	// Metric B: operator-set manual_signal flag for "framework friction hit" (e.g. a specific
	// Alpine x-data pattern that requires architectural workaround to implement correctly).
	// 5000 LOC matches D21's explicit threshold. D23 adopted Alpine+HTMX in Sprint C;
	// this trigger gates the next re-evaluation of D23.
	TechTrigFramework TechTrigName = "t2_framework"

	// TechTrigMultiTenant (T3) fires when the operator marks multi_tenant_signal as true.
	// Clustr is single-tenant today; this is a purely manual signal. The operator sets it
	// when planning a hosted-clustr-as-service deployment or when >=3 logically-separate
	// fleets need isolation (per D27 Bucket 2 trigger condition).
	TechTrigMultiTenant TechTrigName = "t3_multitenant"

	// TechTrigLogArchive (T4) fires when total audit log + node log storage exceeds 50 GiB.
	// Rationale: the single-binary self-hosted model targets operators with <500 nodes;
	// at 50 GiB of log data the SQLite WAL overhead and vacuum cycles become measurable.
	// 50 GiB is a conservative threshold that gives operators headroom before actual
	// performance impact. See docs/tech-triggers.md for the hot/cold archive sprint spec.
	TechTrigLogArchive TechTrigName = "t4_log_archive"
)

// TechTrigState is the in-memory representation of one row in tech_trig_state.
type TechTrigState struct {
	TriggerName      TechTrigName
	CurrentValueJSON string
	ThresholdJSON    string
	FiredAt          *time.Time
	LastEvaluatedAt  *time.Time
	ManualSignal     bool
}

// Fired reports whether this trigger has fired (fired_at IS NOT NULL).
func (t TechTrigState) Fired() bool { return t.FiredAt != nil }

// ListTechTrigStates returns all four trigger rows ordered by trigger_name.
func (db *DB) ListTechTrigStates(ctx context.Context) ([]TechTrigState, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT trigger_name, current_value_json, threshold_json,
		       fired_at, last_evaluated_at, manual_signal
		FROM tech_trig_state
		ORDER BY trigger_name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list tech trig states: %w", err)
	}
	defer rows.Close()
	return scanTechTrigRows(rows)
}

// GetTechTrigState returns the state for a single trigger. Returns ErrNotFound if
// the trigger_name is unknown.
func (db *DB) GetTechTrigState(ctx context.Context, name TechTrigName) (TechTrigState, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT trigger_name, current_value_json, threshold_json,
		       fired_at, last_evaluated_at, manual_signal
		FROM tech_trig_state
		WHERE trigger_name = ?
	`, string(name))
	states, err := scanTechTrigRows(&ttSingleRow{row: row})
	if err != nil {
		return TechTrigState{}, err
	}
	if len(states) == 0 {
		return TechTrigState{}, fmt.Errorf("tech trig %q: not found", name)
	}
	return states[0], nil
}

// UpdateTechTrigState writes current_value_json, last_evaluated_at, and conditionally
// sets fired_at (only sets it once — never clears it except via ResetTechTrig).
func (db *DB) UpdateTechTrigState(ctx context.Context, name TechTrigName, currentValueJSON string, fired bool) error {
	now := time.Now().Unix()
	if fired {
		// Set fired_at only on the first fire (COALESCE preserves existing value).
		_, err := db.sql.ExecContext(ctx, `
			UPDATE tech_trig_state
			SET current_value_json  = ?,
			    last_evaluated_at   = ?,
			    fired_at            = COALESCE(fired_at, ?)
			WHERE trigger_name = ?
		`, currentValueJSON, now, now, string(name))
		return err
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE tech_trig_state
		SET current_value_json = ?,
		    last_evaluated_at  = ?
		WHERE trigger_name = ?
	`, currentValueJSON, now, string(name))
	return err
}

// ResetTechTrig clears fired_at and manual_signal for the given trigger.
// This is the admin "Reset to Not Fired" action.
func (db *DB) ResetTechTrig(ctx context.Context, name TechTrigName) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE tech_trig_state
		SET fired_at      = NULL,
		    manual_signal = 0
		WHERE trigger_name = ?
	`, string(name))
	if err != nil {
		return fmt.Errorf("db: reset tech trig %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tech trig %q: not found", name)
	}
	return nil
}

// SetTechTrigManualSignal sets (or clears) the manual_signal flag for the given
// trigger and, if signal=true, sets fired_at if not already set.
func (db *DB) SetTechTrigManualSignal(ctx context.Context, name TechTrigName, signal bool) error {
	now := time.Now().Unix()
	var err error
	if signal {
		_, err = db.sql.ExecContext(ctx, `
			UPDATE tech_trig_state
			SET manual_signal = 1,
			    fired_at      = COALESCE(fired_at, ?)
			WHERE trigger_name = ?
		`, now, string(name))
	} else {
		_, err = db.sql.ExecContext(ctx, `
			UPDATE tech_trig_state
			SET manual_signal = 0
			WHERE trigger_name = ?
		`, string(name))
	}
	if err != nil {
		return fmt.Errorf("db: set tech trig manual signal %q: %w", name, err)
	}
	return nil
}

// WasAlreadyFired reports whether the trigger had a non-NULL fired_at before
// the most recent UpdateTechTrigState call. Used by the background worker to
// detect the false→true transition (so we only send one notification per firing).
func (db *DB) WasTechTrigAlreadyFired(ctx context.Context, name TechTrigName) (bool, error) {
	var firedAt sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `
		SELECT fired_at FROM tech_trig_state WHERE trigger_name = ?
	`, string(name)).Scan(&firedAt)
	if err != nil {
		return false, fmt.Errorf("db: check fired status %q: %w", name, err)
	}
	return firedAt.Valid, nil
}

// CountNodes returns the current number of registered nodes. Used by T1 evaluation.
func (db *DB) CountNodes(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_configs`).Scan(&n)
	return n, err
}

// MeasureLogBytes returns the approximate total byte size of audit log + node logs.
// Uses page_count * page_size as an upper-bound estimate without reading every row.
// This is a fast pragma-based measurement, not an exact byte count.
func (db *DB) MeasureLogBytes(ctx context.Context) (int64, error) {
	// SQLite page_count * page_size gives the total allocated DB size in bytes.
	// We want just the log tables, so we use the sqlite_stat1 row count approach:
	// approximate by summing row counts across audit_log and node_logs, multiplied
	// by an average row size estimate (500 bytes/row is conservative for audit_log,
	// 200 bytes/row for node_logs).
	//
	// Rationale: pragma page_count counts the whole file; there's no per-table byte
	// count in SQLite without reading every page. Row count * average row size is
	// accurate enough for a 50 GiB threshold check.
	var auditRows, nodeLogRows int64
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&auditRows)
	_ = db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_logs`).Scan(&nodeLogRows)
	// audit_log rows are ~800 bytes (JSON + metadata); node_logs rows are ~300 bytes.
	return auditRows*800 + nodeLogRows*300, nil
}

// CountJSLinesInDir counts non-blank, non-comment lines in *.js files under dir,
// excluding any file path containing "/vendor/". Called by T2 evaluation.
// Returns 0 on any filesystem error (non-fatal for metric purposes).

// TechTrigHistoryRecord is one row in the audit log for trigger events.
// The audit_log table (existing) is the backing store; we query it by action prefix.
type TechTrigHistoryRecord struct {
	ID          string
	TriggerName string
	Action      string // "trigger_fired" | "trigger_reset" | "trigger_signal"
	ActorID     string
	ActorLabel  string
	CreatedAt   time.Time
}

// ListTechTrigHistory returns audit log entries for tech trigger events.
// Filters to actions matching "tech_trig.*" ordered newest-first.
func (db *DB) ListTechTrigHistory(ctx context.Context, limit int) ([]TechTrigHistoryRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, resource_id, action, actor_id, actor_label, created_at
		FROM audit_log
		WHERE action LIKE 'tech_trig.%'
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list tech trig history: %w", err)
	}
	defer rows.Close()
	var out []TechTrigHistoryRecord
	for rows.Next() {
		var rec TechTrigHistoryRecord
		var createdAt int64
		if err := rows.Scan(&rec.ID, &rec.TriggerName, &rec.Action, &rec.ActorID, &rec.ActorLabel, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan tech trig history: %w", err)
		}
		rec.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ─── internal helpers ────────────────────────────────────────────────────────

// techTrigRowScanner is satisfied by both *sql.Rows and the ttSingleRow adapter.
// Named distinctly to avoid conflict with the simpler rowScanner interface in users.go.
type techTrigRowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

// ttSingleRow wraps *sql.Row to satisfy techTrigRowScanner for single-row queries.
type ttSingleRow struct {
	row    *sql.Row
	called bool
	err    error
}

func (s *ttSingleRow) Next() bool {
	if s.called {
		return false
	}
	s.called = true
	return true
}
func (s *ttSingleRow) Scan(dest ...any) error { s.err = s.row.Scan(dest...); return s.err }
func (s *ttSingleRow) Close() error           { return nil }
func (s *ttSingleRow) Err() error             { return s.err }

func scanTechTrigRows(rows techTrigRowScanner) ([]TechTrigState, error) {
	defer rows.Close()
	var out []TechTrigState
	for rows.Next() {
		var t TechTrigState
		var name string
		var firedAt, lastEvalAt sql.NullInt64
		var manualSignal int
		if err := rows.Scan(&name, &t.CurrentValueJSON, &t.ThresholdJSON,
			&firedAt, &lastEvalAt, &manualSignal); err != nil {
			return nil, fmt.Errorf("db: scan tech trig row: %w", err)
		}
		t.TriggerName = TechTrigName(name)
		if firedAt.Valid {
			ts := time.Unix(firedAt.Int64, 0).UTC()
			t.FiredAt = &ts
		}
		if lastEvalAt.Valid {
			ts := time.Unix(lastEvalAt.Int64, 0).UTC()
			t.LastEvaluatedAt = &ts
		}
		t.ManualSignal = manualSignal == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan tech trig rows: %w", err)
	}
	return out, nil
}

// T1ValueJSON marshals the T1 current-value payload.
func T1ValueJSON(nodeCount int, contentionRate float64) (string, error) {
	b, err := json.Marshal(map[string]interface{}{
		"node_count":      nodeCount,
		"contention_rate": contentionRate,
	})
	return string(b), err
}

// T2ValueJSON marshals the T2 current-value payload.
func T2ValueJSON(jsLOC int) (string, error) {
	b, err := json.Marshal(map[string]interface{}{
		"js_loc": jsLOC,
	})
	return string(b), err
}

// T4ValueJSON marshals the T4 current-value payload.
func T4ValueJSON(logBytes int64) (string, error) {
	b, err := json.Marshal(map[string]interface{}{
		"log_bytes": logBytes,
	})
	return string(b), err
}
