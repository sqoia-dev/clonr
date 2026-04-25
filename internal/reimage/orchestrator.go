// Package reimage orchestrates node reimaging: it assigns a new base image,
// sets reimage_pending = true (so the PXE server routes the next boot to deploy),
// calls SetNextBoot(PXE) on the power provider to force PXE as the next boot
// device (required for UEFI nodes where a prior deploy may have moved the OS
// entry to the top of the NVRAM boot order), power-cycles the node, and tracks
// the request lifecycle in the database.
package reimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/power"
)

// defaultReimageMaxConcurrent is the default maximum number of in-flight reimages.
// Bursts above this are held until the next scheduler tick (30s). This prevents
// IPMI BMCs from being rate-limited or crashing under simultaneous reboot storms.
const defaultReimageMaxConcurrent = 20

// defaultPowerCycleStagger is the default pause between consecutive power cycles
// within a batch. IPMI BMCs on many commodity boards have firmware bugs where
// rapid successive chassis-power-reset commands are silently dropped or cause
// the BMC to become unresponsive for 10-30 seconds. 2s stagger is conservative
// enough to avoid this without materially slowing fleet reimage time.
const defaultPowerCycleStagger = 2 * time.Second

// reimageMaxConcurrent reads CLUSTR_REIMAGE_MAX_CONCURRENT from env, falling
// back to defaultReimageMaxConcurrent.
func reimageMaxConcurrent() int {
	if v := os.Getenv("CLUSTR_REIMAGE_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultReimageMaxConcurrent
}

// powerCycleStagger reads CLUSTR_POWER_CYCLE_STAGGER from env as a Go duration,
// falling back to defaultPowerCycleStagger.
func powerCycleStagger() time.Duration {
	if v := os.Getenv("CLUSTR_POWER_CYCLE_STAGGER"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return defaultPowerCycleStagger
}

// Orchestrator wires together the database, power registry, and logging to
// execute reimage requests.
type Orchestrator struct {
	DB       *db.DB
	Registry *power.Registry
	Logger   zerolog.Logger
}

// New constructs an Orchestrator.
func New(database *db.DB, registry *power.Registry, logger zerolog.Logger) *Orchestrator {
	return &Orchestrator{
		DB:       database,
		Registry: registry,
		Logger:   logger.With().Str("component", "reimage").Logger(),
	}
}

// Trigger executes an immediate reimage for the request identified by reqID.
//
// Steps:
//  1. Load the reimage request and node config.
//  2. Validate a power provider is configured.
//  3. Assign node.base_image_id to the requested image.
//  4. Set node.reimage_pending = true (PXE handler reads this on next boot).
//  5. Call provider.SetNextBoot(PXE) to force PXE as the next boot device.
//     This is required for UEFI nodes where efibootmgr placed an OS entry
//     ahead of PXE after a previous successful deploy. Best-effort: failure
//     logs a warning but does not abort the reimage.
//  6. Call provider.PowerCycle() to trigger the reboot (skipped for dry_run).
//  7. Update the reimage request status to "triggered".
//
// If any step fails after the DB writes have started, the request status is
// set to "failed" with the error message before returning.
func (o *Orchestrator) Trigger(ctx context.Context, reqID string) error {
	req, err := o.DB.GetReimageRequest(ctx, reqID)
	if err != nil {
		return fmt.Errorf("reimage trigger: load request %s: %w", reqID, err)
	}

	node, err := o.DB.GetNodeConfig(ctx, req.NodeID)
	if err != nil {
		return fmt.Errorf("reimage trigger: load node %s: %w", req.NodeID, err)
	}

	// Resolve the power provider. Fall back to building one from the legacy
	// BMC config when PowerProvider is not explicitly configured.
	provider, err := o.resolveProvider(node)
	if err != nil {
		failErr := o.DB.UpdateReimageRequestStatus(ctx, reqID, api.ReimageStatusFailed, err.Error())
		if failErr != nil {
			o.Logger.Error().Err(failErr).Str("req_id", reqID).Msg("failed to mark request as failed after provider resolution error")
		}
		return fmt.Errorf("reimage trigger: resolve provider for node %s: %w", node.ID, err)
	}

	// Assign the target image and set reimage_pending before touching the BMC.
	// If we fail here nothing has changed on the node — safe to retry.
	node.BaseImageID = req.ImageID
	if err := o.DB.UpdateNodeConfig(ctx, node); err != nil {
		_ = o.DB.UpdateReimageRequestStatus(ctx, reqID, api.ReimageStatusFailed, err.Error())
		return fmt.Errorf("reimage trigger: assign image on node %s: %w", node.ID, err)
	}
	if err := o.DB.SetReimagePending(ctx, node.ID, true); err != nil {
		_ = o.DB.UpdateReimageRequestStatus(ctx, reqID, api.ReimageStatusFailed, err.Error())
		return fmt.Errorf("reimage trigger: set reimage_pending on node %s: %w", node.ID, err)
	}

	o.Logger.Info().
		Str("req_id", reqID).
		Str("node_id", node.ID).
		Str("node_hostname", node.Hostname).
		Str("image_id", req.ImageID).
		Bool("dry_run", req.DryRun).
		Msg("reimage_pending set — PXE server will route next boot to deploy")

	if req.DryRun {
		// Dry run: reimage_pending is set but skip the power cycle. The node
		// will deploy on next natural reboot (PXE first in boot order).
		o.Logger.Info().
			Str("req_id", reqID).
			Str("node_id", node.ID).
			Msg("dry_run=true — skipping power cycle; node will deploy on next PXE boot")
	} else {
		// Set PXE as the next boot device before power-cycling. This is
		// required for UEFI nodes where a previous successful deploy left an
		// OS entry at the top of the NVRAM boot order. Best-effort: a failure
		// here logs a warning but does not abort the reimage — nodes that
		// already have PXE first will boot correctly regardless.
		if err := provider.SetNextBoot(ctx, power.BootPXE); err != nil {
			o.Logger.Warn().
				Err(err).
				Str("req_id", reqID).
				Str("node_id", node.ID).
				Str("node_hostname", node.Hostname).
				Msg("SetNextBoot(PXE) failed — proceeding with power cycle anyway; node must have PXE first in boot order")
		} else {
			o.Logger.Info().
				Str("req_id", reqID).
				Str("node_id", node.ID).
				Str("node_hostname", node.Hostname).
				Msg("next boot set to PXE")
		}

		o.Logger.Info().
			Str("req_id", reqID).
			Str("node_id", node.ID).
			Str("node_hostname", node.Hostname).
			Msg("power cycling node to trigger reimage")

		if err := provider.PowerCycle(ctx); err != nil {
			errMsg := fmt.Sprintf("PowerCycle failed: %v", err)
			_ = o.DB.UpdateReimageRequestStatus(ctx, reqID, api.ReimageStatusFailed, errMsg)
			return fmt.Errorf("reimage trigger: %s (node %s)", errMsg, node.ID)
		}
	}

	if err := o.DB.UpdateReimageRequestStatus(ctx, reqID, api.ReimageStatusTriggered, ""); err != nil {
		// Non-fatal: the power cycle succeeded; log but don't fail the caller.
		o.Logger.Error().Err(err).Str("req_id", reqID).Msg("power cycle succeeded but failed to update request status")
	}

	o.Logger.Info().
		Str("req_id", reqID).
		Str("node_id", node.ID).
		Str("node_hostname", node.Hostname).
		Bool("dry_run", req.DryRun).
		Msg("reimage triggered successfully")

	return nil
}

// Scheduler starts a background goroutine that polls for scheduled reimage
// requests every 30 seconds and triggers them when their scheduled_at time
// has passed. It returns when ctx is cancelled.
func (o *Orchestrator) Scheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	o.Logger.Info().Msg("reimage scheduler started")

	for {
		select {
		case <-ctx.Done():
			o.Logger.Info().Msg("reimage scheduler stopped")
			return
		case <-ticker.C:
			o.runScheduled(ctx)
		}
	}
}

// runScheduled fetches and triggers overdue scheduled requests, enforcing
// CLUSTR_REIMAGE_MAX_CONCURRENT and CLUSTR_POWER_CYCLE_STAGGER limits.
//
// Requests beyond the concurrency cap are skipped this tick and will be
// retried on the next 30-second scheduler tick. This keeps the scheduler
// simple (stateless) — the DB holds all pending requests as ground truth.
//
// Power cycles are staggered by CLUSTR_POWER_CYCLE_STAGGER (default 2s) to
// avoid triggering IPMI BMC firmware bugs that drop rapid successive commands.
func (o *Orchestrator) runScheduled(ctx context.Context) {
	reqs, err := o.DB.ListPendingScheduledRequests(ctx, time.Now())
	if err != nil {
		o.Logger.Error().Err(err).Msg("scheduler: failed to list pending scheduled requests")
		return
	}
	if len(reqs) == 0 {
		return
	}

	maxConcurrent := reimageMaxConcurrent()
	stagger := powerCycleStagger()

	if len(reqs) > maxConcurrent {
		o.Logger.Warn().
			Int("pending", len(reqs)).
			Int("max_concurrent", maxConcurrent).
			Int("deferred", len(reqs)-maxConcurrent).
			Msg("scheduler: reimage concurrency cap reached — deferring excess requests to next tick")
		reqs = reqs[:maxConcurrent]
	}

	for i, req := range reqs {
		if ctx.Err() != nil {
			return // server shutting down
		}
		o.Logger.Info().
			Str("req_id", req.ID).
			Str("node_id", req.NodeID).
			Time("scheduled_at", *req.ScheduledAt).
			Int("batch_pos", i+1).
			Int("batch_size", len(reqs)).
			Msg("scheduler: triggering scheduled reimage")
		if err := o.Trigger(ctx, req.ID); err != nil {
			o.Logger.Error().Err(err).Str("req_id", req.ID).Msg("scheduler: reimage trigger failed")
		}
		// Stagger power cycles to avoid IPMI BMC rate-limiting.
		// Skip the sleep after the last item in the batch.
		if stagger > 0 && i < len(reqs)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(stagger):
			}
		}
	}
}

// resolveProvider returns a power.Provider for the given node. It prefers the
// explicit PowerProvider config; falls back to building an IPMI provider from
// the legacy BMC config when PowerProvider is nil.
func (o *Orchestrator) resolveProvider(node api.NodeConfig) (power.Provider, error) {
	if node.PowerProvider != nil && node.PowerProvider.Type != "" {
		return o.Registry.Create(power.ProviderConfig{
			Type:   node.PowerProvider.Type,
			Fields: node.PowerProvider.Fields,
		})
	}

	// Legacy BMC fallback: build an IPMI provider from BMC credentials.
	if node.BMC != nil && node.BMC.IPAddress != "" {
		return o.Registry.Create(power.ProviderConfig{
			Type: "ipmi",
			Fields: map[string]string{
				"host":     node.BMC.IPAddress,
				"username": node.BMC.Username,
				"password": node.BMC.Password,
			},
		})
	}

	return nil, errors.New("node has no power provider configured — set power_provider or bmc credentials")
}
