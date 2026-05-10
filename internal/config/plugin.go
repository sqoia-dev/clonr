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
}
