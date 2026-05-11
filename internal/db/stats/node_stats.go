package stats

// node_stats.go — stats-domain operations on the node_stats table in stats.db.
//
// This mirrors internal/db/node_stats.go but operates on *StatsDB instead of
// *db.DB. The types (NodeStatRow, QueryNodeStatsParams, LatestNodeStatRow) are
// re-exported from this package so callers use stats.NodeStatRow rather than
// db.NodeStatRow when talking to the stats DB. Adapters in internal/server/
// bridge the two type universes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// NodeStatRow is a single persisted sample from node_stats in stats.db.
// Identical in shape to db.NodeStatRow but owned by this package.
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

// QueryNodeStatsParams holds filter parameters for QueryNodeStats.
// Identical in shape to db.QueryNodeStatsParams.
type QueryNodeStatsParams struct {
	NodeID         string
	Plugin         string // optional; empty = all plugins
	Sensor         string // optional; empty = all sensors
	Since          time.Time
	Until          time.Time
	Limit          int  // hard-cap; caller sets to 10000
	IncludeExpired bool
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

// InsertStatsBatch persists a slice of stat rows using INSERT OR IGNORE.
// Idempotent on (node_id, plugin, sensor, ts) — re-delivery of unacknowledged
// batches is a no-op.
func (db *StatsDB) InsertStatsBatch(ctx context.Context, rows []NodeStatRow) error {
	if len(rows) == 0 {
		return nil
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("stats: InsertStatsBatch: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO node_stats
			(node_id, plugin, sensor, value, unit, labels_json, ts, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("stats: InsertStatsBatch: prepare: %w", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		var labelsJSON sql.NullString
		if len(r.Labels) > 0 {
			b, err := json.Marshal(r.Labels)
			if err != nil {
				return fmt.Errorf("stats: InsertStatsBatch: marshal labels: %w", err)
			}
			labelsJSON = sql.NullString{String: string(b), Valid: true}
		}

		var expiresAt sql.NullInt64
		if r.ExpiresAt != nil {
			expiresAt = sql.NullInt64{Int64: r.ExpiresAt.Unix(), Valid: true}
		}

		if _, err := stmt.ExecContext(ctx,
			r.NodeID,
			r.Plugin,
			r.Sensor,
			r.Value,
			nullStr(r.Unit),
			labelsJSON,
			r.TS.Unix(),
			expiresAt,
		); err != nil {
			return fmt.Errorf("stats: InsertStatsBatch: insert: %w", err)
		}
	}

	return tx.Commit()
}

// QueryNodeStats returns samples matching the given filters ordered by ts ASC.
// If more than Limit rows match, it returns Limit rows and sets the truncated flag.
func (db *StatsDB) QueryNodeStats(ctx context.Context, p QueryNodeStatsParams) ([]NodeStatRow, bool, error) {
	if p.Limit <= 0 {
		p.Limit = 10000
	}

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

	hasTimeRange := !p.Since.IsZero() || !p.Until.IsZero()
	if !p.IncludeExpired && !hasTimeRange {
		q += " AND " + nodeStatsExpiresAtFilter
		args = append(args, nodeStatsExpiresAtArg(time.Now()))
	}

	q += " ORDER BY ts ASC LIMIT ?"
	args = append(args, p.Limit+1)

	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, fmt.Errorf("stats: QueryNodeStats: %w", err)
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
			return nil, false, fmt.Errorf("stats: QueryNodeStats: scan: %w", err)
		}
		r.TS = time.Unix(tsUnix, 0).UTC()
		if unitStr.Valid {
			r.Unit = unitStr.String
		}
		if labelsJSON.Valid && labelsJSON.String != "" {
			if err := json.Unmarshal([]byte(labelsJSON.String), &r.Labels); err != nil {
				return nil, false, fmt.Errorf("stats: QueryNodeStats: unmarshal labels: %w", err)
			}
		}
		if expiresAt.Valid {
			t := time.Unix(expiresAt.Int64, 0).UTC()
			r.ExpiresAt = &t
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("stats: QueryNodeStats: rows: %w", err)
	}

	truncated := len(result) > p.Limit
	if truncated {
		result = result[:p.Limit]
	}
	return result, truncated, nil
}

// QueryLatestNodeStats returns the most recent sample per (node_id, plugin, sensor)
// for all nodes. Expired samples (expires_at <= now) are filtered out.
func (db *StatsDB) QueryLatestNodeStats(ctx context.Context) ([]LatestNodeStatRow, error) {
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
		return nil, fmt.Errorf("stats: QueryLatestNodeStats: %w", err)
	}
	defer rows.Close()

	var result []LatestNodeStatRow
	for rows.Next() {
		var r LatestNodeStatRow
		var labelsJSON sql.NullString
		var unitStr sql.NullString

		if err := rows.Scan(&r.NodeID, &r.Plugin, &r.Sensor, &r.Value,
			&unitStr, &labelsJSON); err != nil {
			return nil, fmt.Errorf("stats: QueryLatestNodeStats: scan: %w", err)
		}
		if unitStr.Valid {
			r.Unit = unitStr.String
		}
		if labelsJSON.Valid && labelsJSON.String != "" {
			if err := json.Unmarshal([]byte(labelsJSON.String), &r.Labels); err != nil {
				return nil, fmt.Errorf("stats: QueryLatestNodeStats: unmarshal labels: %w", err)
			}
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteOldNodeStats deletes node_stats rows older than the given cutoff.
func (db *StatsDB) DeleteOldNodeStats(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM node_stats WHERE ts < ?`,
		olderThan.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("stats: DeleteOldNodeStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SweepExpiredNodeStats deletes node_stats rows whose expires_at has elapsed.
// Streaming clientd metrics (expires_at IS NULL) are unaffected.
func (db *StatsDB) SweepExpiredNodeStats(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM node_stats
		WHERE expires_at IS NOT NULL
		  AND expires_at < ?
	`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("stats: SweepExpiredNodeStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// nodeStatsExpiresAtFilter is the WHERE-clause fragment that removes expired samples.
const nodeStatsExpiresAtFilter = `(expires_at IS NULL OR expires_at > ?)`

// nodeStatsExpiresAtArg returns the bind argument for nodeStatsExpiresAtFilter.
func nodeStatsExpiresAtArg(now time.Time) int64 { return now.Unix() }

// nullStr returns a sql.NullString that is invalid (NULL) for empty strings.
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
