// Package handlers — webhooks.go implements the Settings > Webhooks CRUD API (S4-2).
//
// Routes (all under /api/v1, admin scope required):
//
//	GET    /admin/webhooks               — list all subscriptions
//	POST   /admin/webhooks               — create subscription
//	GET    /admin/webhooks/{id}          — get subscription
//	PUT    /admin/webhooks/{id}          — update subscription
//	DELETE /admin/webhooks/{id}          — delete subscription
//	GET    /admin/webhooks/{id}/deliveries — list recent delivery attempts
package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// WebhooksHandler manages webhook subscription CRUD.
type WebhooksHandler struct {
	DB *db.DB
}

// validEvents is the set of allowed event types for webhook subscriptions.
var validEvents = map[string]struct{}{
	"deploy.complete":     {},
	"deploy.failed":       {},
	"verify_boot.timeout": {},
	"image.ready":         {},
}

// webhookSubRequest is the JSON body for create/update.
type webhookSubRequest struct {
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Secret  string   `json:"secret"`
	Enabled *bool    `json:"enabled"`
}

// webhookSubResponse is the JSON representation of a subscription.
type webhookSubResponse struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func subToResponse(sub db.WebhookSubscription) webhookSubResponse {
	events := sub.Events
	if events == nil {
		events = []string{}
	}
	return webhookSubResponse{
		ID:        sub.ID,
		URL:       sub.URL,
		Events:    events,
		Enabled:   sub.Enabled,
		CreatedAt: sub.CreatedAt,
		UpdatedAt: sub.UpdatedAt,
	}
}

// HandleList handles GET /api/v1/admin/webhooks.
func (h *WebhooksHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	subs, err := h.DB.ListWebhookSubscriptions(r.Context(), "")
	if err != nil {
		log.Error().Err(err).Msg("webhooks: list")
		writeError(w, err)
		return
	}
	out := make([]webhookSubResponse, len(subs))
	for i, s := range subs {
		out[i] = subToResponse(s)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"webhooks": out, "total": len(out)})
}

// HandleCreate handles POST /api/v1/admin/webhooks.
func (h *WebhooksHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req webhookSubRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeValidationError(w, "url is required")
		return
	}
	if len(req.Events) == 0 {
		writeValidationError(w, "at least one event is required")
		return
	}
	for _, ev := range req.Events {
		if _, ok := validEvents[ev]; !ok {
			writeValidationError(w, "unknown event type: "+ev+". Valid: deploy.complete, deploy.failed, verify_boot.timeout, image.ready")
			return
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	sub := db.WebhookSubscription{
		ID:      uuid.New().String(),
		URL:     req.URL,
		Events:  req.Events,
		Secret:  req.Secret,
		Enabled: enabled,
	}
	if err := h.DB.CreateWebhookSubscription(r.Context(), sub); err != nil {
		log.Error().Err(err).Msg("webhooks: create")
		writeError(w, err)
		return
	}

	created, err := h.DB.GetWebhookSubscription(r.Context(), sub.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, subToResponse(created))
}

// HandleGet handles GET /api/v1/admin/webhooks/{id}.
func (h *WebhooksHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sub, err := h.DB.GetWebhookSubscription(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subToResponse(sub))
}

// HandleUpdate handles PUT /api/v1/admin/webhooks/{id}.
func (h *WebhooksHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.DB.GetWebhookSubscription(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	var req webhookSubRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.URL == "" {
		writeValidationError(w, "url is required")
		return
	}
	if len(req.Events) == 0 {
		writeValidationError(w, "at least one event is required")
		return
	}
	for _, ev := range req.Events {
		if _, ok := validEvents[ev]; !ok {
			writeValidationError(w, "unknown event type: "+ev)
			return
		}
	}

	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	updated := db.WebhookSubscription{
		ID:      id,
		URL:     req.URL,
		Events:  req.Events,
		Secret:  req.Secret,
		Enabled: enabled,
	}
	if err := h.DB.UpdateWebhookSubscription(r.Context(), updated); err != nil {
		log.Error().Err(err).Str("webhook_id", id).Msg("webhooks: update")
		writeError(w, err)
		return
	}

	result, err := h.DB.GetWebhookSubscription(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subToResponse(result))
}

// HandleDelete handles DELETE /api/v1/admin/webhooks/{id}.
func (h *WebhooksHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.DB.DeleteWebhookSubscription(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListDeliveries handles GET /api/v1/admin/webhooks/{id}/deliveries.
func (h *WebhooksHandler) HandleListDeliveries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.DB.GetWebhookSubscription(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	deliveries, err := h.DB.ListWebhookDeliveries(r.Context(), id)
	if err != nil {
		log.Error().Err(err).Str("webhook_id", id).Msg("webhooks: list deliveries")
		writeError(w, err)
		return
	}
	if deliveries == nil {
		deliveries = []db.WebhookDelivery{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deliveries": deliveries, "total": len(deliveries)})
}
