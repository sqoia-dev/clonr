// Package plugins contains the concrete reactive-config plugin implementations
// for clustr-serverd. Each plugin implements config.Plugin and is registered
// once at server startup via config.Register.
//
// Day 2 of the Sprint 36 rollout plan ships the hostname plugin only. Further
// plugins (hosts, sssd, limits, …) follow in Day 3.
package plugins

import (
	"fmt"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// hostnameWatchKey is the config-tree path the hostname plugin subscribes to.
//
// Shape rationale: the observer (Bundle A, observer.go) maps WatchedKeys
// entries verbatim into watchIndex for O(1) lookup. The Notify call in
// UpdateNode emits this same literal key so the two sides match without
// requiring wildcard expansion (which is a Day 3+ observer enhancement).
// Using a single shared key means one Notify wakes the plugin once per
// hostname-change event; the ClusterState.NodeID field carries the
// identity of the changed node.
const hostnameWatchKey = "nodes.*.hostname"

// HostnamePlugin renders /etc/hostname for the node identified by
// state.NodeID.
//
// Render contract:
//   - Pure and idempotent: same NodeID + same Hostname → same output.
//   - No anchors: /etc/hostname is single-purpose; a full overwrite is safe.
//   - Returns a nil slice (not an error) when Hostname is empty, so the
//     observer does not fire a push for unconfigured nodes.
//
// Registration: call config.Register(HostnamePlugin{}) once at startup
// (after config.SetAlertWriter). The plugin is stateless.
type HostnamePlugin struct{}

// Name returns the stable plugin identifier used in DB rows and WS messages.
func (HostnamePlugin) Name() string { return "hostname" }

// WatchedKeys returns the config-tree path this plugin subscribes to.
//
// The observer indexes this key verbatim; UpdateNode must emit the same
// literal string via config.Notify when a hostname field changes.
func (HostnamePlugin) WatchedKeys() []string {
	return []string{hostnameWatchKey}
}

// WatchKey returns the literal watch-key string so callers (UpdateNode,
// tests) can reference it without importing the unexported constant.
func WatchKey() string { return hostnameWatchKey }

// Render returns a single InstallInstruction that writes /etc/hostname for
// the node identified by state.NodeID.
//
// Returns nil, nil when state.NodeConfig.Hostname is empty — the observer
// treats a nil slice as "nothing to push" and skips the WS send.
func (HostnamePlugin) Render(state config.ClusterState) ([]api.InstallInstruction, error) {
	hostname := state.NodeConfig.Hostname
	if hostname == "" {
		return nil, nil
	}

	content := hostname + "\n"
	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/hostname",
		Payload: content,
		// No Anchors: /etc/hostname is a single-line file owned entirely by
		// this plugin. A full overwrite is safe and idempotent.
	}

	return []api.InstallInstruction{instr}, nil
}

// RenderForNode is a convenience wrapper that builds a minimal ClusterState
// for nodeID + cfg and calls Render. Used by UpdateNode after a hostname change.
func RenderForNode(nodeID string, cfg api.NodeConfig) ([]api.InstallInstruction, error) {
	state := config.ClusterState{
		NodeID:     nodeID,
		NodeConfig: cfg,
	}
	return HostnamePlugin{}.Render(state)
}

// ValidateHostname returns a non-nil error if hostname is not a valid /etc/hostname
// value. A hostname plugin produces exactly one "overwrite" instruction whose
// Payload is hostname+"\n"; this function lets callers confirm the input is sane
// before calling Render.
func ValidateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("hostname: hostname must not be empty")
	}
	return nil
}
