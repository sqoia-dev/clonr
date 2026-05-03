package db

// gpg_keys.go — CRUD for user-managed GPG public keys (Sprint 3, GPG-1/2).
//
// Embedded release keys (clustr, rocky-9, EPEL-9) are static and are NOT
// stored here — they live in internal/server/keys.go as compiled-in bytes.
// This table holds operator-imported keys only.

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ListGPGKeys returns all user-imported keys ordered by created_at desc.
func (db *DB) ListGPGKeys(ctx context.Context) ([]api.GPGKey, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT fingerprint, owner, armored_key, created_at FROM gpg_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []api.GPGKey
	for rows.Next() {
		var k api.GPGKey
		var createdAt time.Time
		if err := rows.Scan(&k.Fingerprint, &k.Owner, &k.ArmoredKey, &createdAt); err != nil {
			return nil, err
		}
		k.CreatedAt = createdAt
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// GetGPGKey returns a single key by fingerprint. Returns sql.ErrNoRows when
// not found.
func (db *DB) GetGPGKey(ctx context.Context, fingerprint string) (api.GPGKey, error) {
	var k api.GPGKey
	var createdAt time.Time
	err := db.sql.QueryRowContext(ctx,
		`SELECT fingerprint, owner, armored_key, created_at FROM gpg_keys WHERE fingerprint = ?`,
		fingerprint,
	).Scan(&k.Fingerprint, &k.Owner, &k.ArmoredKey, &createdAt)
	if err != nil {
		return api.GPGKey{}, err
	}
	k.CreatedAt = createdAt
	return k, nil
}

// CreateGPGKey inserts a new key record. Returns an error wrapping
// "already_exists" if the fingerprint is already present.
func (db *DB) CreateGPGKey(ctx context.Context, k api.GPGKey) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO gpg_keys (fingerprint, owner, armored_key, created_at) VALUES (?, ?, ?, ?)`,
		k.Fingerprint, k.Owner, k.ArmoredKey, k.CreatedAt,
	)
	return err
}

// DeleteGPGKey removes a key by fingerprint. Returns sql.ErrNoRows when not
// found so callers can return a proper 404.
func (db *DB) DeleteGPGKey(ctx context.Context, fingerprint string) error {
	res, err := db.sql.ExecContext(ctx,
		`DELETE FROM gpg_keys WHERE fingerprint = ?`, fingerprint)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GPGKeyExists returns true when a key with the given fingerprint is already
// in the table. Used by the import handler to produce a useful error message.
func (db *DB) GPGKeyExists(ctx context.Context, fingerprint string) (bool, error) {
	_, err := db.GetGPGKey(ctx, fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
