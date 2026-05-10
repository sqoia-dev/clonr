// Package config manages clustr runtime configuration and the reactive
// config-observer (Sprint 36).
package config

import "github.com/sqoia-dev/clustr/pkg/api"

// ClusterState is a read-only snapshot of node and cluster-wide config that
// the observer materialises once per scheduler tick and passes to every
// Plugin.Render call. It is intentionally minimal for Bundle A; fields will
// be added in Day 2–3 as plugins are converted to the reactive interface.
//
// Plugins MUST NOT mutate any field. The observer may share the same
// ClusterState pointer across multiple concurrent Render calls.
type ClusterState struct {
	// NodeID is the target node this state snapshot is scoped to.
	NodeID string

	// NodeConfig is the current persisted config for NodeID.
	NodeConfig api.NodeConfig

	// AllNodes is the list of all registered nodes in the cluster.
	// Plugins that produce cross-node content (e.g. /etc/hosts) can range
	// over this slice. The slice must not be modified by plugins.
	AllNodes []api.NodeConfig
}
