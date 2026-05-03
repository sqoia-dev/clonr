package handlers

// Per-user notification preference handlers — Sprint E (E4, CF-11/CF-15 enhancements).
//
// Routes (any authenticated user, scoped to their own prefs):
//   GET  /api/v1/me/notification-prefs            — list effective prefs (merged default + overrides)
//   PUT  /api/v1/me/notification-prefs/{event}    — set delivery mode for one event type
//   POST /api/v1/me/notification-prefs/reset      — reset all overrides to defaults
//
// Admin only:
//   GET  /api/v1/admin/users/{id}/notification-prefs  — view any user's prefs

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// NotificationPrefsHandler provides HTTP handlers for per-user notification preferences.
type NotificationPrefsHandler struct {
	DB           *db.DB
	Audit        *db.AuditService
	// GetActorInfo extracts (actorID, actorLabel) from a request.
	// Injected by server.go to avoid import cycles — the same closure used by
	// other admin handlers.
	GetActorInfo func(r *http.Request) (string, string)
}

// userIDFromRequest extracts the user ID using the injected GetActorInfo closure,
// falling back to the actorFromContext helper that does the local-type trick.
func (h *NotificationPrefsHandler) userIDFromRequest(r *http.Request) string {
	if h.GetActorInfo != nil {
		id, _ := h.GetActorInfo(r)
		return id
	}
	// Fallback: use the actorFromContext helper (local-type trick for session auth).
	id, _ := actorFromContext(r.Context())
	return id
}

// HandleGetMyPrefs handles GET /api/v1/me/notification-prefs.
func (h *NotificationPrefsHandler) HandleGetMyPrefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := h.userIDFromRequest(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	prefs, err := h.DB.ListUserNotificationPrefs(ctx, userID)
	if err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("notif-prefs: list failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load notification preferences"})
		return
	}
	if prefs == nil {
		prefs = []db.NotificationPref{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"preferences": prefs})
}

// HandleSetMyPref handles PUT /api/v1/me/notification-prefs/{event}.
func (h *NotificationPrefsHandler) HandleSetMyPref(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eventType := chi.URLParam(r, "event")
	userID := h.userIDFromRequest(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	var body struct {
		DeliveryMode string `json:"delivery_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := h.DB.SetNotificationPref(ctx, userID, eventType, body.DeliveryMode); err != nil {
		log.Error().Err(err).Str("user_id", userID).Str("event", eventType).Msg("notif-prefs: set failed")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to set preference: " + err.Error()})
		return
	}

	h.Audit.Record(ctx, userID, "user:"+userID, "notification_pref.set",
		"user", userID, "",
		nil, map[string]string{"event_type": eventType, "delivery_mode": body.DeliveryMode})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":       userID,
		"event_type":    eventType,
		"delivery_mode": body.DeliveryMode,
	})
}

// HandleResetMyPrefs handles POST /api/v1/me/notification-prefs/reset.
func (h *NotificationPrefsHandler) HandleResetMyPrefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := h.userIDFromRequest(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	if err := h.DB.ResetNotificationPrefs(ctx, userID); err != nil {
		log.Error().Err(err).Str("user_id", userID).Msg("notif-prefs: reset failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reset preferences"})
		return
	}

	h.Audit.Record(ctx, userID, "user:"+userID, "notification_pref.reset",
		"user", userID, "", nil, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "preferences reset to defaults"})
}

// HandleAdminGetUserPrefs handles GET /api/v1/admin/users/{id}/notification-prefs.
func (h *NotificationPrefsHandler) HandleAdminGetUserPrefs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	targetUserID := chi.URLParam(r, "id")

	prefs, err := h.DB.ListUserNotificationPrefs(ctx, targetUserID)
	if err != nil {
		log.Error().Err(err).Str("target_user_id", targetUserID).Msg("notif-prefs: admin list failed")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load preferences"})
		return
	}
	if prefs == nil {
		prefs = []db.NotificationPref{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"user_id": targetUserID, "preferences": prefs})
}
