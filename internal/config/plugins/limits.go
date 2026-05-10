package plugins

// limits.go — Sprint 36 Day 3
//
// LimitsPlugin renders the clustr-managed block inside /etc/security/limits.conf
// for the node identified by state.NodeID. ANCHORS are required: limits.conf is
// shared with other tooling (PAM, Slurm, operator-managed entries). The plugin
// owns only the "# BEGIN clustr/limits" … "# END clustr/limits" region.
//
// This is the canonical ANCHORS coexistence case from the reactive-config design
// doc (§8.3): a second plugin (e.g. a future limits-slurm plugin) can own its own
// anchor region in the same file without interfering with this plugin's block.
//
// There is no pre-existing imperative path for general node limits in clustr.
// The plugin introduces new capability: applying standard HPC resource limits to
// deployed nodes based on their configured tags (role). When no limits-relevant
// tags are set, a safe permissive default suitable for all HPC node roles is used.

import (
	"fmt"
	"strings"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/pkg/api"
)

const (
	limitsAnchorBegin = "# BEGIN clustr/limits"
	limitsAnchorEnd   = "# END clustr/limits"
)

// limitsWatchKey is the config-tree path the limits plugin subscribes to.
//
// Node tags (formerly "groups") drive the role-based limit profiles. The
// observer receives a Notify call with this key whenever the server updates
// node tags via UpdateNode PUT.
const limitsWatchKey = "nodes.*.tags"

// LimitsPlugin renders the clustr-managed /etc/security/limits.conf block for
// the node identified by state.NodeID. It is stateless and safe for concurrent
// invocation.
//
// Render contract:
//   - Uses ANCHORS (limitsAnchorBegin / limitsAnchorEnd): limits.conf is shared
//     with PAM, Slurm, and operator-managed entries. Only the clustr block is
//     written; everything outside the markers is preserved byte-for-byte.
//   - Pure and idempotent: same NodeID + same Tags → same block content → same hash.
//   - Always returns exactly one instruction (even for unconfigured nodes):
//     a bare anchor block with the standard permissive HPC default.
//
// Registration: call config.Register(LimitsPlugin{}) once at startup inside the
// reactiveConfigPluginsOnce.Do block.
type LimitsPlugin struct{}

// Name returns the stable plugin identifier used in DB rows and WS messages.
func (LimitsPlugin) Name() string { return "limits" }

// WatchedKeys returns the config-tree path this plugin subscribes to.
// The observer indexes this key verbatim; UpdateNode must emit the same literal
// string via config.Notify when node tags change.
func (LimitsPlugin) WatchedKeys() []string {
	return []string{limitsWatchKey}
}

// LimitsWatchKey returns the literal watch-key string so callers (server
// wiring, tests) can reference it without importing the unexported constant.
func LimitsWatchKey() string { return limitsWatchKey }

// LimitsAnchorBegin / LimitsAnchorEnd export the marker strings so callers
// (tests, server wiring) can assert against them.
func LimitsAnchorBegin() string { return limitsAnchorBegin }
func LimitsAnchorEnd() string   { return limitsAnchorEnd }

// Render returns a single InstallInstruction that writes the clustr-managed
// block into /etc/security/limits.conf for the node identified by state.NodeID.
//
// The block content is selected by node role (derived from Tags):
//   - "gpu"     — GPU compute: inherits compute limits plus CUDA memlock.
//   - "compute" — standard HPC compute node: generous nofile, nproc, memlock.
//   - "login"   — login / interactive node: tighter per-user limits.
//   - default   — permissive baseline covering all node types.
//
// Role detection is additive: the most specific matching tag wins.
// Example: a node tagged ["compute","gpu"] uses the gpu profile.
func (LimitsPlugin) Render(state config.ClusterState) ([]api.InstallInstruction, error) {
	role := nodeRole(state.NodeConfig.Tags)
	payload := renderLimitsBlock(role, state.NodeConfig.Hostname)

	instr := api.InstallInstruction{
		Opcode:  "overwrite",
		Target:  "/etc/security/limits.conf",
		Payload: payload,
		// ANCHORS: /etc/security/limits.conf is shared with PAM, Slurm, and
		// operator-managed entries. Only the clustr block is managed; all other
		// content is preserved byte-for-byte. This is the canonical coexistence
		// case from the reactive-config design doc (§8.3).
		Anchors: &api.AnchorPair{
			Begin: limitsAnchorBegin,
			End:   limitsAnchorEnd,
		},
	}

	return []api.InstallInstruction{instr}, nil
}

// nodeRole returns the HPC role label for the given tags slice.
// Priority (highest to lowest): gpu > compute > login > storage > default.
func nodeRole(tags []string) string {
	for _, t := range tags {
		switch strings.ToLower(t) {
		case "gpu":
			return "gpu"
		}
	}
	for _, t := range tags {
		switch strings.ToLower(t) {
		case "compute":
			return "compute"
		case "login":
			return "login"
		case "storage":
			return "storage"
		}
	}
	return "default"
}

// renderLimitsBlock returns the limits.conf lines for the clustr-managed block.
//
// The limits are modelled on common EL9 HPC cluster configurations:
//
//	nofile  — max open file descriptors; HPC workloads open many MPI sockets.
//	nproc   — max user processes; prevents runaway fork bombs.
//	memlock — memory lock (bytes); required for MPI over InfiniBand (RDMA).
//	stack   — stack size; consistent with RHEL defaults.
func renderLimitsBlock(role, hostname string) string {
	var sb strings.Builder

	// Header comment identifies the source so operators understand the block.
	sb.WriteString(fmt.Sprintf("# clustr-managed limits for %s (role: %s)\n", hostname, role))
	sb.WriteString("# Generated by LimitsPlugin — edit node tags to change profile.\n")

	switch role {
	case "gpu":
		// GPU nodes require unlimited memlock for CUDA and RDMA. nofile and
		// nproc are generous to accommodate deep-learning frameworks.
		sb.WriteString("*          soft    nofile     1048576\n")
		sb.WriteString("*          hard    nofile     1048576\n")
		sb.WriteString("*          soft    nproc      unlimited\n")
		sb.WriteString("*          hard    nproc      unlimited\n")
		sb.WriteString("*          soft    memlock    unlimited\n")
		sb.WriteString("*          hard    memlock    unlimited\n")
		sb.WriteString("*          soft    stack      unlimited\n")
		sb.WriteString("*          hard    stack      unlimited\n")

	case "compute":
		// Compute nodes: generous nofile for MPI sockets, unlimited memlock
		// for IB RDMA, high nproc for parallel tasks.
		sb.WriteString("*          soft    nofile     65536\n")
		sb.WriteString("*          hard    nofile     65536\n")
		sb.WriteString("*          soft    nproc      65536\n")
		sb.WriteString("*          hard    nproc      65536\n")
		sb.WriteString("*          soft    memlock    unlimited\n")
		sb.WriteString("*          hard    memlock    unlimited\n")
		sb.WriteString("*          soft    stack      8192\n")
		sb.WriteString("*          hard    stack      unlimited\n")

	case "login":
		// Login nodes: interactive workloads; tighter per-user nproc to
		// prevent runaway processes from degrading interactive sessions.
		sb.WriteString("*          soft    nofile     65536\n")
		sb.WriteString("*          hard    nofile     65536\n")
		sb.WriteString("*          soft    nproc      4096\n")
		sb.WriteString("*          hard    nproc      8192\n")
		sb.WriteString("*          soft    memlock    64\n")
		sb.WriteString("*          hard    memlock    64\n")
		sb.WriteString("*          soft    stack      8192\n")
		sb.WriteString("*          hard    stack      unlimited\n")

	default:
		// Conservative permissive baseline: a safe starting point for any
		// node type that has not been assigned a specific role. Values match
		// common RHEL 9 / Rocky Linux 9 system defaults for HPC workloads.
		sb.WriteString("*          soft    nofile     65536\n")
		sb.WriteString("*          hard    nofile     65536\n")
		sb.WriteString("*          soft    nproc      65536\n")
		sb.WriteString("*          hard    nproc      65536\n")
		sb.WriteString("*          soft    memlock    64\n")
		sb.WriteString("*          hard    memlock    64\n")
		sb.WriteString("*          soft    stack      8192\n")
		sb.WriteString("*          hard    stack      unlimited\n")
	}

	return sb.String()
}
