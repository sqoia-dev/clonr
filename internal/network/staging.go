package network

// staging.go — two-stage commit intercept for network mutation handlers (#154).

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
type StagingIface interface {
	PendingChangesInsert(ctx context.Context, c db.PendingChange) error
}

// isStageRequest returns true when the request has opted into staging.
func isStageRequest(r *http.Request) bool {
	if r.URL.Query().Get("stage") == "true" {
		return true
	}
	if r.Header.Get("X-Clustr-Stage") == "1" {
		return true
	}
	return false
}

// tryStageNetwork checks whether the request should be staged.
// Returns true (and writes the HTTP response) when staging was performed.
func tryStageNetwork(w http.ResponseWriter, r *http.Request, stagingDB StagingIface, kind, target, actorID string) bool {
	if !isStageRequest(r) {
		return false
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error().Err(err).Msg("network staging: read body failed")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to read request body for staging"})
		return true
	}

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
		log.Error().Err(err).Str("kind", kind).Str("target", target).Msg("network staging: insert failed")
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
