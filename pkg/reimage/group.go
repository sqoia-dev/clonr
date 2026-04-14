package reimage

// group.go — rolling group reimage orchestration.
//
// TriggerGroupReimage fans out reimages to every node in a group with:
//   - configurable concurrency cap (default 5)
//   - pause-on-failure-pct: if >N% of the first concurrency-sized wave fail,
//     the job transitions to "paused" and stops dispatching until the operator
//     resumes it via POST /api/v1/reimages/jobs/:id/resume.
//
// Each node reimage is dispatched as a goroutine capped by a semaphore.
// The function blocks until all dispatched goroutines finish. Job progress
// counters are updated atomically in the database after each node completes.
//
// Boot routing: identical to single-node Trigger — reimage_pending is set
// and the PXE server handles routing on next boot. No SetNextBoot() calls.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/db"
)

// TriggerGroupReimage creates a GroupReimageJob, fans out reimages to all
// members, and returns the job ID for status polling. The actual dispatch
// runs in a background goroutine so this function returns immediately after
// creating the job record.
//
// Returns (jobID, error). On error the job record is not written.
func (o *Orchestrator) TriggerGroupReimage(
	ctx context.Context,
	groupID, imageID string,
	concurrency, pauseOnFailurePct int,
) (string, error) {
	if concurrency <= 0 {
		concurrency = 5
	}
	if pauseOnFailurePct <= 0 {
		pauseOnFailurePct = 20
	}

	// Verify group exists.
	if _, err := o.DB.GetNodeGroup(ctx, groupID); err != nil {
		return "", fmt.Errorf("group reimage: load group %s: %w", groupID, err)
	}

	// Load members.
	members, err := o.DB.ListGroupMembers(ctx, groupID)
	if err != nil {
		return "", fmt.Errorf("group reimage: list members for group %s: %w", groupID, err)
	}
	if len(members) == 0 {
		return "", fmt.Errorf("group reimage: group %s has no members", groupID)
	}

	now := time.Now().UTC()
	jobID := uuid.New().String()
	job := db.GroupReimageJob{
		ID:                jobID,
		GroupID:           groupID,
		ImageID:           imageID,
		Concurrency:       concurrency,
		PauseOnFailurePct: pauseOnFailurePct,
		Status:            "running",
		TotalNodes:        len(members),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := o.DB.CreateGroupReimageJob(ctx, job); err != nil {
		return "", fmt.Errorf("group reimage: create job record: %w", err)
	}

	o.Logger.Info().
		Str("job_id", jobID).
		Str("group_id", groupID).
		Str("image_id", imageID).
		Int("total_nodes", len(members)).
		Int("concurrency", concurrency).
		Int("pause_on_failure_pct", pauseOnFailurePct).
		Msg("group reimage: dispatching rolling reimage")

	// Dispatch background goroutine — this returns immediately to the caller.
	go o.runGroupReimage(context.Background(), job, members)

	return jobID, nil
}

// runGroupReimage executes the rolling reimage in a background goroutine.
// It splits members into waves of size=concurrency, checks the pause threshold
// after each wave, and stops if the job transitions to "paused" or "failed".
func (o *Orchestrator) runGroupReimage(ctx context.Context, job db.GroupReimageJob, members []api.NodeConfig) {
	var (
		succeeded int64
		failed    int64
		triggered int64
	)

	// Build per-node reimage requests upfront so each goroutine just calls Trigger.
	// CreateReimageRequest is called in the wave loop to keep DB writes minimal.

	stagger := powerCycleStagger()

	// Process members in waves of size=concurrency.
	for waveStart := 0; waveStart < len(members); waveStart += job.Concurrency {
		waveEnd := waveStart + job.Concurrency
		if waveEnd > len(members) {
			waveEnd = len(members)
		}
		wave := members[waveStart:waveEnd]

		// Check if the job was paused (e.g. by the failure threshold).
		currentJob, err := o.DB.GetGroupReimageJob(ctx, job.ID)
		if err == nil && currentJob.Status != "running" {
			o.Logger.Warn().
				Str("job_id", job.ID).
				Str("status", currentJob.Status).
				Msg("group reimage: job is no longer running — stopping dispatch")
			return
		}

		// Dispatch this wave concurrently via goroutines.
		var wg sync.WaitGroup
		var waveFailed int64
		var waveSucceeded int64

		for i, node := range wave {
			wg.Add(1)
			go func(n api.NodeConfig, pos int) {
				defer wg.Done()

				// Create the individual reimage request.
				reqID, reqErr := o.createReimageRequest(ctx, n.ID, job.ImageID)
				if reqErr != nil {
					o.Logger.Error().Err(reqErr).
						Str("job_id", job.ID).
						Str("node_id", n.ID).
						Msg("group reimage: failed to create reimage request")
					atomic.AddInt64(&waveFailed, 1)
					atomic.AddInt64(&failed, 1)
					return
				}

				// Stagger power cycles within the wave.
				if stagger > 0 && pos > 0 {
					select {
					case <-ctx.Done():
						return
					case <-time.After(stagger * time.Duration(pos)):
					}
				}

				if trigErr := o.Trigger(ctx, reqID); trigErr != nil {
					o.Logger.Error().Err(trigErr).
						Str("job_id", job.ID).
						Str("node_id", n.ID).
						Msg("group reimage: node reimage trigger failed")
					atomic.AddInt64(&waveFailed, 1)
					atomic.AddInt64(&failed, 1)
				} else {
					atomic.AddInt64(&waveSucceeded, 1)
					atomic.AddInt64(&succeeded, 1)
				}
				atomic.AddInt64(&triggered, 1)

				// Update job progress after each node completes.
				j := db.GroupReimageJob{
					ID:                job.ID,
					GroupID:           job.GroupID,
					ImageID:           job.ImageID,
					Concurrency:       job.Concurrency,
					PauseOnFailurePct: job.PauseOnFailurePct,
					Status:            "running",
					TotalNodes:        job.TotalNodes,
					TriggeredNodes:    int(atomic.LoadInt64(&triggered)),
					SucceededNodes:    int(atomic.LoadInt64(&succeeded)),
					FailedNodes:       int(atomic.LoadInt64(&failed)),
				}
				if updateErr := o.DB.UpdateGroupReimageJob(ctx, j); updateErr != nil {
					o.Logger.Error().Err(updateErr).Str("job_id", job.ID).
						Msg("group reimage: failed to update job progress")
				}
			}(node, i)
		}

		wg.Wait()

		// Check pause-on-failure threshold after the wave.
		waveTotal := int64(len(wave))
		if waveTotal > 0 && waveFailed > 0 {
			failPct := int((waveFailed * 100) / waveTotal)
			if failPct > job.PauseOnFailurePct {
				o.Logger.Warn().
					Str("job_id", job.ID).
					Int("fail_pct", failPct).
					Int("threshold", job.PauseOnFailurePct).
					Msg("group reimage: failure threshold exceeded — pausing job")

				pauseJob := db.GroupReimageJob{
					ID:                job.ID,
					GroupID:           job.GroupID,
					ImageID:           job.ImageID,
					Concurrency:       job.Concurrency,
					PauseOnFailurePct: job.PauseOnFailurePct,
					Status:            "paused",
					TotalNodes:        job.TotalNodes,
					TriggeredNodes:    int(atomic.LoadInt64(&triggered)),
					SucceededNodes:    int(atomic.LoadInt64(&succeeded)),
					FailedNodes:       int(atomic.LoadInt64(&failed)),
					ErrorMessage:      fmt.Sprintf("paused: %d%% of wave failed (threshold: %d%%)", failPct, job.PauseOnFailurePct),
				}
				if updateErr := o.DB.UpdateGroupReimageJob(ctx, pauseJob); updateErr != nil {
					o.Logger.Error().Err(updateErr).Str("job_id", job.ID).
						Msg("group reimage: failed to mark job as paused")
				}
				return
			}
		}
	}

	// All waves complete — finalize the job.
	finalStatus := "complete"
	if atomic.LoadInt64(&failed) > 0 {
		finalStatus = "failed"
	}
	finalJob := db.GroupReimageJob{
		ID:                job.ID,
		GroupID:           job.GroupID,
		ImageID:           job.ImageID,
		Concurrency:       job.Concurrency,
		PauseOnFailurePct: job.PauseOnFailurePct,
		Status:            finalStatus,
		TotalNodes:        job.TotalNodes,
		TriggeredNodes:    int(atomic.LoadInt64(&triggered)),
		SucceededNodes:    int(atomic.LoadInt64(&succeeded)),
		FailedNodes:       int(atomic.LoadInt64(&failed)),
	}
	if err := o.DB.UpdateGroupReimageJob(ctx, finalJob); err != nil {
		o.Logger.Error().Err(err).Str("job_id", job.ID).
			Msg("group reimage: failed to finalize job")
	}

	o.Logger.Info().
		Str("job_id", job.ID).
		Str("status", finalStatus).
		Int("succeeded", int(atomic.LoadInt64(&succeeded))).
		Int("failed", int(atomic.LoadInt64(&failed))).
		Msg("group reimage: complete")
}

// createReimageRequest inserts a reimage_requests row for one node and returns its ID.
func (o *Orchestrator) createReimageRequest(ctx context.Context, nodeID, imageID string) (string, error) {
	now := time.Now().UTC()
	reqID := uuid.New().String()
	req := api.ReimageRequest{
		ID:          reqID,
		NodeID:      nodeID,
		ImageID:     imageID,
		Status:      api.ReimageStatusPending,
		RequestedBy: "group-reimage",
		CreatedAt:   now,
	}
	if err := o.DB.CreateReimageRequest(ctx, req); err != nil {
		return "", fmt.Errorf("create reimage request: %w", err)
	}
	return reqID, nil
}
