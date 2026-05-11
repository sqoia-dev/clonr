package handlers

// dangerous_push.go — Sprint 41 Day 3
//
// Implements the typed-confirm-string dangerous-push gate.
//
// Two endpoints (both gated behind CLUSTR_DANGEROUS_GATE_ENABLED=1):
//
//   POST /api/v1/config/dangerous-push
//     Stage a push for a plugin that declares Dangerous=true. Returns a
//     pending_id and the confirm prompt. Requires config.dangerous_push permission.
//
//   POST /api/v1/config/dangerous-push/{pending_id}/confirm
//     Confirm a staged push by submitting the typed cluster-name string. On
//     match, fires the actual config_push to the node via the clientd hub.
//     Requires config.dangerous_push permission.
//
// When the gate flag is set, the regular config-push endpoint (ClientdHandler.ConfigPush)
// rejects requests that target a Dangerous plugin with 409 Conflict.
//
// Design: docs/design/sprint-41-auth-safety.md §4.2 and §3.6.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/auth"
	"github.com/sqoia-dev/clustr/internal/clientd"
	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// maxConfirmAttempts is the number of wrong typed-confirm strings allowed before
// the pending push is locked out (consumed). Prevents brute-force enumeration.
const maxConfirmAttempts = 3

// pendingStageTTL is how long a staged dangerous push waits for operator confirmation.
// After this window the push expires and the operator must re-trigger by re-saving
// the config (which re-fires the observer).
const pendingStageTTL = 10 * time.Minute

// DangerousPushDBIface is the database subset required by DangerousPushHandler.
type DangerousPushDBIface interface {
	InsertPendingDangerousPush(ctx context.Context, p db.PendingDangerousPush) error
	GetPendingDangerousPush(ctx context.Context, id string) (*db.PendingDangerousPush, error)
	IncrementDangerousPushAttempts(ctx context.Context, id string, maxAttempts int) (int, error)
	ConsumePendingDangerousPush(ctx context.Context, id string) error
}

// DangerousPushHandler handles the two staged-confirmation endpoints.
type DangerousPushHandler struct {
	// DB provides access to the pending_dangerous_pushes table.
	DB DangerousPushDBIface
	// Hub is used to deliver the config_push WS frame on confirmed confirmation.
	Hub ClientdHubIface
	// Audit records stage and confirm events.
	Audit *db.AuditService
	// GetActorInfo extracts (actorID, actorLabel) from a request context.
	GetActorInfo func(r *http.Request) (string, string)
	// ClusterName is the string the operator must type verbatim. Sourced from
	// CLUSTR_CLUSTER_NAME (via ServerConfig.ClusterName). Default: "clustr".
	ClusterName string
	// PluginMetadata returns the PluginMetadata for a registered plugin by name.
	// Wired to config.PluginMetadataByName in server.go.
	PluginMetadata func(pluginName string) (config.PluginMetadata, bool)
	// RenderPlugin renders the named plugin for the given cluster state and returns
	// the first instruction plus its rendered hash. Wired in server.go.
	RenderPlugin func(ctx context.Context, pluginName string, nodeID string) (api.InstallInstruction, string, error)
}

// ──────────────────────────────────────────────────────────────────────────────
// Stage endpoint — POST /api/v1/config/dangerous-push
// ──────────────────────────────────────────────────────────────────────────────

// dangerousPushStageRequest is the JSON body for the stage endpoint.
type dangerousPushStageRequest struct {
	NodeID     string          `json:"node_id"`
	PluginName string          `json:"plugin_name"`
	Payload    json.RawMessage `json:"payload,omitempty"` // reserved for future use
}

// dangerousPushStageResponse is the JSON body returned by the stage endpoint.
type dangerousPushStageResponse struct {
	PendingID     string `json:"pending_id"`
	DangerReason  string `json:"danger_reason"`
	ConfirmPrompt string `json:"confirm_prompt"`
	ExpiresAt     string `json:"expires_at"` // RFC3339
}

// HandleStage creates a pending_dangerous_pushes row and returns the confirm prompt.
// Route: POST /api/v1/config/dangerous-push
func (h *DangerousPushHandler) HandleStage(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req dangerousPushStageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.NodeID == "" {
		writeValidationError(w, "node_id is required")
		return
	}
	if req.PluginName == "" {
		writeValidationError(w, "plugin_name is required")
		return
	}

	// Confirm the plugin exists and is actually dangerous.
	meta, ok := h.PluginMetadata(req.PluginName)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{
			Error: fmt.Sprintf("plugin %q is not registered", req.PluginName),
			Code:  "plugin_not_found",
		})
		return
	}
	if !meta.Dangerous {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Error: fmt.Sprintf("plugin %q is not dangerous; use the regular config push endpoint", req.PluginName),
			Code:  "plugin_not_dangerous",
		})
		return
	}

	// Render the plugin to produce the payload that will be sent on confirm.
	instr, renderedHash, err := h.RenderPlugin(r.Context(), req.PluginName, req.NodeID)
	if err != nil {
		log.Error().Err(err).Str("plugin", req.PluginName).Str("node_id", req.NodeID).
			Msg("dangerous push: render failed")
		writeJSON(w, http.StatusUnprocessableEntity, api.ErrorResponse{
			Error: "plugin render failed: " + err.Error(),
			Code:  "render_failed",
		})
		return
	}

	// Marshal the push payload that will be delivered when the operator confirms.
	sum := sha256.Sum256([]byte(instr.Payload))
	checksum := fmt.Sprintf("sha256:%x", sum)
	pushPayload := clientd.ConfigPushPayload{
		Target:       req.PluginName,
		Content:      instr.Payload,
		Checksum:     checksum,
		Plugin:       req.PluginName,
		RenderedHash: renderedHash,
		Priority:     config.EffectivePriority(meta),
	}
	payloadBytes, err := json.Marshal(pushPayload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to marshal push payload",
			Code:  "internal_error",
		})
		return
	}

	clusterName := h.ClusterName
	if clusterName == "" {
		clusterName = "clustr"
	}

	now := time.Now().UTC()
	pendingID := uuid.New().String()
	row := db.PendingDangerousPush{
		ID:           pendingID,
		NodeID:       req.NodeID,
		PluginName:   req.PluginName,
		RenderedHash: renderedHash,
		PayloadJSON:  string(payloadBytes),
		Reason:       meta.DangerReason,
		Challenge:    clusterName,
		ExpiresAt:    now.Add(pendingStageTTL),
		CreatedBy:    actorIDFromRequest(r, h.GetActorInfo),
		CreatedAt:    now,
	}

	if err := h.DB.InsertPendingDangerousPush(r.Context(), row); err != nil {
		log.Error().Err(err).Str("pending_id", pendingID).Msg("dangerous push: insert staged row failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to stage dangerous push",
			Code:  "internal_error",
		})
		return
	}

	// Audit the stage event.
	actorID, actorLabel := h.GetActorInfo(r)
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionConfigDangerousStaged,
			"config_push", pendingID,
			r.RemoteAddr,
			nil,
			map[string]string{
				"plugin":    req.PluginName,
				"reason":    meta.DangerReason,
				"challenge": clusterName,
				"node_id":   req.NodeID,
			},
		)
	}

	log.Info().
		Str("pending_id", pendingID).
		Str("plugin", req.PluginName).
		Str("node_id", req.NodeID).
		Str("actor", actorLabel).
		Msg("dangerous push: staged, awaiting operator confirmation")

	writeJSON(w, http.StatusAccepted, dangerousPushStageResponse{
		PendingID:     pendingID,
		DangerReason:  meta.DangerReason,
		ConfirmPrompt: fmt.Sprintf("Type the cluster name %q to confirm", clusterName),
		ExpiresAt:     row.ExpiresAt.Format(time.RFC3339),
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// Confirm endpoint — POST /api/v1/config/dangerous-push/{pending_id}/confirm
// ──────────────────────────────────────────────────────────────────────────────

// dangerousPushConfirmRequest is the JSON body for the confirm endpoint.
type dangerousPushConfirmRequest struct {
	ConfirmString string `json:"confirm_string"`
}

// dangerousPushConfirmResponse is returned on a successful confirmation.
type dangerousPushConfirmResponse struct {
	OK       bool   `json:"ok"`
	Plugin   string `json:"plugin"`
	NodeID   string `json:"node_id"`
	MsgID    string `json:"msg_id"`
}

// HandleConfirm validates the typed confirm string and, on match, fires the
// staged config_push to the node.
// Route: POST /api/v1/config/dangerous-push/{pending_id}/confirm
func (h *DangerousPushHandler) HandleConfirm(w http.ResponseWriter, r *http.Request) {
	pendingID := chi.URLParam(r, "pending_id")
	if pendingID == "" {
		writeValidationError(w, "missing pending_id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil {
		writeValidationError(w, "failed to read request body")
		return
	}

	var req dangerousPushConfirmRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeValidationError(w, "invalid JSON body")
		return
	}
	if req.ConfirmString == "" {
		writeValidationError(w, "confirm_string is required")
		return
	}

	// Load the staged push.
	staged, err := h.DB.GetPendingDangerousPush(r.Context(), pendingID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, api.ErrorResponse{
			Error: "pending push not found",
			Code:  "not_found",
		})
		return
	}
	if err != nil {
		log.Error().Err(err).Str("pending_id", pendingID).Msg("dangerous push confirm: DB lookup failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "internal server error",
			Code:  "internal_error",
		})
		return
	}

	// Already consumed (confirmed or locked out)?
	if staged.Consumed {
		writeJSON(w, http.StatusGone, api.ErrorResponse{
			Error: "this pending push has already been confirmed or locked out",
			Code:  "gone",
		})
		return
	}

	// Expired?
	if time.Now().UTC().After(staged.ExpiresAt) {
		writeJSON(w, http.StatusGone, api.ErrorResponse{
			Error: "this pending push has expired; re-save the config to generate a new confirmation request",
			Code:  "expired",
		})
		return
	}

	// Confirm-string mismatch.
	if req.ConfirmString != staged.Challenge {
		newCount, incErr := h.DB.IncrementDangerousPushAttempts(r.Context(), pendingID, maxConfirmAttempts)
		if incErr != nil {
			log.Error().Err(incErr).Str("pending_id", pendingID).
				Msg("dangerous push confirm: increment attempts failed")
		}

		attemptsLeft := maxConfirmAttempts - newCount
		if attemptsLeft <= 0 {
			log.Warn().Str("pending_id", pendingID).Str("plugin", staged.PluginName).
				Msg("dangerous push: 3-strike lockout reached, pending push consumed")
			writeJSON(w, http.StatusGone, api.ErrorResponse{
				Error: "too many failed attempts; this pending push has been locked out. Re-save the config to generate a new request.",
				Code:  "locked_out",
			})
			return
		}

		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{
			Error: fmt.Sprintf("confirmation string mismatch (%d attempt(s) remaining)", attemptsLeft),
			Code:  "confirm_mismatch",
		})
		return
	}

	// Match — fire the push.
	var pushPayload clientd.ConfigPushPayload
	if err := json.Unmarshal([]byte(staged.PayloadJSON), &pushPayload); err != nil {
		log.Error().Err(err).Str("pending_id", pendingID).
			Msg("dangerous push confirm: unmarshal staged payload failed")
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to unmarshal staged push payload",
			Code:  "internal_error",
		})
		return
	}

	msgID := uuid.New().String()
	payloadBytes, err := json.Marshal(pushPayload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{
			Error: "failed to marshal push payload for delivery",
			Code:  "internal_error",
		})
		return
	}

	msg := clientd.ServerMessage{
		Type:    "config_push",
		MsgID:   msgID,
		Payload: json.RawMessage(payloadBytes),
	}

	if err := h.Hub.Send(staged.NodeID, msg); err != nil {
		log.Warn().Err(err).
			Str("pending_id", pendingID).
			Str("node_id", staged.NodeID).
			Str("plugin", staged.PluginName).
			Msg("dangerous push confirm: WS send failed (node offline?); push not consumed")
		writeJSON(w, http.StatusBadGateway, api.ErrorResponse{
			Error: "node is not connected; config push could not be delivered",
			Code:  "node_offline",
		})
		return
	}

	// Mark consumed so the row cannot be re-confirmed.
	if err := h.DB.ConsumePendingDangerousPush(r.Context(), pendingID); err != nil {
		// Non-fatal: the push was already sent. Log and continue.
		log.Error().Err(err).Str("pending_id", pendingID).
			Msg("dangerous push confirm: consume row failed (push already delivered)")
	}

	// Audit the confirmed event.
	actorID, actorLabel := h.GetActorInfo(r)
	if h.Audit != nil {
		h.Audit.Record(r.Context(), actorID, actorLabel,
			db.AuditActionConfigDangerousConfirmed,
			"config_push", pendingID,
			r.RemoteAddr,
			map[string]string{
				"plugin":        staged.PluginName,
				"staged_reason": staged.Reason,
			},
			map[string]interface{}{
				"plugin":  staged.PluginName,
				"node_id": staged.NodeID,
				"applied": true,
				"msg_id":  msgID,
			},
		)
	}

	log.Info().
		Str("pending_id", pendingID).
		Str("plugin", staged.PluginName).
		Str("node_id", staged.NodeID).
		Str("msg_id", msgID).
		Str("actor", actorLabel).
		Msg("dangerous push: confirmed and delivered")

	writeJSON(w, http.StatusOK, dangerousPushConfirmResponse{
		OK:     true,
		Plugin: staged.PluginName,
		NodeID: staged.NodeID,
		MsgID:  msgID,
	})
}

// ──────────────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────────────

// actorIDFromRequest extracts the actor ID from the request context using
// the provided GetActorInfo function.
func actorIDFromRequest(r *http.Request, getActorInfo func(*http.Request) (string, string)) string {
	if getActorInfo == nil {
		return ""
	}
	id, _ := getActorInfo(r)
	return id
}

// IsDangerousPlugin returns true if the named plugin is registered and has
// Metadata().Dangerous == true. Used by the regular config-push handler to
// reject dangerous plugins when the gate is enabled.
func IsDangerousPlugin(pluginMetadata func(string) (config.PluginMetadata, bool), pluginName string) bool {
	if pluginMetadata == nil {
		return false
	}
	meta, ok := pluginMetadata(pluginName)
	if !ok {
		return false
	}
	return meta.Dangerous
}

// resolveActorInfo is the signature used by GetActorInfo in DangerousPushHandler.
// Redeclared here so tests can provide a stub without importing the server package.
type resolveActorInfo func(r *http.Request) (actorID, actorLabel string)

// ─── RBAC permission check helper ────────────────────────────────────────────

// RequireConfigDangerousPush returns the auth.VerbConfigDangerousPush string
// so callers can reference it without importing the auth package directly.
func RequireConfigDangerousPush() string {
	return auth.VerbConfigDangerousPush
}
