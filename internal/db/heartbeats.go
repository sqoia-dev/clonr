package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/clientd"
)

// HeartbeatRow is the DB representation of a node heartbeat.
// Numeric fields may be zero if the node did not supply them.
type HeartbeatRow struct {
	NodeID      string
	ReceivedAt  time.Time
	UptimeSec   float64
	Load1       float64
	Load5       float64
	Load15      float64
	MemTotalKB  int64
	MemAvailKB  int64
	DiskUsage   []clientd.DiskUsage
	Services    []clientd.ServiceStatus
	Kernel      string
	ClientdVer  string
}

// UpsertHeartbeat inserts or replaces the heartbeat row for nodeID.
func (db *DB) UpsertHeartbeat(ctx context.Context, nodeID string, hb *HeartbeatRow) error {
	diskJSON, err := json.Marshal(hb.DiskUsage)
	if err != nil {
		return fmt.Errorf("db: heartbeat: marshal disk_usage: %w", err)
	}
	svcJSON, err := json.Marshal(hb.Services)
	if err != nil {
		return fmt.Errorf("db: heartbeat: marshal services: %w", err)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT OR REPLACE INTO node_heartbeats
			(node_id, received_at, uptime_sec, load_1, load_5, load_15,
			 mem_total_kb, mem_avail_kb, disk_usage, services, kernel, clientd_ver)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		nodeID,
		hb.ReceivedAt.Unix(),
		hb.UptimeSec,
		hb.Load1,
		hb.Load5,
		hb.Load15,
		hb.MemTotalKB,
		hb.MemAvailKB,
		string(diskJSON),
		string(svcJSON),
		hb.Kernel,
		hb.ClientdVer,
	)
	return err
}

// GetHeartbeat returns the most recent heartbeat for nodeID.
// Returns sql.ErrNoRows if no heartbeat has been received yet.
func (db *DB) GetHeartbeat(ctx context.Context, nodeID string) (*HeartbeatRow, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT node_id, received_at, uptime_sec, load_1, load_5, load_15,
		       mem_total_kb, mem_avail_kb, disk_usage, services, kernel, clientd_ver
		FROM node_heartbeats
		WHERE node_id = ?
	`, nodeID)

	var r HeartbeatRow
	var receivedAt int64
	var diskJSON, svcJSON string

	err := row.Scan(
		&r.NodeID,
		&receivedAt,
		&r.UptimeSec,
		&r.Load1,
		&r.Load5,
		&r.Load15,
		&r.MemTotalKB,
		&r.MemAvailKB,
		&diskJSON,
		&svcJSON,
		&r.Kernel,
		&r.ClientdVer,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("db: GetHeartbeat: %w", err)
	}

	r.ReceivedAt = time.Unix(receivedAt, 0).UTC()

	if diskJSON != "" && diskJSON != "null" {
		if err := json.Unmarshal([]byte(diskJSON), &r.DiskUsage); err != nil {
			return nil, fmt.Errorf("db: GetHeartbeat: unmarshal disk_usage: %w", err)
		}
	}
	if svcJSON != "" && svcJSON != "null" {
		if err := json.Unmarshal([]byte(svcJSON), &r.Services); err != nil {
			return nil, fmt.Errorf("db: GetHeartbeat: unmarshal services: %w", err)
		}
	}

	return &r, nil
}

// UpdateLastSeen sets node_configs.last_seen_at to the current time for nodeID.
// This is called on every successful heartbeat and hello from clustr-clientd.
func (db *DB) UpdateLastSeen(ctx context.Context, nodeID string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_configs SET last_seen_at = ? WHERE id = ?
	`, now, nodeID)
	return err
}
