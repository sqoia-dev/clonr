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
	KeyHash     string
	Description string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
}

// CreateAPIKey inserts a new hashed API key record.
func (db *DB) CreateAPIKey(ctx context.Context, rec APIKeyRecord) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO api_keys (id, scope, key_hash, description, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		rec.ID, string(rec.Scope), rec.KeyHash, rec.Description, rec.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create api_key: %w", err)
	}
	return nil
}

// LookupAPIKey finds an API key by its SHA-256 hash and returns the scope.
// Returns (scope, nil) on success; (_, sql.ErrNoRows) when not found.
func (db *DB) LookupAPIKey(ctx context.Context, keyHash string) (api.KeyScope, error) {
	var scope string
	err := db.sql.QueryRowContext(ctx,
		`SELECT scope FROM api_keys WHERE key_hash = ?`, keyHash,
	).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", fmt.Errorf("db: lookup api_key: %w", err)
	}

	// Touch last_used_at asynchronously — don't let a write failure block the request.
	go func() {
		_, _ = db.sql.Exec(
			`UPDATE api_keys SET last_used_at = ? WHERE key_hash = ?`,
			time.Now().Unix(), keyHash,
		)
	}()

	return api.KeyScope(scope), nil
}

// CountAPIKeysByScope returns the number of active keys for the given scope.
func (db *DB) CountAPIKeysByScope(ctx context.Context, scope api.KeyScope) (int, error) {
	var count int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_keys WHERE scope = ?`, string(scope),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: count api_keys: %w", err)
	}
	return count, nil
}

// ListAPIKeys returns all API key records (without the hash, for display).
func (db *DB) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, scope, key_hash, description, created_at, last_used_at
		 FROM api_keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list api_keys: %w", err)
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var scope string
		var createdAt int64
		var lastUsedAt sql.NullInt64
		if err := rows.Scan(&rec.ID, &scope, &rec.KeyHash, &rec.Description, &createdAt, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("db: scan api_key: %w", err)
		}
		rec.Scope = api.KeyScope(scope)
		rec.CreatedAt = time.Unix(createdAt, 0)
		if lastUsedAt.Valid {
			t := time.Unix(lastUsedAt.Int64, 0)
			rec.LastUsedAt = &t
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeleteAPIKey removes a key by ID.
func (db *DB) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete api_key: %w", err)
	}
	return nil
}
