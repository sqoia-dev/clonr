package server

// clientd_stats_adapter.go — wires the clientd handler's DB interface so that
// InsertStatsBatch goes to stats.db while all other methods delegate to clustr.db.
//
// Sprint 42 STATS-DB-SPLIT: clientd pushes stats_batch messages over the
// WebSocket; those writes must land in stats.db, not clustr.db. The other
// methods on ClientdDBIface (heartbeat, log batch, node config, LDAP ready)
// remain on clustr.db.

import (
	"context"

	"github.com/sqoia-dev/clustr/internal/db"
	statsdb "github.com/sqoia-dev/clustr/internal/db/stats"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// clientdDBAdapter implements handlers.ClientdDBIface by routing:
//   - InsertStatsBatch → stats.db
//   - everything else  → clustr.db
type clientdDBAdapter struct {
	main    *db.DB
	statsDB *statsdb.StatsDB
}

// newClientdDBAdapter creates a clientdDBAdapter.
func newClientdDBAdapter(main *db.DB, sdb *statsdb.StatsDB) *clientdDBAdapter {
	return &clientdDBAdapter{main: main, statsDB: sdb}
}

// InsertStatsBatch routes the batch write to stats.db, converting types.
func (a *clientdDBAdapter) InsertStatsBatch(ctx context.Context, rows []db.NodeStatRow) error {
	srows := make([]statsdb.NodeStatRow, len(rows))
	for i, r := range rows {
		srows[i] = statsdb.NodeStatRow{
			NodeID:    r.NodeID,
			Plugin:    r.Plugin,
			Sensor:    r.Sensor,
			Value:     r.Value,
			Unit:      r.Unit,
			Labels:    r.Labels,
			TS:        r.TS,
			ExpiresAt: r.ExpiresAt,
		}
	}
	return a.statsDB.InsertStatsBatch(ctx, srows)
}

// The following methods delegate to clustr.db unchanged.

func (a *clientdDBAdapter) UpsertHeartbeat(ctx context.Context, nodeID string, hb *db.HeartbeatRow) error {
	return a.main.UpsertHeartbeat(ctx, nodeID, hb)
}

func (a *clientdDBAdapter) GetHeartbeat(ctx context.Context, nodeID string) (*db.HeartbeatRow, error) {
	return a.main.GetHeartbeat(ctx, nodeID)
}

func (a *clientdDBAdapter) UpdateLastSeen(ctx context.Context, nodeID string) error {
	return a.main.UpdateLastSeen(ctx, nodeID)
}

func (a *clientdDBAdapter) InsertLogBatch(ctx context.Context, entries []api.LogEntry) error {
	return a.main.InsertLogBatch(ctx, entries)
}

func (a *clientdDBAdapter) GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error) {
	return a.main.GetNodeConfig(ctx, id)
}

func (a *clientdDBAdapter) LDAPNodeIsConfigured(ctx context.Context, nodeID string) (bool, error) {
	return a.main.LDAPNodeIsConfigured(ctx, nodeID)
}

func (a *clientdDBAdapter) RecordNodeLDAPReady(ctx context.Context, nodeID string, ready bool, detail string) error {
	return a.main.RecordNodeLDAPReady(ctx, nodeID, ready, detail)
}
