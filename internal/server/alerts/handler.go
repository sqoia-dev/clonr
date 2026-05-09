package alerts

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Handler exposes the SYSTEM-ALERT-FRAMEWORK HTTP endpoints.
//
// Routes (mount under /api/v1):
//
//	POST /system_alerts/push                   — push transient alert
//	POST /system_alerts/set/{key}/{device}     — set/upsert durable alert
//	POST /system_alerts/unset/{key}/{device}   — clear active alert
//	GET  /system_alerts                        — list current
//
// Device may be "-" to address an alert with no device scope; the URL
// segment is required by chi but the empty string is preserved server-side.
type Handler struct {
	Store *Store
}

// Mount registers all SYSTEM-ALERT-FRAMEWORK routes on r.  Caller is
// expected to be inside a /api/v1 sub-router with auth middleware applied.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/system_alerts/push", h.HandlePush)
	r.Post("/system_alerts/set/{key}/{device}", h.HandleSet)
	r.Post("/system_alerts/unset/{key}/{device}", h.HandleUnset)
	r.Get("/system_alerts", h.HandleList)
}

// pushBody is the JSON body for POST /system_alerts/push.
type pushBody struct {
	Key       string         `json:"key"`
	Device    string         `json:"device"`
	Level     Level          `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
	TTLSecond int            `json:"ttl_seconds,omitempty"`
}

// setBody is the JSON body for POST /system_alerts/set/{key}/{device}.
//
// {key, device} arrive in the URL; the body carries level/message/fields.
type setBody struct {
	Level   Level          `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// HandlePush handles POST /api/v1/system_alerts/push.
func (h *Handler) HandlePush(w http.ResponseWriter, r *http.Request) {
	var body pushBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	args := PushArgs{
		Key:     body.Key,
		Device:  body.Device,
		Level:   body.Level,
		Message: body.Message,
		Fields:  body.Fields,
	}
	if body.TTLSecond > 0 {
		args.TTL = time.Duration(body.TTLSecond) * time.Second
	}
	a, err := h.Store.Push(r.Context(), args)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// HandleSet handles POST /api/v1/system_alerts/set/{key}/{device}.
func (h *Handler) HandleSet(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	device := normaliseDeviceParam(chi.URLParam(r, "device"))

	var body setBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	a, err := h.Store.Set(r.Context(), SetArgs{
		Key:     key,
		Device:  device,
		Level:   body.Level,
		Message: body.Message,
		Fields:  body.Fields,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// HandleUnset handles POST /api/v1/system_alerts/unset/{key}/{device}.
func (h *Handler) HandleUnset(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	device := normaliseDeviceParam(chi.URLParam(r, "device"))

	cleared, err := h.Store.Unset(r.Context(), key, device)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
}

// HandleList handles GET /api/v1/system_alerts.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	out, err := h.Store.List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts": out,
		"count":  len(out),
	})
}

// normaliseDeviceParam translates the URL "-" sentinel back to "" so callers
// can issue a no-device unset via /system_alerts/unset/raid_degraded/-
// without ambiguity.  Chi requires every URL segment to be non-empty.
//
// Codex post-ship review issue #9: chi does NOT decode percent-encoded
// slashes inside path segments — devices like "ctrl0/vd1" arrive
// percent-encoded ("ctrl0%2Fvd1") and would be persisted with the
// %2F intact, mismatching the operator's text in subsequent lookups.
// We url.PathUnescape on the device segment before persistence and
// comparison.  Errors from PathUnescape (malformed %xx) leave the
// segment as-is — the validation downstream will catch it.
func normaliseDeviceParam(s string) string {
	if s == "-" {
		return ""
	}
	if dec, err := url.PathUnescape(s); err == nil {
		return dec
	}
	return s
}

// writeJSON encodes v with the given status.  Local copy keeps this package
// independent of internal/server/handlers (avoids an import cycle).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn().Err(err).Msg("system_alerts: encode response failed")
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps store-level errors to HTTP status codes.
//
// Codex post-ship review issue #8: the previous implementation matched
// any error with the literal "system_alerts:" prefix as a validation
// error.  Every fmt.Errorf wrap inside the store carried that prefix
// too — store/DB failures turned into HTTP 400.  We now use errors.Is
// against the typed ErrValidation sentinel, so only validation errors
// surface as 400; everything else is logged and returned 500.
func writeStoreError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrValidation) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Error().Err(err).Msg("system_alerts: store error")
	writeJSONError(w, http.StatusInternalServerError, "internal error")
}
