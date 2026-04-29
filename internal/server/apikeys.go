package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
	"github.com/sqoia-dev/clustr/pkg/api"
	"github.com/sqoia-dev/clustr/internal/db"
)

// generateRawKey generates a cryptographically secure 32-byte random key
// and returns its hex encoding (64 chars). This is the value the operator
// stores; only the SHA-256 hash is persisted.
func generateRawKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// BootstrapDefaultUser creates the default clustr/clustr admin account on first run
// (ADR-0007). Only runs when the users table is completely empty.
// Logs a SECURITY warning to stderr — operator must change the password on first login.
func BootstrapDefaultUser(ctx context.Context, database *db.DB) error {
	count, err := database.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap default user: count: %w", err)
	}
	if count > 0 {
		return nil // users already exist; do not re-create
	}

	// Hash "clustr" with bcrypt cost 12.
	hash, err := bcrypt.GenerateFromPassword([]byte("clustr"), 12)
	if err != nil {
		return fmt.Errorf("bootstrap default user: bcrypt: %w", err)
	}

	rec := db.UserRecord{
		ID:                 uuid.New().String(),
		Username:           "clustr",
		PasswordHash:       string(hash),
		Role:               db.UserRoleAdmin,
		MustChangePassword: false,
		CreatedAt:          time.Now(),
	}
	if err := database.CreateUser(ctx, rec); err != nil {
		return fmt.Errorf("bootstrap default user: insert: %w", err)
	}

	log.Warn().
		Str("username", "clustr").
		Str("role", "admin").
		Msg("SECURITY: default credentials clustr/clustr are active — change the password via Settings when ready")

	return nil
}

// BootstrapAdminKey checks whether any admin key exists in the database.
// If none exists, it generates one, persists the hash, and prints the raw
// key to stdout ONCE. The operator must capture it immediately.
// Called during server startup before accepting traffic.
func BootstrapAdminKey(ctx context.Context, database *db.DB) error {
	count, err := database.CountAPIKeysByScope(ctx, api.KeyScopeAdmin)
	if err != nil {
		return fmt.Errorf("bootstrap admin key: %w", err)
	}
	if count > 0 {
		return nil // keys already exist, nothing to do
	}

	raw, err := generateRawKey()
	if err != nil {
		return err
	}

	rec := db.APIKeyRecord{
		ID:          uuid.New().String(),
		Scope:       api.KeyScopeAdmin,
		KeyHash:     sha256Hex(raw),
		Label:       "bootstrap",
		Description: "bootstrap admin key (auto-generated on first start)",
		CreatedAt:   time.Now(),
	}
	if err := database.CreateAPIKey(ctx, rec); err != nil {
		return fmt.Errorf("bootstrap admin key: persist: %w", err)
	}

	// Print to stdout (operator captures this) and log a warning.
	// Only ever printed once — there is no recovery path if lost; rotate with apikey create.
	fmt.Fprintf(os.Stdout, "\n"+
		"╔══════════════════════════════════════════════════════════════════╗\n"+
		"║              CLUSTR BOOTSTRAP ADMIN API KEY                      ║\n"+
		"║  Save this key — it will NOT be shown again.                    ║\n"+
		"╠══════════════════════════════════════════════════════════════════╣\n"+
		"║  clustr-admin-%s  ║\n"+
		"╚══════════════════════════════════════════════════════════════════╝\n\n",
		raw,
	)
	log.Warn().
		Str("key_id", rec.ID).
		Str("scope", string(rec.Scope)).
		Msg("bootstrap: generated initial admin API key — capture it from stdout now")

	return nil
}

// CreateAPIKey generates a new key for the given scope, persists the hash,
// and returns the raw key to the caller (CLI prints it once).
func CreateAPIKey(ctx context.Context, database *db.DB, scope api.KeyScope, description string) (rawKey string, id string, err error) {
	raw, err := generateRawKey()
	if err != nil {
		return "", "", err
	}

	rec := db.APIKeyRecord{
		ID:          uuid.New().String(),
		Scope:       scope,
		KeyHash:     sha256Hex(raw),
		Description: description,
		CreatedAt:   time.Now(),
	}
	if err := database.CreateAPIKey(ctx, rec); err != nil {
		return "", "", fmt.Errorf("create api key: %w", err)
	}

	return raw, rec.ID, nil
}

// CreateAPIKeyFull generates a new key with label, created_by, and optional expiry.
// Returns the raw key (never stored), the record ID, and the full record for the response.
func CreateAPIKeyFull(ctx context.Context, database *db.DB, scope api.KeyScope, nodeID, label, createdBy string, expiresAt *time.Time) (rawKey string, rec db.APIKeyRecord, err error) {
	raw, err := generateRawKey()
	if err != nil {
		return "", db.APIKeyRecord{}, err
	}

	rec = db.APIKeyRecord{
		ID:        uuid.New().String(),
		Scope:     scope,
		NodeID:    nodeID,
		KeyHash:   sha256Hex(raw),
		Label:     label,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	if err := database.CreateAPIKey(ctx, rec); err != nil {
		return "", db.APIKeyRecord{}, fmt.Errorf("create api key: %w", err)
	}

	return raw, rec, nil
}

// CreateNodeScopedKey mints a fresh node-scoped API key bound to nodeID with a
// 30-day TTL. Any existing node-scoped keys for the same node are revoked atomically
// in the same database transaction as the insert, eliminating the window between
// revoke and create where the node would temporarily have no valid key.
//
// Returns the raw key (prefix: clustr-node-<raw>) for embedding in the iPXE cmdline.
// The raw key is never stored — only its SHA-256 hash is persisted.
func CreateNodeScopedKey(ctx context.Context, database *db.DB, nodeID string) (rawKey string, err error) {
	raw, err := generateRawKey()
	if err != nil {
		return "", err
	}

	exp := time.Now().Add(30 * 24 * time.Hour)
	rec := db.APIKeyRecord{
		ID:          uuid.New().String(),
		Scope:       api.KeyScopeNode,
		NodeID:      nodeID,
		KeyHash:     sha256Hex(raw),
		Label:       "node-deploy-token",
		Description: "node-scoped deploy token (auto-generated at PXE serve time)",
		CreatedAt:   time.Now(),
		ExpiresAt:   &exp,
	}

	// Revoke old keys and insert the new one atomically — no window where the
	// node is left without a valid key between the two operations.
	if err := database.RevokeAndCreateNodeScopedKey(ctx, nodeID, rec); err != nil {
		return "", fmt.Errorf("create node scoped key: %w", err)
	}

	log.Info().
		Str("node_id", nodeID).
		Str("key_id", rec.ID).
		Time("expires_at", exp).
		Msg("node-scoped deploy token minted")

	return raw, nil
}
