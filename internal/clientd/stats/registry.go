package stats

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultInterval = 30 * time.Second
	minInterval     = 5 * time.Second
	maxInterval     = 600 * time.Second
)

// Registry holds the set of active plugins and drives the collection tick loop.
type Registry struct {
	plugins  []Plugin
	interval time.Duration
}

// NewRegistry creates a Registry with the default plugin set and tick interval.
// The interval is read from CLUSTR_STATS_INTERVAL (seconds). Invalid or
// out-of-range values are silently clamped to [5s, 600s].
func NewRegistry() *Registry {
	interval := defaultInterval
	if v := os.Getenv("CLUSTR_STATS_INTERVAL"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			d := time.Duration(secs) * time.Second
			if d < minInterval {
				d = minInterval
			}
			if d > maxInterval {
				d = maxInterval
			}
			interval = d
		}
	}
	return &Registry{interval: interval}
}

// Register adds a plugin to the registry. Call before Run.
func (r *Registry) Register(p Plugin) {
	r.plugins = append(r.plugins, p)
}

// Interval returns the configured collection interval.
func (r *Registry) Interval() time.Duration {
	return r.interval
}

// Run starts the tick loop. On each tick it calls every plugin's Collect method
// and passes the resulting batch to emit. Run blocks until ctx is cancelled.
// emit is called with the plugin name and its samples; it is never called with
// an empty sample slice. emit must not block — the registry does not retry.
func (r *Registry) Run(ctx context.Context, emit func(plugin string, samples []Sample)) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Collect once immediately so the first batch arrives within seconds of
	// startup rather than waiting a full interval.
	r.collect(ctx, emit)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.collect(ctx, emit)
		}
	}
}

// collect runs all plugins for one tick and calls emit per plugin.
func (r *Registry) collect(ctx context.Context, emit func(plugin string, samples []Sample)) {
	tickCtx, cancel := context.WithTimeout(ctx, r.interval/2)
	defer cancel()

	for _, p := range r.plugins {
		samples := func() (ss []Sample) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error().
						Str("plugin", p.Name()).
						Interface("panic", rec).
						Msg("stats: plugin panicked during Collect — recovered")
					ss = nil
				}
			}()
			return p.Collect(tickCtx)
		}()

		if len(samples) == 0 {
			continue
		}

		// Stamp zero timestamps with now.
		now := time.Now().UTC()
		for i := range samples {
			if samples[i].TS.IsZero() {
				samples[i].TS = now
			}
		}

		emit(p.Name(), samples)
	}
}
