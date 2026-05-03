package server

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/metrics"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// runMetricsCollector ticks every 30 seconds and refreshes all Prometheus gauge
// metrics that require querying the database or filesystem.
// Counter metrics (clustr_deploy_total, clustr_api_requests_total,
// clustr_flipback_failures_total, clustr_webhook_deliveries_total) are
// incremented inline at the site of each event — this loop handles gauges only.
func (s *Server) runMetricsCollector(ctx context.Context) {
	// Pre-seed the known node state labels so Prometheus emits them even if
	// counts are zero. This prevents gaps in Grafana dashboards.
	knownStates := []api.NodeState{
		api.NodeStateRegistered,
		api.NodeStateConfigured,
		api.NodeStateDeploying,
		api.NodeStateDeployed,
		api.NodeStateReimagePending,
		api.NodeStateFailed,
		api.NodeStateDeployedPreboot,
		api.NodeStateDeployedVerified,
		api.NodeStateDeployVerifyTimeout,
	}
	for _, st := range knownStates {
		metrics.NodeCount.WithLabelValues(string(st)).Set(0)
	}

	collect := func() {
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		// --- node counts by state ---
		nodes, err := s.db.ListNodeConfigs(ctx2, "")
		if err != nil {
			log.Warn().Err(err).Msg("metrics collector: ListNodeConfigs failed")
		} else {
			stateCounts := make(map[api.NodeState]float64)
			for _, st := range knownStates {
				stateCounts[st] = 0
			}
			for _, n := range nodes {
				stateCounts[n.State()]++
			}
			for st, cnt := range stateCounts {
				metrics.NodeCount.WithLabelValues(string(st)).Set(cnt)
			}
		}

		// --- active deploys (non-terminal reimage requests) ---
		active, err := s.db.CountActiveReimages(ctx2)
		if err != nil {
			log.Warn().Err(err).Msg("metrics collector: CountActiveReimages failed")
		} else {
			metrics.ActiveDeploys.Set(float64(active))
		}

		// --- database file size ---
		if info, err := os.Stat(s.cfg.DBPath); err == nil {
			metrics.DBSizeBytes.Set(float64(info.Size()))
		}

		// --- image disk usage ---
		total, err := dirSize(s.cfg.ImageDir)
		if err != nil {
			log.Warn().Err(err).Msg("metrics collector: image dir size failed")
		} else {
			metrics.ImageDiskBytes.Set(float64(total))
		}
	}

	// Collect once immediately so metrics are populated before the first scrape.
	collect()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

// dirSize returns the total byte size of all regular files under root.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if !d.IsDir() {
			if info, iErr := d.Info(); iErr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
