// Package handlers — variants.go implements the per-attribute variant overlay
// system (Sprint 44 VARIANTS-SYSTEM).
//
// Variants are (scope_kind, scope_id, attribute_path, value_json) tuples that
// override fields on the base NodeConfig at resolve time. The resolver applies
// them in priority order:
//
//	role        (lowest)
//	group
//	node-direct (highest)
//
// Routes:
//
//	GET  /api/v1/nodes/{id}/effective-config  — resolved config with overlays
//	POST /api/v1/variants                     — create one variant
//	DELETE /api/v1/variants/{id}              — clear one variant
//	GET  /api/v1/variants                     — list all (admin)
//
// attribute_path is a dotted JSON-pointer-style path interpreted by ResolveVariants
// against the JSON-marshalled NodeConfig.  Examples:
//
//	"kernel_args"          — overwrites cfg.KernelArgs
//	"bmc.username"         — overwrites cfg.BMC.Username
//	"interfaces[0].ip"     — overwrites cfg.Interfaces[0].IPAddress
//	"custom_vars.GPU_TYPE" — sets cfg.CustomVars["GPU_TYPE"]
//
// The applier operates on json.RawMessage trees rather than reflecting against
// the NodeConfig struct — this means any future field added to NodeConfig is
// automatically overlay-able without code changes here. The cost is that the
// path doesn't get compile-time checking; tests in variants_test.go cover the
// common cases.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// VariantsDB is the minimal DB surface this handler needs.
type VariantsDB interface {
	GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error)
	CreateVariant(ctx context.Context, v db.NodeConfigVariant) error
	DeleteVariant(ctx context.Context, id string) error
	GetVariant(ctx context.Context, id string) (db.NodeConfigVariant, error)
	ListVariantsForNode(ctx context.Context, nodeID, groupID string, roles []string) ([]db.NodeConfigVariant, error)
	ListAllVariants(ctx context.Context) ([]db.NodeConfigVariant, error)
}

// VariantsHandler serves the variant endpoints.
type VariantsHandler struct {
	DB VariantsDB
}

// NewVariantsHandler wires the handler against a real *db.DB.
func NewVariantsHandler(d *db.DB) *VariantsHandler {
	return &VariantsHandler{DB: d}
}

// ─── Wire types ───────────────────────────────────────────────────────────────

// CreateVariantRequest is the body of POST /api/v1/variants.
type CreateVariantRequest struct {
	NodeID        string          `json:"node_id,omitempty"`
	AttributePath string          `json:"attribute_path"`
	Value         json.RawMessage `json:"value"`
	ScopeKind     string          `json:"scope_kind"` // "global" | "group" | "role"
	ScopeID       string          `json:"scope_id,omitempty"`
}

// VariantResponse is the GET/POST response shape for a single variant.
type VariantResponse struct {
	ID            string          `json:"id"`
	NodeID        string          `json:"node_id,omitempty"`
	AttributePath string          `json:"attribute_path"`
	Value         json.RawMessage `json:"value"`
	ScopeKind     string          `json:"scope_kind"`
	ScopeID       string          `json:"scope_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ListVariantsResponse wraps a list of variants.
type ListVariantsResponse struct {
	Variants []VariantResponse `json:"variants"`
}

// EffectiveConfigResponse wraps the resolved NodeConfig plus the variants that
// were applied (so the UI can highlight overlaid fields without re-querying).
type EffectiveConfigResponse struct {
	NodeID    string            `json:"node_id"`
	Config    api.NodeConfig    `json:"config"`
	Variants  []VariantResponse `json:"variants"` // ordered by application priority (role→group→node)
	OverlayBy map[string]string `json:"overlay_by"` // attribute_path → scope_kind that finally won
}

// ─── POST /api/v1/variants ────────────────────────────────────────────────────

func (h *VariantsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateVariantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}

	// Validate scope.
	scope := db.VariantScopeKind(strings.ToLower(req.ScopeKind))
	if !scope.IsValid() {
		writeValidationError(w, fmt.Sprintf("invalid scope_kind %q (valid: global, group, role)", req.ScopeKind))
		return
	}
	if scope == db.VariantScopeGroup && req.ScopeID == "" {
		writeValidationError(w, "scope_id is required for scope_kind=group")
		return
	}
	if scope == db.VariantScopeRole && req.ScopeID == "" {
		writeValidationError(w, "scope_id is required for scope_kind=role (use the role label)")
		return
	}
	if strings.TrimSpace(req.AttributePath) == "" {
		writeValidationError(w, "attribute_path is required")
		return
	}
	if !validAttributePath(req.AttributePath) {
		writeValidationError(w, fmt.Sprintf("invalid attribute_path %q", req.AttributePath))
		return
	}
	if len(req.Value) == 0 {
		writeValidationError(w, "value is required")
		return
	}
	// Validate that value is well-formed JSON. We accept any JSON type — the
	// applier will handle string / number / bool / object / array.
	var probe any
	if err := json.Unmarshal(req.Value, &probe); err != nil {
		writeValidationError(w, fmt.Sprintf("value must be valid JSON: %v", err))
		return
	}

	v := db.NodeConfigVariant{
		ID:            uuid.New().String(),
		NodeID:        req.NodeID,
		AttributePath: req.AttributePath,
		ValueJSON:     string(req.Value),
		ScopeKind:     scope,
		ScopeID:       req.ScopeID,
		CreatedAt:     time.Now().UTC(),
	}
	if scope == db.VariantScopeGlobal && v.NodeID == "" {
		// "global" with no node_id is a true cluster-wide variant — allowed
		// but rare; guard against accidentally unbounded blast-radius writes
		// by requiring an explicit confirm header.
		if r.Header.Get("X-Clustr-Cluster-Wide") != "yes" {
			writeValidationError(w, "global cluster-wide variant requires X-Clustr-Cluster-Wide: yes")
			return
		}
	}

	if err := h.DB.CreateVariant(r.Context(), v); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toVariantResponse(v))
}

// ─── DELETE /api/v1/variants/{id} ─────────────────────────────────────────────

func (h *VariantsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteVariant(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── GET /api/v1/variants ─────────────────────────────────────────────────────

func (h *VariantsHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.ListAllVariants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]VariantResponse, 0, len(rows))
	for _, v := range rows {
		out = append(out, toVariantResponse(v))
	}
	writeJSON(w, http.StatusOK, ListVariantsResponse{Variants: out})
}

// ─── GET /api/v1/nodes/{id}/effective-config ─────────────────────────────────

func (h *VariantsHandler) GetEffectiveConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	cfg, err := h.DB.GetNodeConfig(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}

	roles := nodeRoles(cfg)
	variants, err := h.DB.ListVariantsForNode(r.Context(), nodeID, cfg.GroupID, roles)
	if err != nil {
		writeError(w, err)
		return
	}

	overlayBy, err := ApplyVariants(&cfg, variants)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: fmt.Sprintf("variant application failed: %v", err),
			Code:  "variant_apply_error",
		})
		return
	}

	resp := EffectiveConfigResponse{
		NodeID:    nodeID,
		Config:    sanitizeNodeConfig(cfg),
		Variants:  make([]VariantResponse, 0, len(variants)),
		OverlayBy: overlayBy,
	}
	for _, v := range variants {
		resp.Variants = append(resp.Variants, toVariantResponse(v))
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── Resolver / applier ──────────────────────────────────────────────────────

// ApplyVariants overlays the variants on cfg in priority order (the input
// must already be sorted role → group → node-direct; ListVariantsForNode does
// this).  Returns a map of attribute_path → scope_kind reflecting which scope
// finally won for each path so the UI can render an "overlaid by" badge.
//
// Implementation: marshal cfg to JSON, splice each variant's value into the
// JSON tree at attribute_path, then unmarshal back into cfg. This avoids
// reflection on the (large, evolving) NodeConfig struct and keeps the
// implementation table-driven.
func ApplyVariants(cfg *api.NodeConfig, variants []db.NodeConfigVariant) (map[string]string, error) {
	if len(variants) == 0 {
		return map[string]string{}, nil
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal cfg: %w", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("unmarshal cfg: %w", err)
	}

	overlayBy := make(map[string]string, len(variants))
	for _, v := range variants {
		var value any
		if err := json.Unmarshal([]byte(v.ValueJSON), &value); err != nil {
			return nil, fmt.Errorf("variant %s value: %w", v.ID, err)
		}
		newTree, err := setAtPath(tree, v.AttributePath, value)
		if err != nil {
			return nil, fmt.Errorf("variant %s path %q: %w", v.ID, v.AttributePath, err)
		}
		tree = newTree
		// Last write wins — by the time we exit the loop overlayBy reflects
		// the highest-priority scope that touched each path.
		overlayBy[v.AttributePath] = string(v.ScopeKind)
	}

	merged, err := json.Marshal(tree)
	if err != nil {
		return nil, fmt.Errorf("remarshal: %w", err)
	}
	*cfg = api.NodeConfig{} // reset — Unmarshal merges into existing fields
	if err := json.Unmarshal(merged, cfg); err != nil {
		return nil, fmt.Errorf("unmarshal back: %w", err)
	}
	return overlayBy, nil
}

// pathSegmentRe describes one path segment: bare identifier, optionally
// followed by zero or more [N] index suffixes. Strict to keep the parser
// simple — we don't support quoted keys or escapes.
var pathSegmentRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)(\[\d+\])*$`)

// validAttributePath checks the path is syntactically well-formed.
func validAttributePath(path string) bool {
	if path == "" {
		return false
	}
	for _, seg := range strings.Split(path, ".") {
		if !pathSegmentRe.MatchString(seg) {
			return false
		}
	}
	return true
}

// setAtPath splices value into tree at the given dotted path, creating
// intermediate map nodes as necessary. Array indices ([N]) extend the array
// with nil padding when N >= current length.
func setAtPath(tree any, path string, value any) (any, error) {
	if path == "" {
		return value, nil
	}
	segs := splitPath(path)
	return setAtSegs(tree, segs, value)
}

// pathSeg is one parsed segment. If Index >= 0 the segment indexes into an
// array at that position (after first descending into Key on the parent).
type pathSeg struct {
	Key   string
	Index int // -1 when this segment has no index suffix
}

// splitPath parses "interfaces[0].ip" into [{Key:"interfaces",Index:0},{Key:"ip",Index:-1}].
// Multiple [N] suffixes ("a[0][1]") yield successive index segments.
func splitPath(path string) []pathSeg {
	out := []pathSeg{}
	for _, raw := range strings.Split(path, ".") {
		// Extract key + bracket suffixes.
		key := raw
		var indices []int
		for {
			ob := strings.Index(key, "[")
			if ob < 0 {
				break
			}
			cb := strings.Index(key[ob:], "]")
			if cb < 0 {
				break
			}
			cb += ob
			n, err := strconv.Atoi(key[ob+1 : cb])
			if err != nil {
				break
			}
			indices = append(indices, n)
			key = key[:ob] + key[cb+1:]
		}
		if key != "" {
			out = append(out, pathSeg{Key: key, Index: -1})
		}
		for _, idx := range indices {
			out = append(out, pathSeg{Key: "", Index: idx})
		}
	}
	return out
}

// setAtSegs recursively splices value into tree.
func setAtSegs(tree any, segs []pathSeg, value any) (any, error) {
	if len(segs) == 0 {
		return value, nil
	}
	seg := segs[0]
	rest := segs[1:]

	// Index segment — descend into array.
	if seg.Index >= 0 {
		arr, _ := tree.([]any)
		// Pad with nil up to the requested index.
		for len(arr) <= seg.Index {
			arr = append(arr, nil)
		}
		child, err := setAtSegs(arr[seg.Index], rest, value)
		if err != nil {
			return nil, err
		}
		arr[seg.Index] = child
		return arr, nil
	}

	// Key segment — descend into map.
	m, _ := tree.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	child, err := setAtSegs(m[seg.Key], rest, value)
	if err != nil {
		return nil, err
	}
	m[seg.Key] = child
	return m, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// nodeRoles returns the role labels applicable to this node, used by the
// resolver. We treat each node tag prefixed with "role:" as a role; tags
// without that prefix don't override anything (they're free-form filters).
//
// Convention rationale: role-scoped variants on a 200-node cluster need a
// stable, low-cardinality key.  cfg.Tags is high-cardinality (rack labels,
// project codes, ad-hoc filters); requiring the "role:" prefix keeps the
// resolver from over-matching on every tag.
func nodeRoles(cfg api.NodeConfig) []string {
	roles := []string{}
	for _, t := range cfg.Tags {
		if strings.HasPrefix(t, "role:") {
			roles = append(roles, strings.TrimPrefix(t, "role:"))
		}
	}
	return roles
}

func toVariantResponse(v db.NodeConfigVariant) VariantResponse {
	return VariantResponse{
		ID:            v.ID,
		NodeID:        v.NodeID,
		AttributePath: v.AttributePath,
		Value:         json.RawMessage(v.ValueJSON),
		ScopeKind:     string(v.ScopeKind),
		ScopeID:       v.ScopeID,
		CreatedAt:     v.CreatedAt,
	}
}

