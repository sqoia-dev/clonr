package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/multicast"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// MulticastDB is the subset of db.DB required by MulticastHandler.
type MulticastDB interface {
	MulticastGetSession(ctx context.Context, id string) (multicast.Session, error)
}

// MulticastHandler implements the multicast session API endpoints.
//
//	POST   /api/v1/multicast/enqueue
//	GET    /api/v1/multicast/sessions/{id}/wait
//	POST   /api/v1/multicast/sessions/{id}/members/{node_id}/outcome
type MulticastHandler struct {
	Scheduler *multicast.Scheduler
	DB        MulticastDB
}

// EnqueueRequest is the body for POST /api/v1/multicast/enqueue.
type MulticastEnqueueRequest struct {
	ImageID        string `json:"image_id"`
	LayoutID       string `json:"layout_id,omitempty"`
	NodeID         string `json:"node_id"`
	ForceImmediate bool   `json:"force_immediate,omitempty"`
}

// EnqueueResponse is the response body for POST /api/v1/multicast/enqueue.
type MulticastEnqueueResponse struct {
	SessionID string `json:"session_id"`
}

// OutcomeRequest is the body for POST /api/v1/multicast/sessions/{id}/members/{node_id}/outcome.
type MulticastOutcomeRequest struct {
	Outcome string `json:"outcome"` // success|failed|fellback_unicast
}

// Enqueue handles POST /api/v1/multicast/enqueue.
// Enrolls a node in a multicast session for the given image and layout.
func (h *MulticastHandler) Enqueue(w http.ResponseWriter, r *http.Request) {
	var req MulticastEnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ImageID == "" {
		http.Error(w, "image_id is required", http.StatusBadRequest)
		return
	}
	if req.NodeID == "" {
		http.Error(w, "node_id is required", http.StatusBadRequest)
		return
	}

	sessionID, err := h.Scheduler.Enqueue(r.Context(), multicast.EnqueueRequest{
		ImageID:        req.ImageID,
		LayoutID:       req.LayoutID,
		NodeID:         req.NodeID,
		ForceImmediate: req.ForceImmediate,
	})
	if err != nil {
		log.Error().Err(err).Str("image_id", req.ImageID).Str("node_id", req.NodeID).
			Msg("multicast: enqueue failed")
		http.Error(w, "failed to enqueue multicast session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(MulticastEnqueueResponse{SessionID: sessionID})
}

// Wait handles GET /api/v1/multicast/sessions/{id}/wait.
//
// Long-poll endpoint: nodes call this after enrolling in a session to wait
// for the session descriptor (multicast stream parameters). The handler
// blocks up to waitPollDuration per request, then returns 202 with a
// Retry-After header so the node retries.
//
// When the session fires, returns 200 with the session descriptor.
// When the session fails, returns 200 with {"fallback": true} so the node
// falls back to unicast HTTP fetch.
//
// Query params:
//
//	node_id — the enrolling node ID (required for notified_at tracking)
func (h *MulticastHandler) Wait(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	nodeID := r.URL.Query().Get("node_id")
	if sessionID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}

	// Each long-poll request blocks up to waitPollDuration.
	// The node retries until it gets a non-202 response.
	const waitPollDuration = 5 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), waitPollDuration)
	defer cancel()

	result, err := h.Scheduler.Wait(ctx, sessionID, nodeID)
	if err != nil {
		// Context deadline exceeded — return 202 so node retries.
		if ctx.Err() != nil {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusAccepted)
			return
		}
		log.Error().Err(err).Str("session_id", sessionID).Msg("multicast: wait error")
		http.Error(w, "wait error", http.StatusInternalServerError)
		return
	}

	type waitResponse struct {
		Fallback   bool                          `json:"fallback,omitempty"`
		Descriptor *multicast.SessionDescriptor  `json:"descriptor,omitempty"`
	}
	resp := waitResponse{
		Fallback:   result.Fallback,
		Descriptor: result.Descriptor,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// RecordOutcome handles POST /api/v1/multicast/sessions/{id}/members/{node_id}/outcome.
// Called by the deploy agent on the node after udp-receiver exits.
func (h *MulticastHandler) RecordOutcome(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	nodeID := chi.URLParam(r, "node_id")
	if sessionID == "" || nodeID == "" {
		http.Error(w, "session_id and node_id are required", http.StatusBadRequest)
		return
	}

	var req MulticastOutcomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var outcome multicast.Outcome
	switch req.Outcome {
	case string(multicast.OutcomeSuccess):
		outcome = multicast.OutcomeSuccess
	case string(multicast.OutcomeFailed):
		outcome = multicast.OutcomeFailed
	case string(multicast.OutcomeFellbackUnicast):
		outcome = multicast.OutcomeFellbackUnicast
	default:
		http.Error(w, "outcome must be success|failed|fellback_unicast", http.StatusBadRequest)
		return
	}

	if err := h.Scheduler.RecordOutcome(r.Context(), sessionID, nodeID, outcome); err != nil {
		log.Error().Err(err).Str("session_id", sessionID).Str("node_id", nodeID).
			Msg("multicast: record outcome failed")
		http.Error(w, "failed to record outcome", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetSession handles GET /api/v1/multicast/sessions/{id}.
// Returns the session state for operator visibility.
func (h *MulticastHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	s, err := h.DB.MulticastGetSession(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("session_id", id).Msg("multicast: get session")
		http.Error(w, api.ErrNotFound.Error(), http.StatusNotFound)
		return
	}

	type sessionResponse struct {
		ID             string  `json:"id"`
		ImageID        string  `json:"image_id"`
		LayoutID       string  `json:"layout_id,omitempty"`
		State          string  `json:"state"`
		MulticastGroup string  `json:"multicast_group"`
		SenderPort     int     `json:"sender_port"`
		RateBPS        int64   `json:"rate_bps"`
		MemberCount    int     `json:"member_count"`
		SuccessCount   int     `json:"success_count"`
		Error          string  `json:"error,omitempty"`
	}
	resp := sessionResponse{
		ID:             s.ID,
		ImageID:        s.ImageID,
		LayoutID:       s.LayoutID,
		State:          string(s.State),
		MulticastGroup: s.MulticastGroup,
		SenderPort:     s.SenderPort,
		RateBPS:        s.RateBPS,
		MemberCount:    s.MemberCount,
		SuccessCount:   s.SuccessCount,
		Error:          s.Error,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
