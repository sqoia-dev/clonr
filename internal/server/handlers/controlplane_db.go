package handlers

import (
	"context"

	"github.com/sqoia-dev/clustr/internal/db"
	statsdb "github.com/sqoia-dev/clustr/internal/db/stats"
)

// controlPlaneDBAdapter wraps *db.DB and *statsdb.StatsDB to satisfy
// ControlPlaneDBIface. Stats queries go to statsDB (Sprint 42 STATS-DB-SPLIT).
type controlPlaneDBAdapter struct {
	db      *db.DB
	statsDB *statsdb.StatsDB
}

// NewControlPlaneDBAdapter wraps a *db.DB and *statsdb.StatsDB for use by
// ControlPlaneHandler. QueryLatestNodeStats is routed to the stats DB.
func NewControlPlaneDBAdapter(database *db.DB, sdb *statsdb.StatsDB) ControlPlaneDBIface {
	return &controlPlaneDBAdapter{db: database, statsDB: sdb}
}

func (a *controlPlaneDBAdapter) GetControlPlaneHost(ctx context.Context) (db.Host, error) {
	return a.db.GetControlPlaneHost(ctx)
}

func (a *controlPlaneDBAdapter) QueryLatestNodeStats(ctx context.Context) ([]db.LatestNodeStatRow, error) {
	srows, err := a.statsDB.QueryLatestNodeStats(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]db.LatestNodeStatRow, len(srows))
	for i, r := range srows {
		rows[i] = db.LatestNodeStatRow{
			NodeID: r.NodeID,
			Plugin: r.Plugin,
			Sensor: r.Sensor,
			Value:  r.Value,
			Unit:   r.Unit,
			Labels: r.Labels,
		}
	}
	return rows, nil
}
