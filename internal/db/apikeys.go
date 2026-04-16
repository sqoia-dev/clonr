package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
)

// APIKeyRecord is the persisted representation of an API key (hash only, never the raw key).
type APIKeyRecord struct {
	ID          string
	Scope       api.KeyScope
	NodeID      string // non-empty for node-scoped keys; identifies the bound node
	KeyHash     string
	Description string
	Label       string     // human-readable operator label, e.g. "robert-laptop"
	CreatedBy   string     // label of key/session that created this key (audit attribution)
	CreatedAt   time.Time
	ExpiresAt   *time.Time // nil = no expiry
	RevokedAt   *time.Time // non-nil = soft-deleted, rejected by middleware
	LastUsedAt  *time.Time
}

// APIKeyLookupResult is returned by LookupAPIKey.
type APIKeyLookupResult struct {
	ID     string
	Scope  api.KeyScope
	NodeID string // set only for node-scoped keys
	Label  string
}

// ErrRevoked is returned by LookupAPIKey when a key exists but has been revoked.
var ErrRevoked = fmt.Errorf("api key revoked")

// CreateAPIKey inserts a new hashed API key record.
func (db *DB) CreateAPIKey(ctx context.Context, rec APIKeyRecord) error {
	var expiresAt interface{}
	if rec.ExpiresAt != nil {
		expiresAt = rec.ExpiresAt.Unix()
	}
	var nodeID interface{}
	if rec.NodeID != "" {
		nodeID = rec.NodeID
	}
	var label interface{}
	if rec.Label != "" {
		label = rec.Label
	}
	var createdBy interface{}
	if rec.CreatedBy != "" {
		createdBy = rec.CreatedBy
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO api_keys (id, scope, node_id, key_hash, description, label, created_by, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, string(rec.Scope), nodeID, rec.KeyHash, rec.Description, label, createdBy, rec.CreatedAt.Unix(), expiresAt,
	)
	if err != nil {
		return fmt.Errorf("db: create api_key: %w", err)
	}
	return nil
}

// LookupAPIKey finds an API key by its SHA-256 hash.
// Returns sql.ErrNoRows when not found.
// Returns ErrExpired when found but past its TTL.
// Returns ErrRevoked when found but revoked.
// On success, updates last_used_at asynchronously (fire-and-forget, never blocks the request).
func (db *DB) LookupAPIKey(ctx context.Context, keyHash string) (APIKeyLookupResult, error) {
	var id string
	var scope string
	var nodeID sql.NullString
	var label sql.NullString
	var expiresAt sql.NullInt64
	var revokedAt sql.NullInt64

	err := db.sql.QueryRowContext(ctx,
		`SELECT id, scope, node_id, label, expires_at, revoked_at FROM api_keys WHERE key_hash = ?`, keyHash,
	).Scan(&id, &scope, &nodeID, &label, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyLookupResult{}, sql.ErrNoRows
	}
	if err != nil {
		return APIKeyLookupResult{}, fmt.Errorf("db: lookup api_key: %w", err)
	}

	// Reject revoked keys before expiry check (revocation takes precedence).
	if revokedAt.Valid {
		return APIKeyLookupResult{}, ErrRevoked
	}

	// Enforce TTL if set.
	if expiresAt.Valid && time.Now().Unix() > expiresAt.Int64 {
		return APIKeyLookupResult{}, ErrExpired
	}

	// Batch last_used_at update — the background flusher writes it every 30s.
	db.lastUsedMu.Lock()
	db.lastUsedBatch[keyHash] = time.Now().Unix()
	db.lastUsedMu.Unlock()

	result := APIKeyLookupResult{
		ID:    id,
		Scope: api.KeyScope(scope),
	}
	if nodeID.Valid {
		result.NodeID = nodeID.String
	}
	if label.Valid {
		result.Label = label.String
	}
	return result, nil
}

// RevokeNodeScopedKeys soft-deletes all node-scoped keys bound to the given nodeID.
// Called when a new node-scoped key is minted so that only one live token exists
// per node at any time.
func (db *DB) RevokeNodeScopedKeys(ctx context.Context, nodeID string) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE node_id = ? AND scope = 'node' AND revoked_at IS NULL`,
		time.Now().Unix(), nodeID,
	)
	if err != nil {
		return fmt.Errorf("db: revoke node scoped keys: %w", err)
	}
	return nil
}

// CountAPIKeysByScope returns the number of active (non-revoked, non-expired) keys for the given scope.
func (db *DB) CountAPIKeysByScope(ctx context.Context, scope api.KeyScope) (int, error) {
	var count int
	now := time.Now().Unix()
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys
		 WHERE scope = ?
		   AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)`,
		string(scope), now,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: count api_keys: %w", err)
	}
	return count, nil
}

// ListAPIKeys returns all non-revoked API key records (without the hash, for display).
func (db *DB) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, scope, node_id, key_hash, description, label, created_by, created_at, expires_at, revoked_at, last_used_at
		 FROM api_keys WHERE revoked_at IS NULL ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list api_keys: %w", err)
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var scope string
		var nodeID sql.NullString
		var label sql.NullString
		var createdBy sql.NullString
		var createdAt int64
		var expiresAt sql.NullInt64
		var revokedAt sql.NullInt64
		var lastUsedAt sql.NullInt64
		if err := rows.Scan(&rec.ID, &scope, &nodeID, &rec.KeyHash, &rec.Description, &label, &createdBy, &createdAt, &expiresAt, &revokedAt, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("db: scan api_key: %w", err)
		}
		rec.Scope = api.KeyScope(scope)
		rec.CreatedAt = time.Unix(createdAt, 0)
		if nodeID.Valid {
			rec.NodeID = nodeID.String
		}
		if label.Valid {
			rec.Label = label.String
		}
		if createdBy.Valid {
			rec.CreatedBy = createdBy.String
		}
		if expiresAt.Valid {
			t := time.Unix(expiresAt.Int64, 0)
			rec.ExpiresAt = &t
		}
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0)
			rec.RevokedAt = &t
		}
		if lastUsedAt.Valid {
			t := time.Unix(lastUsedAt.Int64, 0)
			rec.LastUsedAt = &t
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetAPIKey returns a single API key by ID. Returns sql.ErrNoRows when not found.
func (db *DB) GetAPIKey(ctx context.Context, id string) (APIKeyRecord, error) {
	var rec APIKeyRecord
	var scope string
	var nodeID sql.NullString
	var label sql.NullString
	var createdBy sql.NullString
	var createdAt int64
	var expiresAt sql.NullInt64
	var revokedAt sql.NullInt64
	var lastUsedAt sql.NullInt64

	err := db.sql.QueryRowContext(ctx,
		`SELECT id, scope, node_id, key_hash, description, label, created_by, created_at, expires_at, revoked_at, last_used_at
		 FROM api_keys WHERE id = ?`, id,
	).Scan(&rec.ID, &scope, &nodeID, &rec.KeyHash, &rec.Description, &label, &createdBy, &createdAt, &expiresAt, &revokedAt, &lastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, sql.ErrNoRows
	}
	if err != nil {
		return APIKeyRecord{}, fmt.Errorf("db: get api_key: %w", err)
	}
	rec.Scope = api.KeyScope(scope)
	rec.CreatedAt = time.Unix(createdAt, 0)
	if nodeID.Valid {
		rec.NodeID = nodeID.String
	}
	if label.Valid {
		rec.Label = label.String
	}
	if createdBy.Valid {
		rec.CreatedBy = createdBy.String
	}
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0)
		rec.ExpiresAt = &t
	}
	if revokedAt.Valid {
		t := time.Unix(revokedAt.Int64, 0)
		rec.RevokedAt = &t
	}
	if lastUsedAt.Valid {
		t := time.Unix(lastUsedAt.Int64, 0)
		rec.LastUsedAt = &t
	}
	return rec, nil
}

// RevokeAPIKey soft-deletes a key by ID (sets revoked_at = now).
// Returns sql.ErrNoRows when not found or already revoked.
func (db *DB) RevokeAPIKey(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("db: revoke api_key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: revoke api_key rows: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteAPIKey removes a key by ID (hard delete, used internally for node-scoped key cleanup).
func (db *DB) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete api_key: %w", err)
	}
	return nil
}

// UpdateAPIKeyExpiry sets the expires_at column for a key by ID. Used in tests to force expiry.
func (db *DB) UpdateAPIKeyExpiry(ctx context.Context, id string, expiresAt *time.Time) error {
	var val interface{}
	if expiresAt != nil {
		val = expiresAt.Unix()
	}
	_, err := db.sql.ExecContext(ctx, `UPDATE api_keys SET expires_at = ? WHERE id = ?`, val, id)
	if err != nil {
		return fmt.Errorf("db: update api_key expiry: %w", err)
	}
	return nil
}
