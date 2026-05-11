package plugins

// hosts.go — Sprint 36 Day 3
//
// HostsPlugin renders the clustr-managed block inside /etc/hosts for the node
// identified by state.NodeID. The managed block is bounded by ANCHORS so that
// operator-added entries and OS-managed localhost lines are preserved.

import (
	"fmt"
	"strings"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// hostsAnchorBegin / hostsAnchorEnd are the exact marker lines that bound the
// clustr-managed block inside /etc/hosts. They are intentionally different from
// the old imperative markers ("# --- clustr cluster hosts ---") so that
// deploy-time and reactive-push writes can coexist during the Day 4 migration
// window without marker collisions.
const (
	hostsAnchorBegin = "# BEGIN clustr/hosts"
	hostsAnchorEnd   = "# END clustr/hosts"
)

// hostsWatchKey is the config-tree path the hosts plugin subscribes to.
//
// ClusterHosts is populated at node-registration time and updated whenever a
// node's hostname changes (since the cluster-wide hostname→IP map changes).
// The observer receives a Notify call with this key whenever the server
// updates ClusterHosts for any node.
const hostsWatchKey = "nodes.*.cluster_hosts"

// HostsPlugin renders the clustr-managed /etc/hosts block for the node
// identified by state.NodeID. It is stateless and safe for concurrent use.
//
// Render contract:
//   - Uses ANCHORS (hostsAnchorBegin / hostsAnchorEnd): /etc/hosts is shared
//     with the OS (localhost, ::1) and potentially other tools. Only the
//     clustr-managed block is written; everything outside the markers is
//     preserved byte-for-byte.
//   - Pure and idempotent: same AllNodes → same block content → same hash.
//   - Returns nil when AllNodes is empty, so a fresh/unconfigured cluster
//     does not push a vacuous hosts block.
//
// Registration: call config.Register(HostsPlugin{}) once at startup inside
// the reactiveConfigPluginsOnce.Do block.
type HostsPlugin struct{}

// Name returns the stable plugin identifier used in DB rows and WS messages.
func (HostsPlugin) Name() string { return "hosts" }

// WatchedKeys returns the config-tree path this plugin subscribes to.
// The observer indexes this key verbatim; the server must emit the same literal
// string via config.Notify when ClusterHosts changes.
func (HostsPlugin) WatchedKeys() []string {
	return []string{hostsWatchKey}
}

// HostsWatchKey returns the literal watch-key string so callers (server wiring,
// tests) can reference it without importing the unexported constant.
func HostsWatchKey() string { return hostsWatchKey }

// HostsAnchorBegin / HostsAnchorEnd export the marker strings so callers
// (tests, server wiring) can assert against them.
func HostsAnchorBegin() string { return hostsAnchorBegin }
func HostsAnchorEnd() string   { return hostsAnchorEnd }

// Metadata returns the execution and safety invariants for the hosts plugin.
//
// Priority 30: must run after hostname (P=20, so the local-host entry is
// correct) but before any service that resolves cluster peers (slurm, sssd).
// Sits in the Foundation band (0–50).
//
// Dangerous=false: the managed block is bounded by ANCHORS and is
// human-readable. A bad render is recoverable without console access.
//
// Backup=nil on Day 1; wired in Sprint 41 Day 4.
func (HostsPlugin) Metadata() config.PluginMetadata {
	return config.PluginMetadata{
		Priority:  30,
		Dangerous: false,
	}
}

// Render returns a single InstallInstruction that writes the clustr-managed
// block into /etc/hosts for the node identified by state.NodeID.
//
// The rendered block contains all cluster nodes from state.AllNodes, formatted
// identically to writeClusterHosts in internal/deploy/finalize.go:
//
//	<IP> <FQDN> <hostname>   (when FQDN is non-empty and distinct from hostname)
//	<IP> <hostname>           (when FQDN equals hostname or is empty)
//
// Returns nil, nil when state.AllNodes is empty.
func (HostsPlugin) Render(state config.ClusterState) ([]api.InstallInstruction, error) {
	if len(state.AllNodes) == 0 {
		return nil, nil
	}

	payload := renderHostsBlock(state.AllNodes)

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/hosts",
		Payload: payload,
		// ANCHORS: /etc/hosts is shared with the OS. Only the clustr block is
		// managed; operator entries and localhost lines are left untouched.
		Anchors: &api.AnchorPair{
			Begin: hostsAnchorBegin,
			End:   hostsAnchorEnd,
		},
	}

	return []api.InstallInstruction{instr}, nil
}

// renderHostsBlock builds the payload (body between the anchor markers) for
// the clustr-managed /etc/hosts block. The format is identical to the
// imperative writeClusterHosts function in internal/deploy/finalize.go:
//
//   - "%-15s %s %s" when FQDN is non-empty and distinct from Hostname
//   - "%-15s %s"    otherwise
//   - Aliases are appended space-separated after the primary hostname(s).
//
// Source of IPs: each NodeConfig.Interfaces[0].IPAddress, CIDR-stripped (same
// as the imperative registerNode path in internal/server/handlers/nodes.go).
func renderHostsBlock(nodes []api.NodeConfig) string {
	var sb strings.Builder
	for _, n := range nodes {
		if n.Hostname == "" {
			continue
		}
		if len(n.Interfaces) == 0 || n.Interfaces[0].IPAddress == "" {
			continue
		}
		ip := extractIPFromCIDR(n.Interfaces[0].IPAddress)
		if ip == "" {
			continue
		}
		var line string
		if n.FQDN != "" && n.FQDN != n.Hostname {
			line = fmt.Sprintf("%-15s %s %s", ip, n.FQDN, n.Hostname)
		} else {
			line = fmt.Sprintf("%-15s %s", ip, n.Hostname)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// extractIPFromCIDR strips the prefix length from a CIDR string such as
// "192.168.1.50/24", returning "192.168.1.50". Returns the input unchanged
// if it contains no slash (already a bare IP). Mirrors the extractIP helper
// in internal/server/handlers/nodes.go without importing it.
func extractIPFromCIDR(cidr string) string {
	if idx := strings.IndexByte(cidr, '/'); idx >= 0 {
		return cidr[:idx]
	}
	return cidr
}
