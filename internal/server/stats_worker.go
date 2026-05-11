package server

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	statsdb "github.com/sqoia-dev/clustr/internal/db/stats"
	"github.com/sqoia-dev/clustr/internal/metrics"
)

const (
	// Default stats retention in days. Configurable via CLUSTR_STATS_RETENTION_DAYS.
	defaultStatsRetentionDays = 7
	// Hard minimum/maximum for the retention setting.
	minStatsRetentionDays = 1
	maxStatsRetentionDays = 90

	// How often the sweeper runs.
	statsSweeperInterval = 5 * time.Minute

	// How often the Prometheus latest-sample cache is refreshed.
	statsPrometheusRefreshInterval = 5 * time.Second
)

// runStatsRetentionSweeper deletes node_stats rows older than the configured
// retention period. Runs on a 5-minute tick. Default retention: 7 days.
// Configurable via CLUSTR_STATS_RETENTION_DAYS (hard bounds: 1d–90d).
func (s *Server) runStatsRetentionSweeper(ctx context.Context) {
	retentionDays := defaultStatsRetentionDays
	if v := os.Getenv("CLUSTR_STATS_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			if n < minStatsRetentionDays {
				n = minStatsRetentionDays
			}
			if n > maxStatsRetentionDays {
				n = maxStatsRetentionDays
			}
			retentionDays = n
		}
	}
	retention := time.Duration(retentionDays) * 24 * time.Hour

	log.Info().
		Int("retention_days", retentionDays).
		Msg("stats sweeper: started")

	sweep := func() {
		cutoff := time.Now().Add(-retention)
		n, err := s.statsDB.DeleteOldNodeStats(ctx, cutoff)
		if err != nil {
			log.Warn().Err(err).Msg("stats sweeper: DeleteOldNodeStats failed")
			return
		}
		if n > 0 {
			log.Info().Int64("deleted", n).Dur("retention", retention).
				Msg("stats sweeper: deleted old node_stats rows")
		}
	}

	// Sweep once immediately on startup.
	sweep()

	ticker := time.NewTicker(statsSweeperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// runStatsPrometheusRefresher maintains an in-memory cache of the most-recent
// sample per (node_id, plugin, sensor) and updates the clustr_node_metric gauge
// on a 5-second refresh tick. This avoids hammering SQLite on every Prometheus
// scrape, which may be sub-second on busy monitoring stacks.
func (s *Server) runStatsPrometheusRefresher(ctx context.Context) {
	refresh := func() {
		qCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		rows, err := s.statsDB.QueryLatestNodeStats(qCtx)
		if err != nil {
			log.Warn().Err(err).Msg("stats prometheus: QueryLatestNodeStats failed")
			return
		}

		// Build node label: prefer hostname from heartbeat, fall back to node_id.
		// For simplicity in v1 we use node_id. Sprint 24 can enrich this.
		for _, r := range rows {
			metrics.NodeMetric.WithLabelValues(r.NodeID, r.Plugin, r.Sensor).Set(r.Value)
		}
	}

	// Refresh once immediately.
	refresh()

	ticker := time.NewTicker(statsPrometheusRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

// StatsDBAdapter wraps *statsdb.StatsDB for use by the stats handler.
// It bridges the db.* types used by handlers to the statsdb.* types used by StatsDB.
// Declared here rather than in handlers/ to keep the DB import chain clean.
type StatsDBAdapter struct {
	sdb *statsdb.StatsDB
}

// NewStatsDBAdapter creates a StatsDBAdapter backed by the stats DB.
func NewStatsDBAdapter(sdb *statsdb.StatsDB) *StatsDBAdapter {
	return &StatsDBAdapter{sdb: sdb}
}

// QueryNodeStats bridges db.QueryNodeStatsParams → statsdb.QueryNodeStatsParams,
// delegates to stats.db, then bridges the result back to []db.NodeStatRow.
func (a *StatsDBAdapter) QueryNodeStats(ctx context.Context, p db.QueryNodeStatsParams) ([]db.NodeStatRow, bool, error) {
	sp := statsdb.QueryNodeStatsParams{
		NodeID:         p.NodeID,
		Plugin:         p.Plugin,
		Sensor:         p.Sensor,
		Since:          p.Since,
		Until:          p.Until,
		Limit:          p.Limit,
		IncludeExpired: p.IncludeExpired,
	}
	srows, truncated, err := a.sdb.QueryNodeStats(ctx, sp)
	if err != nil {
		return nil, false, err
	}
	rows := make([]db.NodeStatRow, len(srows))
	for i, r := range srows {
		rows[i] = db.NodeStatRow{
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
	return rows, truncated, nil
}

// QueryLatestNodeStats delegates to the stats DB and bridges types back to
// []db.LatestNodeStatRow.
func (a *StatsDBAdapter) QueryLatestNodeStats(ctx context.Context) ([]db.LatestNodeStatRow, error) {
	srows, err := a.sdb.QueryLatestNodeStats(ctx)
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
