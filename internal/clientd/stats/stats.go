// Package stats provides the on-node stats collection framework for clustr-clientd.
//
// Architecture:
//
//	Plugin (interface) — named, self-contained collector (cpu, memory, disks, …)
//	Registry          — holds the active plugin set, drives the tick loop
//	Buffer            — bounded ring buffer for offline batches (see buffer.go)
//
// The tick interval defaults to 30s and is configurable via CLUSTR_STATS_INTERVAL.
// Valid range: 5s–600s. Values outside this range are clamped.
package stats

import (
	"context"
	"time"
)

// Plugin is the interface every stats plugin must implement.
// Each plugin is responsible for a single domain (cpu, memory, etc.).
// Collect is called once per tick; it must be safe to call concurrently from
// the same goroutine (the registry never calls it from multiple goroutines).
// Plugins must not panic — errors should be logged and an empty slice returned.
type Plugin interface {
	// Name returns the stable plugin identifier used in the DB and Prometheus.
	// Must be lowercase, no spaces: "cpu", "memory", "disks", etc.
	Name() string

	// Collect runs one collection cycle and returns the resulting samples.
	// ctx carries a per-tick deadline. Implementations should respect it.
	Collect(ctx context.Context) []Sample
}

// Sample is a single measurement produced by a plugin during one collection cycle.
type Sample struct {
	// Sensor is the specific measurement within the plugin, e.g. "load1", "used_pct".
	Sensor string `json:"sensor"`

	// Value is the numeric measurement.
	Value float64 `json:"value"`

	// Unit describes the value's unit of measure: "pct", "bytes", "count",
	// "celsius", "bps", "iops", "gbps", "seconds". Empty is valid.
	Unit string `json:"unit,omitempty"`

	// Labels are optional key/value pairs for per-device/per-interface disambiguation.
	// E.g. {"iface":"eth0"} or {"device":"sda"}.
	Labels map[string]string `json:"labels,omitempty"`

	// TS is the timestamp of the collection. Plugins may override this to reflect
	// when the underlying kernel counter was read; defaults to now if zero.
	TS time.Time `json:"ts"`

	// MetricName, if non-empty, foreign-keys back to a typed MetricDecl
	// registered via the MetricRegistry (Sprint 38 STAT-REGISTRY).  Plugins
	// that pre-date the registry leave this empty; the existing emit-by-name
	// path keeps working unchanged.  When set, the server uses it to resolve
	// unit, title, upper-bound, and chart-grouping hints for the UI without a
	// separate dashboard config.
	MetricName string `json:"metric_name,omitempty"`
}
