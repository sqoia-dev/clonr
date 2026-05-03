package server

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// runReimagePendingReaper runs hourly and finds node_configs rows where
// reimage_pending=TRUE but no reimage_requests row is in a non-terminal state.
// These are orphaned flags — e.g. if the server crashed after setting
// reimage_pending but before the reimage was triggered. Left uncleaned they
// cause the PXE server to deploy the node on every boot, potentially looping.
//
// Clears reimage_pending and logs a warning for each recovered node. (S4-3)
func (s *Server) runReimagePendingReaper(ctx context.Context) {
	reap := func() {
		ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		nodes, err := s.db.ListNodeConfigs(ctx2, "")
		if err != nil {
			log.Warn().Err(err).Msg("reimage-pending reaper: ListNodeConfigs failed")
			return
		}

		for _, node := range nodes {
			if !node.ReimagePending {
				continue
			}
			// Check for any non-terminal reimage request.
			active, err := s.db.GetActiveReimageForNode(ctx2, node.ID)
			if err != nil {
				log.Warn().Err(err).Str("node_id", node.ID).
					Msg("reimage-pending reaper: GetActiveReimageForNode failed (skipping)")
				continue
			}
			if active != nil {
				// There is a live reimage in progress — reimage_pending is correct.
				continue
			}

			// Orphaned reimage_pending flag — clear it.
			if err := s.db.SetReimagePending(ctx2, node.ID, false); err != nil {
				log.Error().Err(err).
					Str("node_id", node.ID).
					Str("hostname", node.Hostname).
					Msg("reimage-pending reaper: failed to clear orphaned reimage_pending flag")
				continue
			}
			log.Warn().
				Str("node_id", node.ID).
				Str("hostname", node.Hostname).
				Msg("reimage-pending reaper: cleared orphaned reimage_pending flag (no active reimage request found)")
		}
	}

	// Run once at startup, then hourly.
	reap()

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reap()
		}
	}
}

// resumeRunningGroupReimageJobs is called at server startup (S4-4). It queries
// for any group_reimage_jobs rows with status='running' from a prior process and
// resumes them by re-dispatching the remaining nodes. Without this, a server
// restart mid-group-reimage leaves the job stuck in 'running' with partial
// progress and no goroutine driving it.
//
// Resume strategy: reload the group members and re-trigger only nodes that do
// not already have a non-terminal reimage request. This avoids double-reimaging
// nodes that were already triggered before the crash.
func (s *Server) resumeRunningGroupReimageJobs(ctx context.Context) {
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	jobs, err := s.db.ListRunningGroupReimageJobs(ctx2)
	if err != nil {
		log.Warn().Err(err).Msg("group-reimage resume: ListRunningGroupReimageJobs failed")
		return
	}
	if len(jobs) == 0 {
		return
	}

	log.Info().Int("count", len(jobs)).Msg("group-reimage resume: found running jobs from prior process — resuming")

	for _, job := range jobs {
		j := job // capture for goroutine
		go func() {
			if err := s.reimageOrchestrator.ResumeGroupReimageJob(context.Background(), j.ID); err != nil {
				log.Warn().Err(err).Str("job_id", j.ID).
					Msg("group-reimage resume: ResumeGroupReimageJob failed (non-fatal)")
			} else {
				log.Info().Str("job_id", j.ID).Str("group_id", j.GroupID).
					Msg("group-reimage resume: job resumed successfully")
			}
		}()
	}
}
