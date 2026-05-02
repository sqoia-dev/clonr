// Package selector implements the clustr selector grammar shared by all
// batch commands (exec, cp, console, ipmi sel, health).
//
// Selector flags:
//
//	-n / --nodes     hostlist syntax: node01, node[01-32], node[01-04,08,12-15]
//	-g / --group     group name (node_groups table)
//	-A / --all       all registered nodes
//	-a / --active    active nodes (deployed_verified state)
//	--racks          rack names — empty-fallback until rack model lands in #138
//	--chassis        chassis names — empty-fallback until rack model lands in #138
//	--ignore-status  bypass the "active" filter when -a is used; also suppresses
//	                 state-based filtering from other selectors
//
// Empty SelectorSet (no flags set) → error "at least one selector required".
// No silent "all nodes."
package selector

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// NodeID is a clustr node UUID string.
type NodeID = string

// SelectorSet carries the parsed selector flag values for a single invocation.
// It is populated by RegisterSelectorFlags / ReadSelectorFlags and then passed
// to Resolve.
type SelectorSet struct {
	// Nodes is the raw hostlist expression from -n / --nodes.
	// Examples: "node01", "node[01-32]", "node[01-04,08]", "n01,n[03-05]".
	Nodes string

	// Group is the node group name from -g / --group.
	Group string

	// All selects all registered nodes when true (-A / --all).
	All bool

	// Active selects only nodes in the deployed_verified state (-a / --active).
	Active bool

	// Racks is a comma-separated list of rack names (--racks).
	// The rack model lands in #138; until then Resolve returns empty results for
	// this selector without error.
	Racks string

	// Chassis is a comma-separated list of chassis names (--chassis).
	// Same empty-fallback semantics as Racks.
	Chassis string

	// IgnoreStatus, when true, bypasses the "active" filter so all nodes
	// matching the other selectors are returned regardless of deploy state.
	IgnoreStatus bool
}

// IsEmpty returns true when no selector has been specified.
func (s *SelectorSet) IsEmpty() bool {
	return s.Nodes == "" &&
		s.Group == "" &&
		!s.All &&
		!s.Active &&
		s.Racks == "" &&
		s.Chassis == ""
}

// RegisterSelectorFlags attaches the standard selector flags to cmd.
// Every batch subcommand (exec, cp, console, ipmi sel, health) should call
// this in their init/construction so the grammar is uniform.
//
// The caller holds a *SelectorSet that is populated by cobra when the command
// runs; pass it back to Resolve.
func RegisterSelectorFlags(cmd *cobra.Command, set *SelectorSet) {
	cmd.Flags().StringVarP(&set.Nodes, "nodes", "n", "", "Hostlist: node01, node[01-32], node[01-04,08,12-15]")
	cmd.Flags().StringVarP(&set.Group, "group", "g", "", "Node group name")
	cmd.Flags().BoolVarP(&set.All, "all", "A", false, "All registered nodes")
	cmd.Flags().BoolVarP(&set.Active, "active", "a", false, "Active nodes (deployed_verified state only)")
	cmd.Flags().StringVar(&set.Racks, "racks", "", "Rack names (comma-separated) — resolved after #138 lands")
	cmd.Flags().StringVar(&set.Chassis, "chassis", "", "Chassis names (comma-separated) — resolved after #138 lands")
	cmd.Flags().BoolVar(&set.IgnoreStatus, "ignore-status", false,
		"Bypass the active-only filter; return all nodes matched by the other selectors regardless of deploy state")
}

// SelectorDB is the database interface required by Resolve.
// Declared as an interface so tests can inject a fake.
type SelectorDB interface {
	// ListAllNodes returns all node configs.
	ListAllNodes(ctx context.Context) ([]SelectorNode, error)
	// ListGroupMemberIDs returns the node IDs of all members of the named group.
	ListGroupMemberIDs(ctx context.Context, groupName string) ([]NodeID, error)
}

// SelectorNode carries the minimal node fields needed by the resolver.
type SelectorNode struct {
	ID       NodeID
	Hostname string
	// Active is true when the node is in the deployed_verified state.
	Active bool
}

// Resolve translates a SelectorSet into an ordered, deduplicated slice of
// NodeIDs. The returned slice is sorted for deterministic fan-out order.
//
// Rules:
//   - Empty SelectorSet → error "at least one selector required"
//   - --racks / --chassis → accepted but return empty (rack model not yet in DB)
//   - --ignore-status → suppresses the active-state filter when -a is used;
//     also suppresses state filtering for -n / -g selectors
//   - Nodes from multiple selectors in the same invocation are unioned
func Resolve(ctx context.Context, db SelectorDB, set SelectorSet) ([]NodeID, error) {
	if set.IsEmpty() {
		return nil, fmt.Errorf("at least one selector required (-n, -g, -A, -a, --racks, --chassis)")
	}

	// Fetch the full node list once; all selectors work from this snapshot.
	all, err := db.ListAllNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("selector: list nodes: %w", err)
	}

	// Build lookup maps for fast hostname → node resolution.
	byHostname := make(map[string]SelectorNode, len(all))
	byID := make(map[NodeID]SelectorNode, len(all))
	for _, n := range all {
		byHostname[n.Hostname] = n
		byID[n.ID] = n
	}

	seen := make(map[NodeID]struct{})
	var result []NodeID

	add := func(id NodeID) {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}

	// passesActiveFilter returns true if the node should be included given the
	// current active/ignore-status flags.
	//
	// Rules:
	//   --ignore-status          → always pass (bypass state gate entirely)
	//   -a (Active) without -A   → only deployed_verified nodes pass
	//   -A (All)                 → all nodes pass regardless of state
	//   -n / -g                  → all matched nodes pass (no state gate)
	passesActiveFilter := func(n SelectorNode) bool {
		if set.IgnoreStatus {
			return true
		}
		if set.Active && !set.All {
			return n.Active
		}
		return true
	}

	// -A / --all
	if set.All {
		for _, n := range all {
			// -A always includes every node; --ignore-status is redundant but harmless.
			add(n.ID)
		}
	}

	// -a / --active (without -A, which already covered everything)
	if set.Active && !set.All {
		for _, n := range all {
			if passesActiveFilter(n) {
				add(n.ID)
			}
		}
	}

	// -n / --nodes  (hostlist)
	// Hostlist selects named nodes directly; state filtering is NOT applied
	// unless --ignore-status is explicitly combined with -a (the active flag).
	// A plain -n resolves all named nodes regardless of their deploy state.
	if set.Nodes != "" {
		hostnames, parseErr := ParseHostlist(set.Nodes)
		if parseErr != nil {
			return nil, fmt.Errorf("selector: invalid hostlist %q: %w", set.Nodes, parseErr)
		}
		for _, h := range hostnames {
			n, ok := byHostname[h]
			if !ok {
				return nil, fmt.Errorf("selector: node %q not found", h)
			}
			add(n.ID)
		}
	}

	// -g / --group
	// Group selects all group members; same no-state-gate rule as -n.
	if set.Group != "" {
		ids, grpErr := db.ListGroupMemberIDs(ctx, set.Group)
		if grpErr != nil {
			return nil, fmt.Errorf("selector: group %q: %w", set.Group, grpErr)
		}
		for _, id := range ids {
			n, ok := byID[id]
			if !ok {
				// Node was in the group but not in the all-nodes snapshot
				// (e.g. deleted between queries). Skip silently.
				continue
			}
			add(n.ID)
			_ = n // keep reference for future state filtering
		}
	}

	// --racks / --chassis — accepted, empty-fallback (rack model lands in #138).
	// We silently return nothing for these selectors; we do NOT error.
	// The sprint spec says: "return empty results gracefully (rack model lands later)".
	_ = set.Racks
	_ = set.Chassis

	// Deterministic output order.
	sort.Strings(result)
	return result, nil
}

// ─── Hostlist parser ──────────────────────────────────────────────────────────
//
// Supported syntax (pdsh/clustershell compatible subset):
//
//	node01                  → ["node01"]
//	node[01-04]             → ["node01", "node02", "node03", "node04"]
//	node[01-04,08,12-15]    → ["node01","node02","node03","node04","node08","node12",…"node15"]
//	n01,n[03-05]            → ["n01","n03","n04","n05"]
//
// Width of the first element in a range determines zero-padding of generated names.

// rangeRe matches a single bracket expression like "[01-04,08,12-15]".
// The inner content (without brackets) is captured in group 1.
var rangeRe = regexp.MustCompile(`^(.*?)\[([^\]]+)\](.*)$`)

// ParseHostlist parses a hostlist expression and returns the expanded hostname list.
// The expression may be a comma-separated list of hostlist tokens.
// Returns an error if the expression is syntactically invalid.
func ParseHostlist(expr string) ([]string, error) {
	// Top-level split: comma separates independent tokens UNLESS the comma is
	// inside brackets. We handle this with a simple state machine.
	tokens, err := splitTopLevel(expr)
	if err != nil {
		return nil, err
	}

	var out []string
	seen := make(map[string]struct{})
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		expanded, expErr := expandToken(tok)
		if expErr != nil {
			return nil, expErr
		}
		for _, h := range expanded {
			if _, ok := seen[h]; !ok {
				seen[h] = struct{}{}
				out = append(out, h)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty hostlist")
	}
	return out, nil
}

// splitTopLevel splits expr by commas that are NOT inside brackets.
func splitTopLevel(expr string) ([]string, error) {
	var tokens []string
	depth := 0
	start := 0
	for i, ch := range expr {
		switch ch {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				return nil, fmt.Errorf("unmatched ']' in %q", expr)
			}
			depth--
		case ',':
			if depth == 0 {
				tokens = append(tokens, expr[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unmatched '[' in %q", expr)
	}
	tokens = append(tokens, expr[start:])
	return tokens, nil
}

// expandToken expands a single token that may contain exactly one bracket group.
// Tokens with no brackets are returned as-is (single-element slice).
func expandToken(tok string) ([]string, error) {
	m := rangeRe.FindStringSubmatch(tok)
	if m == nil {
		// No bracket group — plain hostname.
		return []string{tok}, nil
	}

	prefix := m[1]
	inner := m[2]
	suffix := m[3]

	// Recursively handle the suffix in case of pathological nested syntax.
	// (We don't support nested brackets, but we do pass suffix through plain.)
	if strings.ContainsAny(suffix, "[]") {
		return nil, fmt.Errorf("nested bracket groups are not supported in %q", tok)
	}

	// Parse the inner range expression: comma-separated items which are either
	// plain numbers or "start-end" ranges.
	parts := strings.Split(inner, ",")
	var names []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			// Range.
			dash := strings.Index(part, "-")
			startStr := part[:dash]
			endStr := part[dash+1:]

			startVal, err := strconv.Atoi(startStr)
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q in %q", startStr, tok)
			}
			endVal, err := strconv.Atoi(endStr)
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q in %q", endStr, tok)
			}
			if endVal < startVal {
				return nil, fmt.Errorf("range end %d < start %d in %q", endVal, startVal, tok)
			}
			// Width is determined by the longer of the two boundary strings.
			width := len(startStr)
			if len(endStr) > width {
				width = len(endStr)
			}
			for i := startVal; i <= endVal; i++ {
				names = append(names, fmt.Sprintf("%s%0*d%s", prefix, width, i, suffix))
			}
		} else {
			// Single number.
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid range element %q in %q", part, tok)
			}
			names = append(names, fmt.Sprintf("%s%0*d%s", prefix, len(part), n, suffix))
		}
	}
	return names, nil
}
