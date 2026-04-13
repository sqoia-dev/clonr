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
	"github.com/sqoia-dev/clonr/pkg/api"
	"github.com/sqoia-dev/clonr/pkg/db"
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
		"║              CLONR BOOTSTRAP ADMIN API KEY                      ║\n"+
		"║  Save this key — it will NOT be shown again.                    ║\n"+
		"╠══════════════════════════════════════════════════════════════════╣\n"+
		"║  clonr-admin-%s  ║\n"+
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
