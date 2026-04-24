// render.go — Slurm config template renderer.
// RenderConfig applies Go text/template to a config file template.
// RenderAllForNode builds the full RenderContext for a node and renders every
// managed config file whose is_template flag is set.
package slurm

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// RenderContext holds all data available to templates during rendering.
// Field names are the public names used inside {{ }} template blocks.
type RenderContext struct {
	// ClusterName is the Slurm cluster name from module config.
	ClusterName string
	// ControllerHostname is the hostname of the node with role "controller".
	// Falls back to "clonr-server" when no controller node is registered.
	ControllerHostname string
	// Nodes holds all compute + controller nodes for the NodeName block in slurm.conf.
	Nodes []NodeRenderData
	// CurrentNode is the node this render is specifically for (may be nil for global renders).
	CurrentNode *NodeRenderData
	// Overrides holds per-node override key → value for the current node.
	Overrides map[string]string
	// Timestamp is the render time, for header comments.
	Timestamp string
}

// NodeRenderData represents one node's hardware parameters for the slurm.conf
// NodeName line. Field names must match the existing slurm.conf.tmpl markers.
type NodeRenderData struct {
	// NodeID is the internal clonr node UUID.
	NodeID string
	// NodeName is the hostname used in slurm.conf NodeName= directives.
	NodeName string
	// CPUCount is the total logical CPU count (sockets * cores * threads).
	CPUCount string
	// Sockets is the number of physical sockets.
	Sockets string
	// CoresPerSocket is the number of cores per socket.
	CoresPerSocket string
	// ThreadsPerCore is the number of hardware threads per core (1 or 2).
	ThreadsPerCore string
	// RealMemoryMB is the usable RAM in megabytes.
	RealMemoryMB string
	// GRESParam is the Gres= value for this node (e.g. "gpu:a100:2").
	// Empty string means no Gres line.
	GRESParam string
	// Roles is the set of Slurm roles for this node.
	Roles []string
}

// RenderConfig renders a single config file template for a specific node.
// templateContent is the raw config file content (potentially with Go template markers).
// If is_template is false, the content is returned as-is (this function always
// tries to parse; callers pass is_template=false content through directly).
// Returns the rendered string or an error.
func RenderConfig(templateContent string, ctx RenderContext) (string, error) {
	tmpl, err := template.New("slurm_config").Option("missingkey=zero").Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("render: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("render: execute template: %w", err)
	}
	return buf.String(), nil
}

// RenderAllForNode renders all managed config files for the given node.
// It fetches current template versions from the DB, builds the RenderContext
// (all Slurm nodes + their overrides + hardware data), and returns a map of
// filename → rendered content. Files with is_template=false are returned as-is.
//
// The returned map only contains files that exist in the DB. Missing files are
// logged as warnings and skipped.
func (m *Manager) RenderAllForNode(ctx context.Context, nodeID string) (map[string]string, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	if cfg == nil {
		return nil, fmt.Errorf("render: slurm module not configured")
	}

	// Build the RenderContext.
	renderCtx, err := m.buildRenderContext(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("render: build context: %w", err)
	}

	result := make(map[string]string, len(cfg.ManagedFiles))

	for _, filename := range cfg.ManagedFiles {
		row, err := m.db.SlurmGetCurrentConfig(ctx, filename)
		if err != nil {
			log.Warn().Err(err).Str("filename", filename).Msg("slurm render: skip missing config file")
			continue
		}

		if !row.IsTemplate {
			result[filename] = row.Content
			continue
		}

		rendered, err := RenderConfig(row.Content, *renderCtx)
		if err != nil {
			log.Warn().Err(err).Str("filename", filename).Str("node_id", nodeID).
				Msg("slurm render: template render failed, using raw content")
			result[filename] = row.Content
			continue
		}
		result[filename] = rendered
	}

	// gres.conf special case: if the node has a gres_conf_content override, use it.
	if renderCtx.Overrides != nil {
		if gresContent, ok := renderCtx.Overrides["gres_conf_content"]; ok && gresContent != "" {
			result["gres.conf"] = gresContent
		}
	}

	return result, nil
}

// buildRenderContext constructs the full RenderContext for a given node.
// It fetches all node roles from the DB, resolves hostnames via ListNodeConfigs,
// and collects overrides for the current node.
func (m *Manager) buildRenderContext(ctx context.Context, nodeID string) (*RenderContext, error) {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()

	clusterName := ""
	if cfg != nil {
		clusterName = cfg.ClusterName
	}

	// Fetch all node role entries.
	roleEntries, err := m.db.SlurmListAllNodeRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("buildRenderContext: list node roles: %w", err)
	}

	// Fetch all node configs to resolve hostnames.
	allNodeConfigs, err := m.db.ListNodeConfigs(ctx, "")
	if err != nil {
		// Non-fatal: we can still render with node IDs if hostname lookup fails.
		log.Warn().Err(err).Msg("slurm render: failed to list node configs, using node IDs as hostnames")
		allNodeConfigs = nil
	}

	// Build a nodeID → api.NodeConfig lookup for hostname resolution.
	nodeConfigByID := make(map[string]api.NodeConfig, len(allNodeConfigs))
	for _, nc := range allNodeConfigs {
		nodeConfigByID[nc.ID] = nc
	}

	// Build NodeRenderData for each node that has a Slurm role (excluding RoleNone/login-only).
	var nodes []NodeRenderData
	controllerHostname := "clonr-server"

	for _, entry := range roleEntries {
		// Skip nodes with no meaningful compute roles.
		if !hasRole(entry.Roles, RoleController) && !hasRole(entry.Roles, RoleCompute) {
			continue
		}

		hostname := entry.NodeID // fallback
		if nc, ok := nodeConfigByID[entry.NodeID]; ok && nc.Hostname != "" {
			hostname = nc.Hostname
		}

		// Fetch per-node hardware overrides to populate NodeRenderData fields.
		overrides, err := m.db.SlurmGetNodeOverrides(ctx, entry.NodeID)
		if err != nil {
			overrides = map[string]string{}
		}

		nd := NodeRenderData{
			NodeID:         entry.NodeID,
			NodeName:       hostname,
			CPUCount:       overrideOrDefault(overrides, "cpus", "1"),
			Sockets:        overrideOrDefault(overrides, "sockets", "1"),
			CoresPerSocket: overrideOrDefault(overrides, "cores_per_socket", "1"),
			ThreadsPerCore: overrideOrDefault(overrides, "threads_per_core", "1"),
			RealMemoryMB:   overrideOrDefault(overrides, "real_memory", "1000"),
			GRESParam:      overrides["gres"],
			Roles:          entry.Roles,
		}

		if hasRole(entry.Roles, RoleController) {
			controllerHostname = hostname
		}

		nodes = append(nodes, nd)
	}

	// Fetch overrides for the current node (used for gres_conf_content, etc.).
	currentNodeOverrides, err := m.db.SlurmGetNodeOverrides(ctx, nodeID)
	if err != nil {
		currentNodeOverrides = map[string]string{}
	}

	// Find the current node's NodeRenderData entry.
	var currentNode *NodeRenderData
	for i := range nodes {
		if nodes[i].NodeID == nodeID {
			currentNode = &nodes[i]
			break
		}
	}

	// If currentNode not in the compute/controller list (e.g. login-only node),
	// build a minimal entry so templates that reference .CurrentNode still work.
	if currentNode == nil {
		hostname := nodeID
		if nc, ok := nodeConfigByID[nodeID]; ok && nc.Hostname != "" {
			hostname = nc.Hostname
		}
		nd := NodeRenderData{
			NodeID:         nodeID,
			NodeName:       hostname,
			CPUCount:       overrideOrDefault(currentNodeOverrides, "cpus", "1"),
			Sockets:        overrideOrDefault(currentNodeOverrides, "sockets", "1"),
			CoresPerSocket: overrideOrDefault(currentNodeOverrides, "cores_per_socket", "1"),
			ThreadsPerCore: overrideOrDefault(currentNodeOverrides, "threads_per_core", "1"),
			RealMemoryMB:   overrideOrDefault(currentNodeOverrides, "real_memory", "1000"),
			GRESParam:      currentNodeOverrides["gres"],
		}
		currentNode = &nd
	}

	return &RenderContext{
		ClusterName:        clusterName,
		ControllerHostname: controllerHostname,
		Nodes:              nodes,
		CurrentNode:        currentNode,
		Overrides:          currentNodeOverrides,
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// overrideOrDefault returns the value from overrides for key, or def if absent/empty.
func overrideOrDefault(overrides map[string]string, key, def string) string {
	if v, ok := overrides[key]; ok && v != "" {
		return v
	}
	return def
}
