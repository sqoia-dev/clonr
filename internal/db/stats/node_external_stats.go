package stats

// node_external_stats.go — stats-domain operations on node_external_stats
// in stats.db.
//
// Mirrors internal/db/node_external_stats.go but operates on *StatsDB.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExternalStatsSource enumerates the agent-less collectors.
// Mirrors db.ExternalStatsSource — keep them in sync.
type ExternalStatsSource string

const (
	ExternalSourceProbe ExternalStatsSource = "probe"
	ExternalSourceBMC   ExternalStatsSource = "bmc"
	ExternalSourceSNMP  ExternalStatsSource = "snmp"
	ExternalSourceIPMI  ExternalStatsSource = "ipmi"
)

// NodeExternalStatRow is one persisted external-collector sample in stats.db.
type NodeExternalStatRow struct {
	NodeID     string
	Source     ExternalStatsSource
	Payload    json.RawMessage
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// UpsertExternalStat writes (or replaces) the latest sample for (node_id, source).
func (db *StatsDB) UpsertExternalStat(ctx context.Context, r NodeExternalStatRow) error {
	if r.NodeID == "" {
		return fmt.Errorf("stats: UpsertExternalStat: node_id is empty")
	}
	if r.Source == "" {
		return fmt.Errorf("stats: UpsertExternalStat: source is empty")
	}
	if len(r.Payload) == 0 {
		r.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(r.Payload) {
		return fmt.Errorf("stats: UpsertExternalStat: payload is not valid JSON")
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO node_external_stats (node_id, source, payload_json, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (node_id, source) DO UPDATE SET
			payload_json = excluded.payload_json,
			last_seen_at = excluded.last_seen_at,
			expires_at   = excluded.expires_at
	`,
		r.NodeID,
		string(r.Source),
		string(r.Payload),
		r.LastSeenAt.Unix(),
		r.ExpiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("stats: UpsertExternalStat: %w", err)
	}
	return nil
}

// ListExternalStatsForNode returns every non-expired external sample for a node.
func (db *StatsDB) ListExternalStatsForNode(
	ctx context.Context,
	nodeID string,
	now time.Time,
) ([]NodeExternalStatRow, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, source, payload_json, last_seen_at, expires_at
		FROM node_external_stats
		WHERE node_id = ?
		  AND expires_at > ?
	`, nodeID, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("stats: ListExternalStatsForNode: %w", err)
	}
	defer rows.Close()

	var out []NodeExternalStatRow
	for rows.Next() {
		var (
			r           NodeExternalStatRow
			source      string
			payloadStr  string
			lastSeenTS  int64
			expiresAtTS int64
		)
		if err := rows.Scan(&r.NodeID, &source, &payloadStr, &lastSeenTS, &expiresAtTS); err != nil {
			return nil, fmt.Errorf("stats: ListExternalStatsForNode: scan: %w", err)
		}
		r.Source = ExternalStatsSource(source)
		r.Payload = json.RawMessage(payloadStr)
		r.LastSeenAt = time.Unix(lastSeenTS, 0).UTC()
		r.ExpiresAt = time.Unix(expiresAtTS, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: ListExternalStatsForNode: rows: %w", err)
	}
	return out, nil
}

// SweepExpiredExternalStats deletes rows whose expires_at < cutoff.
func (db *StatsDB) SweepExpiredExternalStats(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM node_external_stats WHERE expires_at < ?`,
		cutoff.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("stats: SweepExpiredExternalStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
