package server

// dangerous_push_janitor.go — Sprint 41 hygiene
//
// Background sweeper for the pending_dangerous_pushes staging table.
// Rows accumulate on every POST /api/v1/config/dangerous-push; they are
// consumed (consumed=1) on successful confirmation or after 3 strikes, but
// nothing removes them from the table. Without this janitor the table grows
// without bound.
//
// Sweep cadence: every 10 minutes (matching the row TTL so there is at most
// one TTL-worth of dead rows at any point in time).
//
// Per-sweep actions:
//   1. Collect IDs of expired-but-unconfirmed rows (for per-row audit events).
//   2. DELETE all consumed=1 OR expires_at < now rows in a single transaction.
//   3. Emit dangerous_push.expired audit event for each expired-unconfirmed ID.
//   4. If total deleted > 0, emit a summary dangerous_push.janitor audit event.
//   5. Log the deleted count at INFO level.

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// dangerousPushJanitorInterval matches pendingStageTTL in handlers/dangerous_push.go.
// Running the janitor at the same cadence as the TTL bounds dead-row accumulation
// to at most one TTL window.
const dangerousPushJanitorInterval = 10 * time.Minute

// runDangerousPushJanitor ticks every 10 minutes, removes stale rows from
// pending_dangerous_pushes, and emits audit events for expired-unconfirmed rows.
// Lifecycle: started by StartBackgroundWorkers; stops when ctx is cancelled.
func (s *Server) runDangerousPushJanitor(ctx context.Context) {
	log.Info().
		Str("interval", dangerousPushJanitorInterval.String()).
		Msg("dangerous-push janitor: started")

	ticker := time.NewTicker(dangerousPushJanitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("dangerous-push janitor: stopping")
			return
		case <-ticker.C:
			s.sweepDangerousPushes(ctx)
		}
	}
}

// sweepDangerousPushes runs one janitor pass. Separated from the tick loop so
// the logic can be tested without a real ticker.
func (s *Server) sweepDangerousPushes(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	now := time.Now().UTC()
	expiredIDs, totalDeleted, err := s.db.JanitorSweepDangerousPushes(sweepCtx, now)
	if err != nil {
		log.Error().Err(err).Msg("dangerous-push janitor: sweep failed")
		return
	}

	if totalDeleted == 0 {
		return
	}

	log.Info().
		Int64("deleted", totalDeleted).
		Int("expired_unconfirmed", len(expiredIDs)).
		Msg("dangerous-push janitor: swept stale rows")

	// Emit one dangerous_push.expired audit event per expired-unconfirmed row.
	// These are the operationally interesting events: a row that was staged but
	// never confirmed within the TTL window.
	for _, id := range expiredIDs {
		if s.audit != nil {
			s.audit.Record(sweepCtx, "system", "clustr",
				db.AuditActionDangerousPushExpired,
				"pending_dangerous_push", id,
				"",
				nil,
				map[string]interface{}{
					"reason": "expired_unconfirmed",
					"swept_at": now.Format(time.RFC3339),
				},
			)
		}
	}

	// Emit a summary janitor event when any rows were swept.
	if s.audit != nil {
		s.audit.Record(sweepCtx, "system", "clustr",
			db.AuditActionDangerousPushJanitor,
			"pending_dangerous_push", "",
			"",
			nil,
			map[string]interface{}{
				"total_deleted":       totalDeleted,
				"expired_unconfirmed": len(expiredIDs),
				"swept_at":            now.Format(time.RFC3339),
			},
		)
	}
}
