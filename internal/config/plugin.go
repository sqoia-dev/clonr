package config

import "github.com/sqoia-dev/clustr/pkg/api"

// Plugin is the unit of reactive config rendering. Implementations are
// stateless and registered once at server startup via Register.
//
// All methods must be safe for concurrent invocation. The observer may call
// Render on the same Plugin instance from multiple goroutines simultaneously
// when different nodes are affected by the same dirty event.
type Plugin interface {
	// Name is the stable plugin identifier used as the per-plugin tag in
	// config_push WS messages and as the plugin_name column in
	// config_render_state DB rows. Must be unique across all registered plugins.
	// Convention: lowercase, hyphenated — e.g. "hostname", "hosts", "sssd-conf".
	Name() string

	// WatchedKeys returns the set of config-tree paths this plugin depends on.
	// Each entry is either a fully-qualified path (e.g. "nodes.<id>.hostname")
	// or a path with a single trailing "*" segment that the observer expands
	// at registration time. The returned slice MUST be deterministic for a
	// given plugin version — no time, no random. The observer caches the
	// expansion.
	WatchedKeys() []string

	// Render produces the install-instructions this plugin contributes for
	// NodeID, given a snapshot of the cluster state.
	//
	// Render MUST be:
	//   - Idempotent: same ClusterState → same []api.InstallInstruction output.
	//   - Side-effect-free: no DB writes, no filesystem writes, no network
	//     calls. The diff engine may call Render speculatively and discard
	//     the output.
	//   - Pure-functional in the input: no global state outside state.
	//
	// An empty (nil) slice is valid and means "this plugin contributes nothing
	// for this node" (e.g. a controller-only plugin returns nil for compute nodes).
	Render(state ClusterState) ([]api.InstallInstruction, error)

	// Metadata returns the cross-cutting invariants the observer, push pipeline,
	// and clientd apply path consult. The result MUST be deterministic for a
	// given plugin version — no time, no random. The observer caches the result
	// at registration time.
	//
	// The zero value of PluginMetadata is "default, safe, low-priority, no
	// backup, not dangerous." A plugin that has no special requirements may
	// return PluginMetadata{} (or use DefaultPluginMetadata). See
	// internal/config/plugin_metadata.go for the priority band convention.
	Metadata() PluginMetadata
}

// PluginMetadata bundles the cross-cutting invariants a plugin declares.
// The zero value is valid and means: Priority=0, Dangerous=false, Backup=nil.
// Adding a field to this struct is a non-breaking change — every plugin gets
// the zero default for the new field until it overrides Metadata().
//
// Priority bands (document the intent, pick deliberately):
//
//	  0– 50  Foundation   — hostname, /etc/hosts, kernel sysctls
//	 51–100  Middleware   — sssd, pam, chrony
//	101–150  Applications — slurm, ssh keys, limits
//	151–200  Post-apply   — service restarts, validation probes
//
// See docs/design/sprint-41-auth-safety.md §2 for full rationale.
type PluginMetadata struct {
	// Priority orders apply within a single observer batch. Lower runs earlier.
	// Default 100 when unset (use DefaultPriority). Stable sort: equal priorities
	// preserve plugin registration order.
	// Valid range: 0–1000. Registration panics on out-of-range values.
	Priority int

	// Dangerous, when true, instructs the server to require an operator
	// confirmation token before delivering the config_push WS frame. The clientd
	// apply path is unchanged — the gate is server-side. See §4.2 of the design doc.
	// When true, DangerReason MUST be non-empty (validated at registration).
	Dangerous bool

	// DangerReason is the human-readable string surfaced in the confirmation UI
	// and CLI prompt. Empty when Dangerous is false. Required (non-empty) when
	// Dangerous is true.
	DangerReason string

	// Backup, when non-nil, instructs clientd to snapshot the listed paths before
	// applying this plugin's push. See §5 of the design doc.
	// Nil on Day 1 — wired in Day 4.
	Backup *BackupSpec
}

// BackupSpec describes the on-node snapshot clientd should take before applying
// a plugin's push.
type BackupSpec struct {
	// Paths is the list of file paths to snapshot. Each path is resolved verbatim
	// on the node — clientd does not expand globs. The plugin is responsible for
	// knowing exactly what it writes.
	Paths []string

	// RetainN is the number of snapshots to keep, oldest-first GC.
	// Default 3 when zero. Hard-capped at 16 by clientd.
	RetainN int

	// StoredAt is the directory template under which clientd writes the snapshot.
	// Tokens: <plugin>, <timestamp>. Default:
	//   /var/lib/clustr/backups/<plugin>/<timestamp>/
	StoredAt string
}

// DefaultPriority is the priority assigned to a plugin that returns
// PluginMetadata{} (zero value). It sits in the middle of the Applications
// band, compatible with Sprint 36 behavior (no ordering was enforced).
const DefaultPriority = 100
