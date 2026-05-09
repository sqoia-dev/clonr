// Sprint 38 STAT-REGISTRY — typed metric registry.
//
// Two registries co-exist in this package:
//
//  1. *Registry (registry.go) — the plugin-runner registry.  Holds the active
//     []Plugin and drives the tick loop.
//  2. *MetricRegistry (this file) — the typed *metric definition* registry.
//     Plugins declare metric semantics (type, unit, upper-bound, title,
//     chart-grouping hint) once at construction time.  The registry hands
//     these declarations to the server via the heartbeat so the UI can
//     resolve unit/title/chart-group for a sample without a separate
//     dashboard config.
//
// Backward compatibility:  the existing emit-by-name path (Sample{Sensor, …}
// without MetricName) keeps working.  The MetricRegistry is the new
// ergonomic top — plugins that opt in get type-checked metrics and a
// foreign-key from Sample.MetricName back to a MetricDecl.  Plugins that
// don't opt in continue to emit by name as before.
//
// Concurrency:  MetricRegistry uses a sync.RWMutex.  Register MUST be called
// at process startup, before any Collect goroutine is started.  Lookup is
// safe for concurrent use.

package stats

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// MetricType is the value-domain of a registered metric.
type MetricType string

const (
	// TypeFloat — a real-valued measurement (e.g. temperature, throughput).
	TypeFloat MetricType = "float"
	// TypeInt — an integer counter or gauge.
	TypeInt MetricType = "int"
	// TypeBool — a binary state encoded as 0/1.
	TypeBool MetricType = "bool"
)

// validTypes is the set of accepted MetricType values.  Lookups in
// validate() avoid a switch that's easy to drift from this list.
var validTypes = map[MetricType]bool{TypeFloat: true, TypeInt: true, TypeBool: true}

// MetricDecl is the typed declaration for a single metric.  It is the
// "schema row" that Sample.MetricName foreign-keys back to.
//
// JSON tags use snake_case to match the rest of the wire format
// (stats_batch / heartbeat).  Fields are intentionally tagged omitempty so
// the wire shape stays compact for declarations that omit optional fields.
type MetricDecl struct {
	// Type is one of "float", "int", "bool".  Required.
	Type MetricType `json:"type"`
	// Name is the metric identifier, e.g. "ib_rate_gbps".  Required.
	// Names are unique across (Name, Device).
	Name string `json:"name"`
	// Device, if non-empty, scopes the metric to a specific device or label
	// (e.g. "mlx5_0/1", "sda", "ctrl0").  Two metrics with the same Name
	// but different Device values are distinct entries in the registry —
	// this is the "duplicate-name-different-device" case.
	Device string `json:"device,omitempty"`
	// Unit is a free-form unit string ("celsius", "gbps", "pct", "count").
	Unit string `json:"unit,omitempty"`
	// Upper, if non-zero, is the expected upper-bound for charting axes.
	Upper float64 `json:"upper,omitempty"`
	// Title is the human-readable label shown in the UI.
	Title string `json:"title,omitempty"`
	// ChartGroup is the dashboard-chart-grouping hint (clustervisor's `ddcg`).
	// All metrics with the same ChartGroup render in one chart.
	ChartGroup string `json:"chart_group,omitempty"`
}

// Option mutates a MetricDecl during Register.  Functional options keep the
// Register call site readable when only a subset of fields is set.
type Option func(*MetricDecl)

// Device sets MetricDecl.Device.
func Device(s string) Option { return func(d *MetricDecl) { d.Device = s } }

// Unit sets MetricDecl.Unit.
func Unit(s string) Option { return func(d *MetricDecl) { d.Unit = s } }

// Upper sets MetricDecl.Upper (the chart upper-bound).
func Upper(v float64) Option { return func(d *MetricDecl) { d.Upper = v } }

// Title sets MetricDecl.Title (human-readable label).
func Title(s string) Option { return func(d *MetricDecl) { d.Title = s } }

// ChartGroup sets MetricDecl.ChartGroup (the `ddcg` chart-grouping hint).
func ChartGroup(s string) Option { return func(d *MetricDecl) { d.ChartGroup = s } }

// MetricKey is the composite (Name, Device) lookup key.
type MetricKey struct {
	Name   string
	Device string
}

// MetricRegistry holds the set of registered MetricDecl entries.
//
// THREAD-SAFETY: all exported methods are safe for concurrent use; mu guards
// the entries map.  Register acquires mu.Lock; Get/All acquire mu.RLock.
type MetricRegistry struct {
	mu      sync.RWMutex
	entries map[MetricKey]MetricDecl
}

// NewMetricRegistry constructs an empty MetricRegistry.
func NewMetricRegistry() *MetricRegistry {
	return &MetricRegistry{entries: make(map[MetricKey]MetricDecl)}
}

// Errors returned by Register.
var (
	// ErrMetricInvalidType — typ was not one of "float", "int", "bool".
	ErrMetricInvalidType = errors.New("metric registry: invalid type")
	// ErrMetricMissingName — name was empty.
	ErrMetricMissingName = errors.New("metric registry: name required")
	// ErrMetricDuplicate — (Name, Device) already registered.
	ErrMetricDuplicate = errors.New("metric registry: duplicate (name, device)")
	// ErrMetricMissingTitle — Title is required for new ergonomic registrations.
	// Without a title the UI has no human-readable label to render, which
	// defeats the purpose of having a typed registry — fail fast at Register
	// time rather than ship a metric the UI can't display.
	ErrMetricMissingTitle = errors.New("metric registry: title required (use Title(...))")
)

// Register adds a metric declaration to the registry.  Returns an error on:
//
//   - invalid typ (not "float", "int", "bool")
//   - empty name
//   - missing required Title option
//   - duplicate (Name, Device) key
//
// The (Name, Device) tuple is the unique key — registering the same name
// for two different devices is allowed and produces two distinct entries.
//
// Register is intended to be called at plugin construction time, before the
// plugin runner starts emitting samples.  Calling Register concurrently with
// Get/All is safe but the caller should still treat the registry as
// effectively-immutable post-startup.
func (r *MetricRegistry) Register(typ MetricType, name string, opts ...Option) (MetricDecl, error) {
	if !validTypes[typ] {
		return MetricDecl{}, fmt.Errorf("%w: %q (want float|int|bool)", ErrMetricInvalidType, typ)
	}
	if name == "" {
		return MetricDecl{}, ErrMetricMissingName
	}

	d := MetricDecl{Type: typ, Name: name}
	for _, opt := range opts {
		opt(&d)
	}

	if d.Title == "" {
		return MetricDecl{}, fmt.Errorf("%w: name=%q", ErrMetricMissingTitle, name)
	}

	key := MetricKey{Name: d.Name, Device: d.Device}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; exists {
		return MetricDecl{}, fmt.Errorf("%w: name=%q device=%q", ErrMetricDuplicate, key.Name, key.Device)
	}
	r.entries[key] = d
	return d, nil
}

// MustRegister is the panicking variant of Register, for cases where a
// registration error indicates a programmer bug (constants in the plugin
// source).  Plugins that register at init/construction time should use this
// — there is no runtime recovery for a typo in a metric name.
func (r *MetricRegistry) MustRegister(typ MetricType, name string, opts ...Option) MetricDecl {
	d, err := r.Register(typ, name, opts...)
	if err != nil {
		panic("stats: MustRegister: " + err.Error())
	}
	return d
}

// Get returns the MetricDecl for (name, device), or false if not registered.
func (r *MetricRegistry) Get(name, device string) (MetricDecl, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.entries[MetricKey{Name: name, Device: device}]
	return d, ok
}

// All returns a snapshot of every registered metric, sorted by (Name, Device)
// for stable output.  Callers may freely mutate the returned slice; entries
// themselves are values (no shared pointers) so mutation is local.
func (r *MetricRegistry) All() []MetricDecl {
	r.mu.RLock()
	out := make([]MetricDecl, 0, len(r.entries))
	for _, d := range r.entries {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Device < out[j].Device
	})
	return out
}

// Len returns the number of registered metrics.
func (r *MetricRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Sample is a convenience method that constructs a stats.Sample with
// MetricName pre-populated from the registry entry.  Panics if (name, device)
// is not registered — callers should always Register before Sample.
//
// This is the ergonomic emit path — plugins that opt into the typed registry
// use reg.Sample("ib_rate_gbps", "mlx5_0/1", 100.0) and the foreign-key is
// guaranteed to resolve server-side.  Plugins emitting by name continue to
// construct Sample directly without going through the registry.
func (r *MetricRegistry) Sample(name, device string, value float64) Sample {
	d, ok := r.Get(name, device)
	if !ok {
		panic(fmt.Sprintf("stats: MetricRegistry.Sample: unknown metric (name=%q, device=%q)", name, device))
	}
	s := Sample{
		Sensor:     d.Name,
		Value:      value,
		Unit:       d.Unit,
		MetricName: d.Name,
	}
	if device != "" {
		s.Labels = map[string]string{"device": device}
	}
	return s
}
