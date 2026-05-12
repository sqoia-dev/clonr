package server

// notice_janitor.go — Sprint 43-prime Day 1
//
// Background sweeper for the notices table.
//
// Policy (founder-approved, non-negotiable):
//   - Dismissed notices older than 30 days → hard-DELETE.
//   - Un-dismissed notices (dismissed_at IS NULL) NEVER expire.
//
// Sweep cadence: every 1 hour.
//
// Per-sweep actions:
//  1. DELETE notices WHERE dismissed_at IS NOT NULL AND dismissed_at < now-30d.
//  2. If deleted > 0: log INFO + emit notice.retention_sweep audit event.

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

const (
	noticeJanitorInterval  = 1 * time.Hour
	noticeRetentionPeriod  = 30 * 24 * time.Hour // 30 days
)

// runNoticeJanitor ticks every hour and hard-deletes dismissed notices older
// than 30 days. Lifecycle: started by StartBackgroundWorkers; stops when ctx
// is cancelled.
func (s *Server) runNoticeJanitor(ctx context.Context) {
	log.Info().
		Str("interval", noticeJanitorInterval.String()).
		Str("retention", noticeRetentionPeriod.String()).
		Msg("notice janitor: started")

	ticker := time.NewTicker(noticeJanitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("notice janitor: stopping")
			return
		case <-ticker.C:
			s.sweepDismissedNotices(ctx)
		}
	}
}

// sweepDismissedNotices runs one janitor pass. Separated from the tick loop so
// the logic can be tested without a real ticker.
func (s *Server) sweepDismissedNotices(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cutoff := time.Now().UTC().Add(-noticeRetentionPeriod)
	n, err := s.db.SweepDismissedNotices(sweepCtx, cutoff)
	if err != nil {
		log.Error().Err(err).Msg("notice janitor: sweep failed")
		return
	}

	if n == 0 {
		return
	}

	log.Info().
		Int64("deleted", n).
		Msg("notice janitor: hard-deleted old dismissed notices")

	if s.audit != nil {
		s.audit.Record(sweepCtx, "system", "clustr",
			db.AuditActionNoticeRetentionSweep,
			"notice", "",
			"",
			nil,
			map[string]interface{}{
				"deleted":  n,
				"cutoff":   cutoff.Format(time.RFC3339),
				"swept_at": time.Now().UTC().Format(time.RFC3339),
			},
		)
	}
}
