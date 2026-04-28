// auto_policy.go — HTTP handlers for the auto-compute allocation policy engine.
// Sprint H, v1.7.0 / CF-29.
//
// Routes:
//   POST /api/v1/projects            — create project with optional auto_compute=true (H1)
//   GET  /api/v1/admin/auto-policy   — read policy config (admin)
//   PUT  /api/v1/admin/auto-policy   — update policy config (admin)
//   POST /api/v1/node-groups/:id/undo-auto-policy — reverse auto-provisioning (H3)
//   GET  /api/v1/node-groups/:id/auto-policy-state — read undo window state (H3 banner)
package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/allocation"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/internal/notifications" // Notifier type
)

// AutoPolicyHandler handles the auto-compute allocation policy API.
type AutoPolicyHandler struct {
	DB     *db.DB
	Audit  *db.AuditService
	Engine *allocation.Engine

	// Notifier dispatches email notifications. May be nil.
	Notifier *notifications.Notifier

	// GetActorInfo returns (actorID, actorLabel) from the request context.
	GetActorInfo func(r *http.Request) (id, label string)
}

// ─── POST /api/v1/projects ────────────────────────────────────────────────────

// ProjectCreateRequest is the body for POST /api/v1/projects.
type ProjectCreateRequest struct {
	// ProjectName is the human-readable name of the project (required).
	ProjectName string `json:"project_name"`

	// AutoCompute, when true, triggers the H1 engine.
	AutoCompute bool `json:"auto_compute"`

	// The fields below are only used when auto_compute=true.

	// NodeGroupTemplate overrides the policy partition template for this run.
	NodeGroupTemplate string `json:"nodegroup_template,omitempty"`

	// InitialMembers is a list of LDAP usernames to pre-populate.
	InitialMembers []string `json:"initial_members,omitempty"`

	// SlurmPartition is the Slurm partition to assign.
	SlurmPartition string `json:"slurm_partition,omitempty"`

	// LDAPSyncEnabled (default true when LDAP module is enabled).
	LDAPSyncEnabled *bool `json:"ldap_sync_enabled,omitempty"`

	// PIUserID is the user who will own the project.
	// When empty the engine uses the authenticated PI's user ID from context.
	PIUserID string `json:"pi_user_id,omitempty"`
}

// ProjectCreateResponse is returned from POST /api/v1/projects.
type ProjectCreateResponse struct {
	// AutoAllocResult is populated when auto_compute=true. Nil otherwise.
	AutoAllocResult *allocation.Result `json:"auto_alloc_result,omitempty"`

	// Message is a human-readable summary.
	Message string `json:"message"`

	// AutoCompute echoes whether the engine ran.
	AutoCompute bool `json:"auto_compute"`
}

// HandleCreateProject handles POST /api/v1/projects.
// When auto_compute=true, runs the auto-policy engine synchronously and returns
// the created NodeGroup IDs in the response. If the engine fails, returns an
// error with the offending step in the error message (no partial state).
func (h *AutoPolicyHandler) HandleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req ProjectCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ProjectName == "" {
		writeError(w, "project_name is required", http.StatusBadRequest)
		return
	}

	actorID, actorLabel := "", "unknown"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}

	// Resolve PI user ID: caller-provided or the actor themselves.
	piUserID := req.PIUserID
	if piUserID == "" {
		piUserID = actorID
	}

	if !req.AutoCompute {
		// No auto-policy: just acknowledge the project creation request.
		// In a future sprint this would also create a DB project record.
		// For v1.7.0 the "project" abstraction is the NodeGroup; this endpoint
		// is the entry point for the wizard.
		writeJSON(w, http.StatusCreated, ProjectCreateResponse{
			AutoCompute: false,
			Message:     "project created (auto-compute disabled)",
		})
		return
	}

	// Check engine is available.
	if h.Engine == nil {
		writeError(w, "auto-compute policy engine not initialized", http.StatusServiceUnavailable)
		return
	}

	// Load policy config to check whether enabled globally.
	cfg, err := h.DB.GetAutoPolicyConfig(ctx)
	if err != nil {
		log.Error().Err(err).Msg("auto-policy: load config failed")
		writeError(w, "failed to load auto-policy config", http.StatusInternalServerError)
		return
	}

	// If explicitly requested via body flag, override the global enabled check.
	// (A PI explicitly requesting auto_compute=true gets it even if the global
	// default is off — the wizard always sends the flag explicitly.)

	// Resolve LDAP sync flag.
	ldapSync := true // default on
	if req.LDAPSyncEnabled != nil {
		ldapSync = *req.LDAPSyncEnabled
	}
	_ = cfg // used for logging below

	engineReq := allocation.Request{
		ProjectName:       req.ProjectName,
		PIUserID:          piUserID,
		PIUsername:        actorLabel,
		PartitionTemplate: req.NodeGroupTemplate,
		InitialMembers:    req.InitialMembers,
		LDAPSyncEnabled:   ldapSync,
		SelectedPartition: req.SlurmPartition,
	}

	result, err := h.Engine.Run(ctx, engineReq, actorID, actorLabel)
	if err != nil {
		log.Error().Err(err).
			Str("project_name", req.ProjectName).
			Str("pi_user_id", piUserID).
			Msg("auto-policy: engine run failed")
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify admins if configured.
	if h.Notifier != nil && cfg.NotifyAdminsOnCreate {
		go func() {
			adminEmails, emailErr := h.DB.GetAdminEmails(ctx)
			if emailErr != nil || len(adminEmails) == 0 {
				return
			}
			subject := "clustr: auto-allocation created — " + result.NodeGroupName
			body := "A new auto-compute allocation was created.\n\n" +
				"Project: " + req.ProjectName + "\n" +
				"NodeGroup: " + result.NodeGroupName + " (ID: " + result.NodeGroupID + ")\n" +
				"PI: " + piUserID + "\n" +
				"Slurm partition: " + result.SlurmPartitionName + "\n" +
				"Undo deadline: " + result.UndoDeadline.Format(time.RFC3339) + "\n"
			if sendErr := h.Notifier.Mailer.Send(ctx, adminEmails, subject, body); sendErr != nil {
				log.Warn().Err(sendErr).Msg("auto-policy: admin notify send failed")
			}
		}()
	}

	// Mark PI's onboarding wizard as completed.
	if piUserID != "" {
		if err := h.DB.MarkOnboardingCompleted(ctx, piUserID); err != nil {
			log.Warn().Err(err).Str("pi_user_id", piUserID).
				Msg("auto-policy: mark onboarding completed failed (non-fatal)")
		}
	}

	writeJSON(w, http.StatusCreated, ProjectCreateResponse{
		AutoCompute:     true,
		AutoAllocResult: result,
		Message:         "project created with auto-compute allocation",
	})
}

// ─── GET/PUT /api/v1/admin/auto-policy ───────────────────────────────────────

// HandleGetAutoPolicyConfig handles GET /api/v1/admin/auto-policy (admin only).
func (h *AutoPolicyHandler) HandleGetAutoPolicyConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.DB.GetAutoPolicyConfig(r.Context())
	if err != nil {
		writeError(w, "failed to load auto-policy config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":                   cfg.Enabled,
		"default_node_count":        cfg.DefaultNodeCount,
		"default_hardware_profile":  cfg.DefaultHardwareProfile,
		"default_partition_template": cfg.DefaultPartitionTemplate,
		"default_role":              cfg.DefaultRole,
		"notify_admins_on_create":   cfg.NotifyAdminsOnCreate,
		"updated_at":                cfg.UpdatedAt.Format(time.RFC3339),
	})
}

// HandleUpdateAutoPolicyConfig handles PUT /api/v1/admin/auto-policy (admin only).
func (h *AutoPolicyHandler) HandleUpdateAutoPolicyConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled                  *bool   `json:"enabled"`
		DefaultNodeCount         *int    `json:"default_node_count"`
		DefaultHardwareProfile   *string `json:"default_hardware_profile"`
		DefaultPartitionTemplate *string `json:"default_partition_template"`
		DefaultRole              *string `json:"default_role"`
		NotifyAdminsOnCreate     *bool   `json:"notify_admins_on_create"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	existing, err := h.DB.GetAutoPolicyConfig(ctx)
	if err != nil {
		writeError(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	// Apply partial updates (only non-nil fields).
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	if body.DefaultNodeCount != nil {
		existing.DefaultNodeCount = *body.DefaultNodeCount
	}
	if body.DefaultHardwareProfile != nil {
		existing.DefaultHardwareProfile = *body.DefaultHardwareProfile
	}
	if body.DefaultPartitionTemplate != nil {
		existing.DefaultPartitionTemplate = *body.DefaultPartitionTemplate
	}
	if body.DefaultRole != nil {
		existing.DefaultRole = *body.DefaultRole
	}
	if body.NotifyAdminsOnCreate != nil {
		existing.NotifyAdminsOnCreate = *body.NotifyAdminsOnCreate
	}

	if err := h.DB.UpdateAutoPolicyConfig(ctx, *existing); err != nil {
		writeError(w, "failed to update config", http.StatusInternalServerError)
		return
	}

	actorID, actorLabel := "", "admin"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	if h.Audit != nil {
		h.Audit.Record(ctx, actorID, actorLabel,
			"auto_policy.config.updated", "auto_policy_config", "default", r.RemoteAddr,
			nil, map[string]string{"enabled": boolStr(existing.Enabled)},
		)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── H3 — Undo window ─────────────────────────────────────────────────────────

// HandleUndoAutoPolicy handles POST /api/v1/node-groups/:id/undo-auto-policy (H3).
// Reverses everything the engine created within the 24-hour window.
// Admin or the PI owner can call this.
func (h *AutoPolicyHandler) HandleUndoAutoPolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	actorID, actorLabel := "", "unknown"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}

	if h.Engine == nil {
		writeError(w, "auto-compute policy engine not initialized", http.StatusServiceUnavailable)
		return
	}

	// Read state before undo so we can notify the PI afterward (group will be deleted).
	var undoStateView *allocation.StateView
	if h.Notifier != nil {
		stateJSON, finalizedAt, stErr := h.DB.GetAutoComputeState(ctx, groupID)
		if stErr == nil && stateJSON != "" {
			if sv, svErr := allocation.ParseStateView(stateJSON, finalizedAt); svErr == nil {
				undoStateView = sv
			}
		}
	}

	if err := h.Engine.Undo(ctx, groupID, actorID, actorLabel); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("auto-policy undo: failed")
		// Distinguish window-closed from other errors.
		if isWindowClosed(err) {
			writeError(w, err.Error(), http.StatusConflict)
			return
		}
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notification: email the PI with a summary of what was reverted.
	if h.Notifier != nil && undoStateView != nil && undoStateView.PIUserID != "" {
		sv := undoStateView
		go func() {
			subject := "clustr: auto-allocation undone — " + sv.NodeGroupName
			body := "Your auto-compute allocation has been reverted.\n\n" +
				"NodeGroup " + sv.NodeGroupName + " has been deleted.\n" +
				"Slurm partition '" + sv.SlurmPartitionName + "' entries were marked for removal.\n" +
				"If you need a new allocation, please contact your system administrator.\n"
			if sendErr := h.Notifier.Mailer.Send(ctx, []string{sv.PIUserID}, subject, body); sendErr != nil {
				log.Warn().Err(sendErr).Msg("auto-policy undo: PI notify send failed")
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"group_id": groupID,
		"message":  "auto-allocation reversed; NodeGroup deleted and policy state cleared",
	})
}

// HandleGetAutoPolicyState handles GET /api/v1/node-groups/:id/auto-policy-state (H3 banner).
// Returns the undo window state for the PI portal banner.
func (h *AutoPolicyHandler) HandleGetAutoPolicyState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groupID := chi.URLParam(r, "id")

	stateJSON, finalizedAt, err := h.DB.GetAutoComputeState(ctx, groupID)
	if err != nil {
		writeError(w, "group not found or error loading state", http.StatusNotFound)
		return
	}
	if stateJSON == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"auto_compute":  false,
			"undo_available": false,
		})
		return
	}

	sv, err := allocation.ParseStateView(stateJSON, finalizedAt)
	if err != nil {
		writeError(w, "failed to parse policy state", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"auto_compute":         true,
		"undo_available":       sv.UndoAvailable,
		"hours_remaining":      sv.HoursRemaining,
		"undo_deadline":        sv.UndoDeadline.Format(time.RFC3339),
		"node_group_id":        sv.NodeGroupID,
		"node_group_name":      sv.NodeGroupName,
		"slurm_partition_name": sv.SlurmPartitionName,
		"ldap_group_dn":        sv.LDAPGroupDN,
		"created_at":           sv.CreatedAt.Format(time.RFC3339),
	})
}

// ─── onboarding wizard status ─────────────────────────────────────────────────

// HandleGetOnboardingStatus handles GET /api/v1/portal/pi/onboarding-status.
// Returns whether the PI has completed the first-project wizard (H2).
func (h *AutoPolicyHandler) HandleGetOnboardingStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := piUserIDFromContext(ctx)
	if userID == "" {
		writeError(w, "cannot determine PI identity", http.StatusUnprocessableEntity)
		return
	}

	completed, err := h.DB.IsOnboardingCompleted(ctx, userID)
	if err != nil {
		writeError(w, "failed to check onboarding status", http.StatusInternalServerError)
		return
	}

	// Count how many NodeGroups the PI owns.
	groups, err := h.DB.ListNodeGroupsByPI(ctx, userID)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID).Msg("onboarding status: list groups failed (non-fatal)")
		groups = nil
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"onboarding_completed": completed,
		"has_projects":         len(groups) > 0,
		"project_count":        len(groups),
		// show_wizard is true when PI has never completed wizard AND has no projects.
		"show_wizard": !completed && len(groups) == 0,
	})
}

// HandleCompleteOnboarding handles POST /api/v1/portal/pi/onboarding-complete.
// Marks the wizard as dismissed (skip path — no project created).
func (h *AutoPolicyHandler) HandleCompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := piUserIDFromContext(ctx)
	if userID == "" {
		writeError(w, "cannot determine PI identity", http.StatusUnprocessableEntity)
		return
	}

	if err := h.DB.MarkOnboardingCompleted(ctx, userID); err != nil {
		writeError(w, "failed to update onboarding status", http.StatusInternalServerError)
		return
	}

	actorID, actorLabel := userID, "pi"
	if h.GetActorInfo != nil {
		actorID, actorLabel = h.GetActorInfo(r)
	}
	if h.Audit != nil {
		h.Audit.Record(ctx, actorID, actorLabel,
			"pi.onboarding.dismissed", "user", userID, r.RemoteAddr,
			nil, nil,
		)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func isWindowClosed(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg, "window closed", "window expired", "undo window")
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
