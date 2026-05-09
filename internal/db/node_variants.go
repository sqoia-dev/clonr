// Package db — node_variants.go implements the per-attribute overlay store for
// Sprint 44 VARIANTS-SYSTEM.
//
// A variant is one (scope_kind, scope_id, attribute_path, value_json) tuple
// stored in node_config_variants (migration 109).  At resolve time the variants
// applicable to a given node are fetched in priority order:
//
//	role        (lowest priority)
//	group
//	node-direct (highest priority)
//
// Higher-priority variants overwrite lower-priority ones for the same
// attribute_path.  The base NodeConfig (read from node_configs) is the floor —
// when no variant has anything to say about a path, the base value is left
// unchanged.
//
// attribute_path is interpreted by the API-layer applier (see handlers/variants.go);
// this file only stores raw rows.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// VariantScopeKind enumerates the variant scope types.
type VariantScopeKind string

const (
	VariantScopeGlobal VariantScopeKind = "global"
	VariantScopeGroup  VariantScopeKind = "group"
	VariantScopeRole   VariantScopeKind = "role"
)

// IsValid reports whether v is one of the recognised scope kinds.
func (v VariantScopeKind) IsValid() bool {
	switch v {
	case VariantScopeGlobal, VariantScopeGroup, VariantScopeRole:
		return true
	}
	return false
}

// NodeConfigVariant is a single variant row.
//
// NodeID is set when the variant is direct-on-node (scope_kind=global,
// node_id!=""); for "group" and "role" scopes NodeID is empty and ScopeID
// points at the group_id or role label respectively.
//
// ValueJSON is the raw JSON-encoded value to splice in at AttributePath. The
// applier in handlers/variants.go parses this against api.NodeConfig.
type NodeConfigVariant struct {
	ID            string
	NodeID        string // optional; non-empty implies scope_kind=global node-direct
	AttributePath string
	ValueJSON     string
	ScopeKind     VariantScopeKind
	ScopeID       string // group_id, role label, or "" for node-direct global
	CreatedAt     time.Time
}

// CreateVariant inserts a new variant row. Caller must populate ID (UUID) and
// CreatedAt (UTC). Returns api.ErrBadRequest on invalid scope kind.
func (db *DB) CreateVariant(ctx context.Context, v NodeConfigVariant) error {
	if !v.ScopeKind.IsValid() {
		return fmt.Errorf("db: invalid variant scope_kind %q", v.ScopeKind)
	}
	if strings.TrimSpace(v.AttributePath) == "" {
		return fmt.Errorf("db: variant attribute_path required")
	}
	if strings.TrimSpace(v.ValueJSON) == "" {
		return fmt.Errorf("db: variant value_json required")
	}

	// Normalise nullable node_id — SQLite stores empty string as the empty
	// string by default, but we want NULL so the index can skip those rows.
	var nodeIDArg interface{}
	if v.NodeID == "" {
		nodeIDArg = nil
	} else {
		nodeIDArg = v.NodeID
	}

	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO node_config_variants
			(id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, nodeIDArg, v.AttributePath, v.ValueJSON,
		string(v.ScopeKind), v.ScopeID, v.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: insert variant: %w", err)
	}
	return nil
}

// DeleteVariant removes a variant by ID. Returns api.ErrNotFound when no row
// matched.
func (db *DB) DeleteVariant(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM node_config_variants WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete variant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete variant rows: %w", err)
	}
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// GetVariant returns one variant by ID.
func (db *DB) GetVariant(ctx context.Context, id string) (NodeConfigVariant, error) {
	var v NodeConfigVariant
	var nodeID sql.NullString
	var createdAt int64
	var scopeKind string
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
		FROM node_config_variants WHERE id = ?`, id)
	err := row.Scan(&v.ID, &nodeID, &v.AttributePath, &v.ValueJSON, &scopeKind, &v.ScopeID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeConfigVariant{}, api.ErrNotFound
	}
	if err != nil {
		return NodeConfigVariant{}, fmt.Errorf("db: get variant: %w", err)
	}
	v.ScopeKind = VariantScopeKind(scopeKind)
	if nodeID.Valid {
		v.NodeID = nodeID.String
	}
	v.CreatedAt = time.Unix(createdAt, 0).UTC()
	return v, nil
}

// ListVariants returns every variant row applicable to nodeID, in resolver
// priority order: role first, then group, then node-direct.
//
// Within each tier rows are ordered by created_at so deterministic UI rendering
// is possible. The applier overwrites earlier rows with later ones for the same
// attribute_path, which means later-tier variants beat earlier-tier — the
// caller does not need to be aware of priority during application.
//
// Implementation detail: this routine performs three queries rather than one
// JOIN-heavy statement so the (scope_kind, scope_id) and (node_id) indexes
// stay tight. At <1000 variants per node the round-trip cost is negligible.
func (db *DB) ListVariantsForNode(ctx context.Context, nodeID string, groupID string, roles []string) ([]NodeConfigVariant, error) {
	out := make([]NodeConfigVariant, 0, 8)

	// 1. Role-scoped variants (lowest priority).
	for _, role := range roles {
		if role == "" {
			continue
		}
		rs, err := db.queryVariants(ctx, `
			SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
			FROM node_config_variants
			WHERE scope_kind = 'role' AND scope_id = ?
			ORDER BY created_at ASC`, role)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}

	// 2. Group-scoped variants.
	if groupID != "" {
		rs, err := db.queryVariants(ctx, `
			SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
			FROM node_config_variants
			WHERE scope_kind = 'group' AND scope_id = ?
			ORDER BY created_at ASC`, groupID)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}

	// 3. Cluster-wide globals + node-direct variants (highest priority).
	//
	// Cluster-wide globals are the (scope_kind='global', node_id IS NULL)
	// rows: variants every node should pick up unless overridden by a
	// node-direct row.  The previous query restricted to node_id = ?
	// alone, which silently dropped them — Codex post-ship review
	// issue #4.
	//
	// Ordering: cluster-wide rows come FIRST in the slice (lower
	// priority) so the applier's "later wins" semantics let node-direct
	// rows overwrite them.  `(node_id IS NULL) DESC` puts the NULL rows
	// first; within each tier we still order by created_at ASC for
	// deterministic UI rendering.
	if nodeID != "" {
		rs, err := db.queryVariants(ctx, `
			SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
			FROM node_config_variants
			WHERE scope_kind = 'global' AND (node_id = ? OR node_id IS NULL)
			ORDER BY (node_id IS NULL) DESC, created_at ASC`, nodeID)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	} else {
		// Symmetric path for nodeless callers (rare): still surface the
		// cluster-wide rows so admin inspection / planning UIs see them.
		rs, err := db.queryVariants(ctx, `
			SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
			FROM node_config_variants
			WHERE scope_kind = 'global' AND node_id IS NULL
			ORDER BY created_at ASC`)
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}

	return out, nil
}

// ListAllVariants returns every variant row, ordered by scope_kind then
// created_at. Used by GET /api/v1/variants for admin inspection.
func (db *DB) ListAllVariants(ctx context.Context) ([]NodeConfigVariant, error) {
	return db.queryVariants(ctx, `
		SELECT id, node_id, attribute_path, value_json, scope_kind, scope_id, created_at
		FROM node_config_variants
		ORDER BY scope_kind, scope_id, created_at`)
}

func (db *DB) queryVariants(ctx context.Context, query string, args ...interface{}) ([]NodeConfigVariant, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: query variants: %w", err)
	}
	defer rows.Close()

	var out []NodeConfigVariant
	for rows.Next() {
		var v NodeConfigVariant
		var nodeID sql.NullString
		var createdAt int64
		var scopeKind string
		if err := rows.Scan(&v.ID, &nodeID, &v.AttributePath, &v.ValueJSON, &scopeKind, &v.ScopeID, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan variant: %w", err)
		}
		v.ScopeKind = VariantScopeKind(scopeKind)
		if nodeID.Valid {
			v.NodeID = nodeID.String
		}
		v.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows err: %w", err)
	}
	return out, nil
}

