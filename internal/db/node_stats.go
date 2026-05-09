package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// NodeStatRow is a single persisted sample from node_stats.
//
// ExpiresAt is added by Sprint 38 STAT-EXPIRES. Nil means "this sample
// never expires" — the long-standing behaviour for clientd-pushed
// streaming metrics. A non-nil ExpiresAt marks a sample as TTL-bounded;
// it disappears from "current value" queries once now() passes the
// timestamp, and the daily sweeper deletes it some time after that.
type NodeStatRow struct {
	NodeID    string
	Plugin    string
	Sensor    string
	Value     float64
	Unit      string
	Labels    map[string]string
	TS        time.Time
	ExpiresAt *time.Time
}

// InsertStatsBatch persists a slice of stat rows using INSERT OR IGNORE so that
// idempotent re-delivery of unacknowledged batches (same node_id/plugin/sensor/ts)
// is a no-op rather than an error.
func (db *DB) InsertStatsBatch(ctx context.Context, rows []NodeStatRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: InsertStatsBatch: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO node_stats
			(node_id, plugin, sensor, value, unit, labels_json, ts, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("db: InsertStatsBatch: prepare: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		var labelsJSON sql.NullString
		if len(r.Labels) > 0 {
			b, err := json.Marshal(r.Labels)
			if err != nil {
				return fmt.Errorf("db: InsertStatsBatch: marshal labels: %w", err)
			}
			labelsJSON = sql.NullString{String: string(b), Valid: true}
		}

		// expires_at is nullable. Streaming clientd metrics leave it
		// nil; agent-less probes set it to TS + ttl so stale entries
		// drop out of "current views" automatically.
		var expiresAt sql.NullInt64
		if r.ExpiresAt != nil {
			expiresAt = sql.NullInt64{Int64: r.ExpiresAt.Unix(), Valid: true}
		}

		if _, err := stmt.ExecContext(ctx,
			r.NodeID,
			r.Plugin,
			r.Sensor,
			r.Value,
			nullString(r.Unit),
			labelsJSON,
			r.TS.Unix(),
			expiresAt,
		); err != nil {
			return fmt.Errorf("db: InsertStatsBatch: insert: %w", err)
		}
	}

	return tx.Commit()
}

// QueryNodeStatsParams holds filter parameters for QueryNodeStats.
//
// IncludeExpired toggles the STAT-EXPIRES filter (Sprint 38). When
// false (the default for "current values" reads), rows with
// expires_at <= now are filtered out. When true, every row in the
// time window is returned regardless of expiry — used by the audit /
// historical-trace API which still wants to surface samples that have
// since expired.
type QueryNodeStatsParams struct {
	NodeID         string
	Plugin         string // optional; empty = all plugins
	Sensor         string // optional; empty = all sensors
	Since          time.Time
	Until          time.Time
	Limit          int // hard-cap; caller sets to 10000
	IncludeExpired bool
}

// QueryNodeStats returns samples matching the given filters ordered by ts ASC.
// If more than Limit rows match, it returns Limit rows and sets the truncated flag.
func (db *DB) QueryNodeStats(ctx context.Context, p QueryNodeStatsParams) ([]NodeStatRow, bool, error) {
	if p.Limit <= 0 {
		p.Limit = 10000
	}

	// Build the query dynamically to avoid unnecessary conditions.
	q := `SELECT node_id, plugin, sensor, value, unit, labels_json, ts, expires_at
	      FROM node_stats
	      WHERE node_id = ?
	        AND ts >= ?
	        AND ts <= ?`
	args := []interface{}{p.NodeID, p.Since.Unix(), p.Until.Unix()}

	if p.Plugin != "" {
		q += " AND plugin = ?"
		args = append(args, p.Plugin)
	}
	if p.Sensor != "" {
		q += " AND sensor = ?"
		args = append(args, p.Sensor)
	}
	// Sprint 38 STAT-EXPIRES: hide stale TTL samples from "current"
	// views unless the caller explicitly opts in.
	if !p.IncludeExpired {
		q += " AND " + nodeStatsExpiresAtFilter
		args = append(args, nodeStatsExpiresAtArg(time.Now()))
	}

	// Fetch one extra row to detect truncation without an extra COUNT query.
	q += " ORDER BY ts ASC LIMIT ?"
	args = append(args, p.Limit+1)

	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("db: QueryNodeStats: %w", err)
	}
	defer rows.Close()

	var result []NodeStatRow
	for rows.Next() {
		var r NodeStatRow
		var labelsJSON sql.NullString
		var unitStr sql.NullString
		var tsUnix int64
		var expiresAt sql.NullInt64

		if err := rows.Scan(&r.NodeID, &r.Plugin, &r.Sensor, &r.Value,
			&unitStr, &labelsJSON, &tsUnix, &expiresAt); err != nil {
			return nil, false, fmt.Errorf("db: QueryNodeStats: scan: %w", err)
		}
		r.TS = time.Unix(tsUnix, 0).UTC()
		if unitStr.Valid {
			r.Unit = unitStr.String
		}
		if labelsJSON.Valid && labelsJSON.String != "" {
			if err := json.Unmarshal([]byte(labelsJSON.String), &r.Labels); err != nil {
				return nil, false, fmt.Errorf("db: QueryNodeStats: unmarshal labels: %w", err)
			}
		}
		if expiresAt.Valid {
			t := time.Unix(expiresAt.Int64, 0).UTC()
			r.ExpiresAt = &t
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("db: QueryNodeStats: rows: %w", err)
	}

	truncated := len(result) > p.Limit
	if truncated {
		result = result[:p.Limit]
	}
	return result, truncated, nil
}

// LatestNodeStatRow is a single most-recent sample per (node_id, plugin, sensor).
// Used by the Prometheus exposition endpoint.
type LatestNodeStatRow struct {
	NodeID string
	Plugin string
	Sensor string
	Value  float64
	Unit   string
	Labels map[string]string
}

// QueryLatestNodeStats returns the most recent sample per (node_id, plugin, sensor)
// for all nodes. Used to populate the Prometheus exposition cache.
//
// Sprint 38 STAT-EXPIRES: rows whose expires_at has elapsed are
// filtered out so stale agent-less probe values stop appearing in the
// /metrics scrape output once their TTL passes.
func (db *DB) QueryLatestNodeStats(ctx context.Context) ([]LatestNodeStatRow, error) {
	// Efficient SQLite query: for each (node_id, plugin, sensor) pick the row
	// with the maximum ts. The idx_node_stats_node_plugin index accelerates this.
	q := `SELECT node_id, plugin, sensor, value, unit, labels_json
	      FROM node_stats
	      WHERE ts = (
	          SELECT MAX(ts)
	          FROM node_stats AS inner
	          WHERE inner.node_id = node_stats.node_id
	            AND inner.plugin  = node_stats.plugin
	            AND inner.sensor  = node_stats.sensor
	      )
	      AND ` + nodeStatsExpiresAtFilter

	rows, err := db.sql.QueryContext(ctx, q, nodeStatsExpiresAtArg(time.Now()))
	if err != nil {
		return nil, fmt.Errorf("db: QueryLatestNodeStats: %w", err)
	}
	defer rows.Close()

	var result []LatestNodeStatRow
	for rows.Next() {
		var r LatestNodeStatRow
		var labelsJSON sql.NullString
		var unitStr sql.NullString

		if err := rows.Scan(&r.NodeID, &r.Plugin, &r.Sensor, &r.Value,
			&unitStr, &labelsJSON); err != nil {
			return nil, fmt.Errorf("db: QueryLatestNodeStats: scan: %w", err)
		}
		if unitStr.Valid {
			r.Unit = unitStr.String
		}
		if labelsJSON.Valid && labelsJSON.String != "" {
			if err := json.Unmarshal([]byte(labelsJSON.String), &r.Labels); err != nil {
				return nil, fmt.Errorf("db: QueryLatestNodeStats: unmarshal labels: %w", err)
			}
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteOldNodeStats deletes rows older than the given cutoff time.
// Called by the retention sweeper in clustr-serverd.
func (db *DB) DeleteOldNodeStats(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM node_stats WHERE ts < ?`,
		olderThan.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: DeleteOldNodeStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// nullString returns a sql.NullString that is invalid (NULL) for empty strings.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
