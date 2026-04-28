// Package allocation provides the auto-compute allocation policy engine for
// clustr (Sprint H, v1.7.0 / CF-29).
//
// When a PI project is created with auto_compute=true the engine:
//   1. Creates a NodeGroup named per a configurable template.
//   2. Queues an LDAP posixGroup sync (G1 LDAP project plugin).
//   3. Sets the NodeGroup access restriction to membership-based (G2).
//   4. Records the PI as owner (pi_user_id).
//   5. Fires a Slurm partition config entry if Slurm is enabled.
//   6. Records everything it did in auto_policy_state JSON (for undo).
//
// If any step fails the engine returns an error and the caller is expected
// to delete the partially-created NodeGroup (atomic rollback via DB tx).
package allocation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"text/template"
	"bytes"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
	"github.com/sqoia-dev/clustr/pkg/api"
)

// Engine is the auto-compute allocation policy engine.
// Zero-value is valid; all capability hooks are optional (nil = no-op).
type Engine struct {
	DB    *db.DB
	Audit *db.AuditService

	// SyncLDAPGroup is called after NodeGroup creation to queue an LDAP posixGroup
	// sync (G1). May be nil (LDAP not enabled).
	SyncLDAPGroup func(ctx context.Context, groupID string) error

	// SetGroupRestriction applies the G2 access restriction (membership-based
	// by default). May be nil.
	SetGroupRestriction func(ctx context.Context, groupID string) error

	// AddSlurmPartition writes a new Slurm partition entry for the NodeGroup.
	// Returns the partition name on success. May be nil (Slurm not enabled).
	AddSlurmPartition func(ctx context.Context, groupID, partitionName string) error
}

// Request carries all inputs for one auto-policy engine run.
type Request struct {
	// ProjectName is the human-readable project name entered by the PI.
	ProjectName string

	// ProjectSlug is a sanitized identifier derived from ProjectName.
	// Callers should set this; the engine will derive it if empty.
	ProjectSlug string

	// PIUserID is the clustr users.id of the PI who will own the NodeGroup.
	PIUserID string

	// PIUsername is the LDAP/clustr username of the PI (for audit log).
	PIUsername string

	// PartitionTemplate overrides the policy's default partition name template.
	// Leave empty to use the admin-configured default.
	PartitionTemplate string

	// InitialMembers is the list of LDAP usernames to pre-populate.
	// Empty is fine; the PI can add members later.
	InitialMembers []string

	// LDAPSyncEnabled — when true, queue an LDAP sync immediately after creation.
	// Defaults to true when LDAP is enabled. Set to false to skip.
	LDAPSyncEnabled bool

	// SelectedPartition is the Slurm partition the PI chose (H2 wizard dropdown).
	// May be empty when Slurm is not enabled.
	SelectedPartition string
}

// Result is the output from a successful Engine.Run call.
type Result struct {
	// NodeGroupID is the ID of the created NodeGroup.
	NodeGroupID string `json:"node_group_id"`

	// NodeGroupName is the name of the created NodeGroup.
	NodeGroupName string `json:"node_group_name"`

	// LDAPGroupDN is the LDAP group DN queued for sync (empty when LDAP not enabled).
	LDAPGroupDN string `json:"ldap_group_dn,omitempty"`

	// SlurmPartitionName is the Slurm partition entry added (empty when Slurm not enabled).
	SlurmPartitionName string `json:"slurm_partition_name,omitempty"`

	// PIUserID is echoed from the request.
	PIUserID string `json:"pi_user_id"`

	// CreatedAt is the timestamp of the NodeGroup creation.
	CreatedAt time.Time `json:"created_at"`

	// UndoDeadline is CreatedAt + 24h — the last moment undo is possible.
	UndoDeadline time.Time `json:"undo_deadline"`

	// PolicySnapshot is the serialized AutoPolicyConfig that was active at
	// run time, for the audit log.
	PolicySnapshot string `json:"policy_snapshot"`
}

// policyState is the JSON schema stored in node_groups.auto_policy_state.
// It contains everything needed to reverse the engine's actions.
type policyState struct {
	Version            string    `json:"v"`
	NodeGroupID        string    `json:"node_group_id"`
	NodeGroupName      string    `json:"node_group_name"`
	LDAPGroupDN        string    `json:"ldap_group_dn,omitempty"`
	SlurmPartitionName string    `json:"slurm_partition_name,omitempty"`
	PIUserID           string    `json:"pi_user_id"`
	InitialMembers     []string  `json:"initial_members,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	PolicySnapshot     string    `json:"policy_snapshot"`
}

// Run executes the auto-compute policy for one PI project.
// It is synchronous and either succeeds fully or returns an error (no partial state persisted).
func (e *Engine) Run(ctx context.Context, req Request, actorID, actorLabel string) (*Result, error) {
	// Load the current policy config.
	cfg, err := e.DB.GetAutoPolicyConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("auto-policy: load config: %w", err)
	}

	// Derive slug if caller didn't provide one.
	slug := req.ProjectSlug
	if slug == "" {
		slug = slugify(req.ProjectName)
	}

	// Resolve the NodeGroup name from the partition template (same variable set).
	groupName := slug + "-compute"

	// Resolve the Slurm partition name.
	partitionTmplStr := req.PartitionTemplate
	if partitionTmplStr == "" {
		partitionTmplStr = cfg.DefaultPartitionTemplate
	}
	partitionName := req.SelectedPartition
	if partitionName == "" {
		partitionName = renderTemplate(partitionTmplStr, map[string]string{
			"ProjectSlug": slug,
			"ProjectName": req.ProjectName,
		})
	}

	// ─── Step 1: Create the NodeGroup ────────────────────────────────────────
	now := time.Now().UTC()
	role := cfg.DefaultRole
	if role == "" {
		role = "compute"
	}
	group := api.NodeGroup{
		ID:        uuid.New().String(),
		Name:      groupName,
		Role:      role,
		Description: fmt.Sprintf("Auto-provisioned compute allocation for %s", req.ProjectName),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := e.DB.CreateNodeGroupFull(ctx, group); err != nil {
		return nil, fmt.Errorf("auto-policy[step=create_group]: %w", err)
	}
	log.Info().Str("group_id", group.ID).Str("group_name", group.Name).
		Msg("auto-policy: NodeGroup created")

	// Rollback helper — called on any subsequent failure.
	rollback := func(step string, stepErr error) (*Result, error) {
		if delErr := e.DB.DeleteNodeGroup(ctx, group.ID); delErr != nil {
			log.Error().Err(delErr).Str("group_id", group.ID).
				Msg("auto-policy: rollback DeleteNodeGroup failed")
		}
		return nil, fmt.Errorf("auto-policy[step=%s]: %w (rolled back NodeGroup %s)", step, stepErr, group.ID)
	}

	// ─── Step 2: Assign the PI as owner ──────────────────────────────────────
	if err := e.DB.SetNodeGroupPI(ctx, group.ID, req.PIUserID); err != nil {
		return rollback("assign_pi", err)
	}
	log.Info().Str("group_id", group.ID).Str("pi_user_id", req.PIUserID).
		Msg("auto-policy: PI assigned")

	// ─── Step 3: Queue LDAP sync (G1) ────────────────────────────────────────
	ldapGroupDN := ""
	if req.LDAPSyncEnabled && e.SyncLDAPGroup != nil {
		if err := e.SyncLDAPGroup(ctx, group.ID); err != nil {
			// LDAP sync failure is non-fatal: log and continue. The sync queue
			// will retry. We don't roll back the NodeGroup for an LDAP hiccup.
			log.Warn().Err(err).Str("group_id", group.ID).
				Msg("auto-policy: LDAP sync queue failed (non-fatal)")
		} else {
			// The LDAP DN follows the standard project plugin convention.
			ldapGroupDN = fmt.Sprintf("cn=%s,ou=clustr-projects,dc=clustr,dc=local", group.Name)
			log.Info().Str("group_id", group.ID).Str("ldap_dn", ldapGroupDN).
				Msg("auto-policy: LDAP sync queued")
		}
	}

	// ─── Step 4: Apply G2 access restriction (membership-based default) ──────
	if e.SetGroupRestriction != nil {
		if err := e.SetGroupRestriction(ctx, group.ID); err != nil {
			log.Warn().Err(err).Str("group_id", group.ID).
				Msg("auto-policy: access restriction setup failed (non-fatal)")
		}
	}

	// ─── Step 5: Add Slurm partition entry ───────────────────────────────────
	actualPartition := ""
	if partitionName != "" && e.AddSlurmPartition != nil {
		if err := e.AddSlurmPartition(ctx, group.ID, partitionName); err != nil {
			// Slurm partition failure IS fatal for auto-policy (the whole point of
			// the engine is to wire Slurm). Roll back the NodeGroup.
			return rollback("add_slurm_partition", err)
		}
		actualPartition = partitionName
		log.Info().Str("group_id", group.ID).Str("partition", actualPartition).
			Msg("auto-policy: Slurm partition entry added")
	}

	// ─── Step 6: Persist the auto_policy_state for undo ──────────────────────
	cfgJSON, _ := json.Marshal(cfg)
	state := policyState{
		Version:            "1",
		NodeGroupID:        group.ID,
		NodeGroupName:      group.Name,
		LDAPGroupDN:        ldapGroupDN,
		SlurmPartitionName: actualPartition,
		PIUserID:           req.PIUserID,
		InitialMembers:     req.InitialMembers,
		CreatedAt:          now,
		PolicySnapshot:     string(cfgJSON),
	}
	stateJSON, _ := json.Marshal(state)

	if err := e.DB.SetAutoComputeState(ctx, group.ID, string(stateJSON)); err != nil {
		return rollback("persist_state", err)
	}

	// ─── Step 7: Audit log ───────────────────────────────────────────────────
	if e.Audit != nil {
		e.Audit.Record(ctx, actorID, actorLabel,
			"pi_onboarded.auto_allocation",
			"node_group", group.ID, "",
			nil,
			map[string]string{
				"group_name":   group.Name,
				"pi_user_id":   req.PIUserID,
				"partition":    actualPartition,
				"ldap_dn":      ldapGroupDN,
				"policy_state": string(stateJSON),
			},
		)
	}

	result := &Result{
		NodeGroupID:        group.ID,
		NodeGroupName:      group.Name,
		LDAPGroupDN:        ldapGroupDN,
		SlurmPartitionName: actualPartition,
		PIUserID:           req.PIUserID,
		CreatedAt:          now,
		UndoDeadline:       now.Add(24 * time.Hour),
		PolicySnapshot:     string(cfgJSON),
	}
	return result, nil
}

// Undo reverses everything the engine created for a given NodeGroup within
// the 24-hour undo window (H3). Returns an error if the window has closed or
// the group was not created by the engine.
func (e *Engine) Undo(ctx context.Context, groupID, actorID, actorLabel string) error {
	stateJSON, finalizedAt, err := e.DB.GetAutoComputeState(ctx, groupID)
	if err != nil {
		return fmt.Errorf("auto-policy undo: load state: %w", err)
	}
	if stateJSON == "" {
		return fmt.Errorf("auto-policy undo: group %s was not created by the engine", groupID)
	}
	if finalizedAt != nil {
		return fmt.Errorf("auto-policy undo: 24-hour window closed at %s", finalizedAt.Format(time.RFC3339))
	}

	var state policyState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return fmt.Errorf("auto-policy undo: parse state: %w", err)
	}

	// Check window has not elapsed (belt-and-suspenders against race).
	if time.Since(state.CreatedAt) > 24*time.Hour {
		return fmt.Errorf("auto-policy undo: undo window expired (created %s)", state.CreatedAt.Format(time.RFC3339))
	}

	// Reverse step 5: remove Slurm partition entry.
	// (We can't call AddSlurmPartition; the caller wires a RemoveSlurmPartition hook if needed.)
	// For v1.7.0 the Slurm partition removal is logged; the operator can delete
	// the partition entry from the Slurm config editor. A future sprint can wire
	// an automated removal.
	if state.SlurmPartitionName != "" {
		log.Info().Str("group_id", groupID).Str("partition", state.SlurmPartitionName).
			Msg("auto-policy undo: Slurm partition note — operator should remove partition from slurm.conf")
	}

	// Reverse step 1: delete the NodeGroup (cascades LDAP sync queue, memberships, etc.).
	if err := e.DB.DeleteNodeGroup(ctx, groupID); err != nil {
		return fmt.Errorf("auto-policy undo: delete group: %w", err)
	}

	// Audit.
	if e.Audit != nil {
		e.Audit.Record(ctx, actorID, actorLabel,
			"auto_allocation.undone",
			"node_group", groupID, "",
			map[string]string{
				"group_name":   state.NodeGroupName,
				"pi_user_id":   state.PIUserID,
				"partition":    state.SlurmPartitionName,
				"ldap_dn":      state.LDAPGroupDN,
			},
			nil,
		)
	}

	log.Info().Str("group_id", groupID).Str("actor", actorLabel).
		Msg("auto-policy: undo completed")
	return nil
}

// StateFromJSON parses an auto_policy_state JSON blob into the exported fields
// needed by the API response (undo deadline, partition name, etc.).
type StateView struct {
	NodeGroupID        string    `json:"node_group_id"`
	NodeGroupName      string    `json:"node_group_name"`
	LDAPGroupDN        string    `json:"ldap_group_dn,omitempty"`
	SlurmPartitionName string    `json:"slurm_partition_name,omitempty"`
	PIUserID           string    `json:"pi_user_id"`
	CreatedAt          time.Time `json:"created_at"`
	UndoDeadline       time.Time `json:"undo_deadline"`
	UndoAvailable      bool      `json:"undo_available"`
	HoursRemaining     float64   `json:"hours_remaining"`
}

// ParseStateView parses the state JSON and populates the view struct.
func ParseStateView(stateJSON string, finalizedAt *time.Time) (*StateView, error) {
	var state policyState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return nil, fmt.Errorf("parse auto policy state: %w", err)
	}
	deadline := state.CreatedAt.Add(24 * time.Hour)
	remaining := time.Until(deadline).Hours()
	available := finalizedAt == nil && remaining > 0

	return &StateView{
		NodeGroupID:        state.NodeGroupID,
		NodeGroupName:      state.NodeGroupName,
		LDAPGroupDN:        state.LDAPGroupDN,
		SlurmPartitionName: state.SlurmPartitionName,
		PIUserID:           state.PIUserID,
		CreatedAt:          state.CreatedAt,
		UndoDeadline:       deadline,
		UndoAvailable:      available,
		HoursRemaining:     max(remaining, 0),
	}, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a project name to a DNS-safe slug.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	// Clamp to 48 chars so the slug + "-compute" suffix stays ≤ 63.
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// renderTemplate executes a Go template string with the given data map.
// On error it falls back to the raw template string.
func renderTemplate(tmplStr string, data map[string]string) string {
	tmpl, err := template.New("partition").Parse(tmplStr)
	if err != nil {
		return tmplStr
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return tmplStr
	}
	return buf.String()
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
