package db

// node_external_stats — Sprint 38 Bundle A.
//
// This table stores the "latest sample" written by clustr-serverd's
// agent-less collectors (PROBE-3 reachability probes, ipmi-sensors BMC
// sweep, gosnmp switch/PDU sweep). One row per (node_id, source); each
// poll overwrites the previous row via UPSERT.
//
// The streaming time-series in node_stats is unrelated; do not mix the
// two. node_stats is for clientd-pushed samples that need historical
// charting. node_external_stats is a key/value snapshot the UI reads
// to answer "is this node reachable right now?" and "what did the BMC
// say in the last poll?".

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExternalStatsSource enumerates the agent-less collectors. Mirrors the
// "source" column on node_external_stats — keep them in sync.
type ExternalStatsSource string

const (
	// ExternalSourceProbe holds the PROBE-3 reachability booleans
	// (ping, ssh, ipmi-mc) plus the timestamp at which they were last
	// checked.
	ExternalSourceProbe ExternalStatsSource = "probe"
	// ExternalSourceBMC holds the most-recent ipmi-sensors sweep:
	// per-sensor name → reading.
	ExternalSourceBMC ExternalStatsSource = "bmc"
	// ExternalSourceSNMP holds the most-recent gosnmp sweep:
	// per-OID → value.
	ExternalSourceSNMP ExternalStatsSource = "snmp"
	// ExternalSourceIPMI is reserved for raw IPMI commands beyond
	// the sensor sweep (mc info, FRU). Currently unused.
	ExternalSourceIPMI ExternalStatsSource = "ipmi"
)

// NodeExternalStatRow is one persisted external-collector sample.
//
// Payload is the collector-specific JSON shape. Callers cast it to
// their own struct (ProbePayload, BMCPayload, SNMPPayload). This keeps
// schema migrations forward-compatible when a collector grows new
// fields.
type NodeExternalStatRow struct {
	NodeID     string
	Source     ExternalStatsSource
	Payload    json.RawMessage
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// UpsertExternalStat writes (or replaces) the latest sample for
// (node_id, source). The collector pool calls this once per poll cycle
// per (node, source).
func (db *DB) UpsertExternalStat(ctx context.Context, r NodeExternalStatRow) error {
	if r.NodeID == "" {
		return fmt.Errorf("db: UpsertExternalStat: node_id is empty")
	}
	if r.Source == "" {
		return fmt.Errorf("db: UpsertExternalStat: source is empty")
	}
	if len(r.Payload) == 0 {
		// Treat empty payload as a "{}" object — collectors with no
		// data still mark the sample as present so freshness checks
		// work.
		r.Payload = json.RawMessage(`{}`)
	}
	// SQLite doesn't enforce JSON validity; do a cheap sanity check
	// here so we surface bad payloads in tests rather than on read.
	if !json.Valid(r.Payload) {
		return fmt.Errorf("db: UpsertExternalStat: payload is not valid JSON")
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
		return fmt.Errorf("db: UpsertExternalStat: %w", err)
	}
	return nil
}

// ListExternalStatsForNode returns every non-expired external sample
// for a single node, regardless of source. The API handler uses this
// to assemble the {probes, samples, last_seen, expires_at} envelope.
//
// Stale rows (expires_at <= now) are filtered out so the "current"
// view reflects only live data. The sweeper deletes stale rows on a
// daily cadence; this filter handles the in-between window.
func (db *DB) ListExternalStatsForNode(
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
		return nil, fmt.Errorf("db: ListExternalStatsForNode: %w", err)
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
			return nil, fmt.Errorf("db: ListExternalStatsForNode: scan: %w", err)
		}
		r.Source = ExternalStatsSource(source)
		r.Payload = json.RawMessage(payloadStr)
		r.LastSeenAt = time.Unix(lastSeenTS, 0).UTC()
		r.ExpiresAt = time.Unix(expiresAtTS, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: ListExternalStatsForNode: rows: %w", err)
	}
	return out, nil
}

// SweepExpiredExternalStats deletes every row whose expires_at is
// earlier than the supplied cutoff. Returns the number of rows
// deleted. Called daily from the sweeper goroutine.
func (db *DB) SweepExpiredExternalStats(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM node_external_stats WHERE expires_at < ?`,
		cutoff.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: SweepExpiredExternalStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SweepExpiredNodeStats deletes node_stats rows whose expires_at has
// elapsed (i.e. expires_at IS NOT NULL AND expires_at < cutoff).
// Streaming clientd metrics that leave expires_at NULL are unaffected
// — the existing 7-day retention sweeper handles those.
func (db *DB) SweepExpiredNodeStats(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM node_stats
		WHERE expires_at IS NOT NULL
		  AND expires_at < ?
	`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("db: SweepExpiredNodeStats: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// nodeStatsExpiresAtFilter is the WHERE-clause fragment that removes
// expired samples from "current values" queries. Exported as a string
// constant so multiple call sites (stats handler, Prometheus exporter)
// stay in sync.
const nodeStatsExpiresAtFilter = `(expires_at IS NULL OR expires_at > ?)`

// nodeStatsExpiresAtArg returns the bind argument used with
// nodeStatsExpiresAtFilter — Unix seconds for "now".
func nodeStatsExpiresAtArg(now time.Time) int64 { return now.Unix() }

