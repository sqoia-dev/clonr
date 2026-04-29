package handlers

// gpg_keys.go — REST endpoints for user-managed GPG public keys (Sprint 3, GPG-1/2/3).
//
// GET    /api/v1/gpg-keys               — list all keys (embedded + user-imported)
// POST   /api/v1/gpg-keys               — import an ASCII-armored public key block
// DELETE /api/v1/gpg-keys/{fingerprint} — remove a user-imported key
//
// The three embedded keys (clustr-release, RPM-GPG-KEY-rocky-9, RPM-GPG-KEY-EPEL-9)
// are returned by the list endpoint with source="embedded". They cannot be
// deleted via this API — they are compiled into the binary.

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/openpgp"        //nolint:staticcheck // deprecated pkg; no alternatives in deps
	"golang.org/x/crypto/openpgp/armor"  //nolint:staticcheck
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// EmbeddedGPGKey represents a compile-time key that cannot be deleted.
type EmbeddedGPGKey struct {
	Fingerprint string
	Owner       string
	ArmoredKey  []byte
}

// GPGKeysHandler handles /api/v1/gpg-keys routes.
type GPGKeysHandler struct {
	DB           *db.DB
	EmbeddedKeys []EmbeddedGPGKey
}

// ListGPGKeys handles GET /api/v1/gpg-keys.
// Returns embedded keys first, then user-imported keys (newest first).
func (h *GPGKeysHandler) ListGPGKeys(w http.ResponseWriter, r *http.Request) {
	// Build response: embedded keys first.
	var keys []api.GPGKey
	for _, ek := range h.EmbeddedKeys {
		fp := ek.Fingerprint
		if fp == "" {
			fp = fingerprintFromArmor(ek.ArmoredKey)
		}
		keys = append(keys, api.GPGKey{
			Fingerprint: fp,
			Owner:       ek.Owner,
			Source:      "embedded",
			CreatedAt:   time.Time{}, // zero — embedded at build time
		})
	}

	// Append user-imported keys.
	userKeys, err := h.DB.ListGPGKeys(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("gpg-keys: list db keys")
		writeError(w, err)
		return
	}
	for _, k := range userKeys {
		k.Source = "user"
		k.ArmoredKey = "" // omit full key in list response
		keys = append(keys, k)
	}

	writeJSON(w, http.StatusOK, api.ListGPGKeysResponse{Keys: keys})
}

// ImportGPGKey handles POST /api/v1/gpg-keys.
// Body: {"armored_key": "<ASCII-armored PGP public key block>", "owner": "..."}
// Validates the block, extracts the fingerprint, and imports it.
func (h *GPGKeysHandler) ImportGPGKey(w http.ResponseWriter, r *http.Request) {
	var req api.ImportGPGKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.ArmoredKey) == "" {
		writeValidationError(w, "armored_key is required")
		return
	}

	// Parse and validate the key block; extract fingerprint.
	entities, fingerprint, err := parseArmoredPublicKey([]byte(req.ArmoredKey))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("invalid PGP public key: %s", err.Error()),
			"code":  "invalid_key",
		})
		return
	}
	_ = entities // parsed for validation; fingerprint extracted

	// Check for duplicates.
	exists, err := h.DB.GPGKeyExists(r.Context(), fingerprint)
	if err != nil {
		log.Error().Err(err).Msg("gpg-keys: check exists")
		writeError(w, err)
		return
	}
	if exists {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("a key with fingerprint %s is already imported", fingerprint),
			"code":  "already_exists",
		})
		return
	}

	// Also reject if it matches an embedded key fingerprint.
	for _, ek := range h.EmbeddedKeys {
		ekFP := ek.Fingerprint
		if ekFP == "" {
			ekFP = fingerprintFromArmor(ek.ArmoredKey)
		}
		if strings.EqualFold(ekFP, fingerprint) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "this key is already present as an embedded release key",
				"code":  "already_exists",
			})
			return
		}
	}

	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		owner = "imported key"
	}

	k := api.GPGKey{
		Fingerprint: fingerprint,
		Owner:       owner,
		ArmoredKey:  strings.TrimSpace(req.ArmoredKey),
		Source:      "user",
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.DB.CreateGPGKey(r.Context(), k); err != nil {
		log.Error().Err(err).Msg("gpg-keys: create")
		writeError(w, err)
		return
	}

	// Return with armored_key included so the client can verify.
	writeJSON(w, http.StatusCreated, k)
}

// DeleteGPGKey handles DELETE /api/v1/gpg-keys/{fingerprint}.
// Only user-imported keys can be deleted; embedded keys return 403.
func (h *GPGKeysHandler) DeleteGPGKey(w http.ResponseWriter, r *http.Request) {
	fingerprint := chi.URLParam(r, "fingerprint")

	// Reject deletion of embedded keys.
	for _, ek := range h.EmbeddedKeys {
		ekFP := ek.Fingerprint
		if ekFP == "" {
			ekFP = fingerprintFromArmor(ek.ArmoredKey)
		}
		if strings.EqualFold(ekFP, fingerprint) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "embedded release keys cannot be deleted",
				"code":  "forbidden",
			})
			return
		}
	}

	if err := h.DB.DeleteGPGKey(r.Context(), fingerprint); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "gpg key not found",
				"code":  "not_found",
			})
			return
		}
		log.Error().Err(err).Msg("gpg-keys: delete")
		writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// parseArmoredPublicKey validates the ASCII-armored public key and returns the
// parsed openpgp entities and the 40-char hex fingerprint of the primary key.
func parseArmoredPublicKey(data []byte) (openpgp.EntityList, string, error) {
	block, err := armor.Decode(strings.NewReader(string(data)))
	if err != nil {
		return nil, "", fmt.Errorf("decode armor: %w", err)
	}
	if block.Type != openpgp.PublicKeyType {
		return nil, "", fmt.Errorf("expected %q, got %q", openpgp.PublicKeyType, block.Type)
	}
	entities, err := openpgp.ReadKeyRing(block.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read key ring: %w", err)
	}
	if len(entities) == 0 {
		return nil, "", fmt.Errorf("no keys found in armored block")
	}
	fp := hex.EncodeToString(entities[0].PrimaryKey.Fingerprint[:])
	return entities, fp, nil
}

// fingerprintFromArmor attempts to extract the fingerprint from an embedded
// key's armored bytes. Returns empty string on failure (non-fatal; embedded
// key still shows up in the list).
func fingerprintFromArmor(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	_, fp, err := parseArmoredPublicKey(data)
	if err != nil {
		return ""
	}
	return fp
}
