package ldap

// staging.go — two-stage commit intercept for LDAP mutation handlers (#154).
//
// When a mutation request arrives with ?stage=true (or X-Clustr-Stage: 1),
// the handler serialises the raw request body into the pending_changes table
// and returns 202 Accepted without touching the LDAP directory. The operator
// can later POST /api/v1/changes/commit to replay the changes through the
// normal immediate-apply code paths.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// StagingIface is the DB subset required for staging operations.
// Satisfied by *db.DB; extracted for testability.
type StagingIface interface {
	PendingChangesInsert(ctx context.Context, c db.PendingChange) error
}

// isStageRequest returns true when the request has opted into staging.
// Opt-in triggers:
//   - query param ?stage=true
//   - header X-Clustr-Stage: 1
func isStageRequest(r *http.Request) bool {
	if r.URL.Query().Get("stage") == "true" {
		return true
	}
	if r.Header.Get("X-Clustr-Stage") == "1" {
		return true
	}
	return false
}

// tryStageLDAP checks whether the request should be staged.
// If staging is requested, it reads the request body (resetting r.Body so callers
// can still decode it), writes a PendingChange row, and responds 202 Accepted —
// returning true so the caller can short-circuit the immediate-apply path.
//
// kind is e.g. "ldap_user"; target is the uid/cn being mutated; actorID is from
// the request context.
func tryStageLDAP(w http.ResponseWriter, r *http.Request, stagingDB StagingIface, kind, target, actorID string) bool {
	if !isStageRequest(r) {
		return false
	}

	// Read and replace body so the caller can still decode it if needed.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("ldap staging: read body failed")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to read request body for staging"})
		return true
	}

	// Ensure payload is valid JSON (or wrap it if not).
	payload := string(bodyBytes)
	if !json.Valid(bodyBytes) {
		payload = `{"raw":"` + payload + `"}`
	}

	c := db.PendingChange{
		ID:        uuid.New().String(),
		Kind:      kind,
		Target:    target,
		Payload:   payload,
		CreatedBy: actorID,
		CreatedAt: time.Now().Unix(),
	}
	if err := stagingDB.PendingChangesInsert(r.Context(), c); err != nil {
		log.Error().Err(err).Str("kind", kind).Str("target", target).Msg("ldap staging: insert failed")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "staging insert failed"})
		return true
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"staged":     true,
		"id":         c.ID,
		"kind":       kind,
		"target":     target,
		"created_at": c.CreatedAt,
	})
	return true
}
