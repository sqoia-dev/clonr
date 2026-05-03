// project_plugin.go — OpenLDAP project plugin (G1, Sprint G / CF-24).
//
// When a NodeGroup is created, the sync worker auto-creates a posixGroup in
// LDAP (named per the configured template, default ou=clustr-projects,<base_dn>).
// Member add/remove operations are mirrored to the posixGroup memberUid list.
// LDAP failures never block the primary workflow: they are queued for retry.
package ldap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/sqoia-dev/clustr/internal/db"
)

// ProjectPluginConfig holds the LDAP project plugin configuration.
type ProjectPluginConfig struct {
	// OUTemplate is the LDAP OU path used for project groups.
	// Default: "ou=clustr-projects,<base_dn>"
	// The posixGroup is created at cn=<groupSlug>,<OUTemplate>.
	OUTemplate string

	// GroupNamePrefix is prepended to the NodeGroup slug to form the posixGroup CN.
	// Default: "clustr-project-"
	GroupNamePrefix string

	// BaseGIDNumber is used when auto-assigning GID numbers for project groups.
	// Groups are assigned BaseGIDNumber + sequential index. Default: 10000.
	BaseGIDNumber int
}

// defaultProjectPluginConfig returns production-safe defaults.
func defaultProjectPluginConfig() ProjectPluginConfig {
	return ProjectPluginConfig{
		OUTemplate:      "", // derived at runtime as "ou=clustr-projects,<base_dn>"
		GroupNamePrefix: "clustr-project-",
		BaseGIDNumber:   10000,
	}
}

// ProjectGroupCN returns the posixGroup CN for a NodeGroup given its name/slug.
// Uses the configured prefix + lowercased, hyphenated version of the group name.
func (m *Manager) ProjectGroupCN(groupName string) string {
	m.mu.RLock()
	prefix := m.projectPluginCfg.GroupNamePrefix
	m.mu.RUnlock()

	slug := strings.ToLower(strings.ReplaceAll(groupName, " ", "-"))
	// Remove characters not valid in LDAP CNs.
	var cleaned strings.Builder
	for _, ch := range slug {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			cleaned.WriteRune(ch)
		}
	}
	return prefix + cleaned.String()
}

// EnsureProjectGroup creates the posixGroup for a NodeGroup if it does not exist.
// Idempotent: EntryAlreadyExists is silently ignored.
// If LDAP is not ready, the operation is queued for later retry and the function
// returns immediately (non-blocking — never panics the caller).
//
// Called from:
//   - NodeGroup creation handler
//   - Admin-triggered re-sync
func (m *Manager) EnsureProjectGroup(ctx context.Context, groupID, groupName string) {
	if err := m.ensureProjectGroupWithError(ctx, groupID, groupName); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Str("group_name", groupName).
			Msg("ldap: project plugin: ensure project group failed — queuing for retry")
		// Queue a retry; never propagate to caller.
		_ = m.db.SetLDAPSyncState(ctx, groupID, db.LDAPSyncStateFailed, err.Error())
		_ = m.db.EnqueueLDAPSync(ctx, groupID, db.LDAPSyncOpCreateGroup,
			map[string]string{"group_name": groupName},
			time.Now().Add(1*time.Minute),
		)
	}
}

func (m *Manager) ensureProjectGroupWithError(ctx context.Context, groupID, groupName string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("project plugin: ldap not ready: %w", err)
	}

	cn := m.ProjectGroupCN(groupName)

	// Read the LDAP config to build the OU path.
	row, err := m.db.LDAPGetConfig(ctx)
	if err != nil {
		return fmt.Errorf("project plugin: read ldap config: %w", err)
	}

	// Determine the target OU. Use "ou=clustr-projects,<base_dn>" by default,
	// falling back to "ou=groups,<base_dn>" if the projects OU doesn't exist.
	baseDN := row.BaseDN
	projectsOU := fmt.Sprintf("ou=clustr-projects,%s", baseDN)

	// Try to create the clustr-projects OU first (idempotent).
	if ouErr := dit.EnsureOU("clustr-projects", baseDN); ouErr != nil {
		// Fall back to existing groups OU — still useful.
		log.Warn().Err(ouErr).Msg("ldap: project plugin: could not create ou=clustr-projects, using ou=groups")
		projectsOU = fmt.Sprintf("ou=groups,%s", baseDN)
	}

	// Build a GID by hashing the groupID to a number in the 10000–29999 range.
	gid := m.deriveGIDForGroup(groupID)

	// Create the posixGroup (or verify it exists).
	groupDN, err := dit.EnsureProjectGroup(cn, gid, groupName, projectsOU)
	if err != nil {
		return fmt.Errorf("project plugin: create posix group %s: %w", cn, err)
	}

	// Persist the DN and mark synced.
	if err := m.db.SetLDAPGroupDN(ctx, groupID, groupDN); err != nil {
		return fmt.Errorf("project plugin: save ldap group dn: %w", err)
	}

	log.Info().Str("group_id", groupID).Str("dn", groupDN).Msg("ldap: project plugin: posix group ensured")
	return nil
}

// SyncProjectGroupMembers reconciles the memberUid list of the project posixGroup
// with the current set of approved LDAP usernames for the group.
// Called after member add/remove and on periodic re-sync.
// Never blocks the caller — queues on failure.
func (m *Manager) SyncProjectGroupMembers(ctx context.Context, groupID, groupName string, currentMembers []string) {
	if err := m.syncProjectGroupMembersWithError(ctx, groupID, groupName, currentMembers); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Msg("ldap: project plugin: member sync failed — queuing")
		_ = m.db.SetLDAPSyncState(ctx, groupID, db.LDAPSyncStateFailed, err.Error())
		_ = m.db.EnqueueLDAPSync(ctx, groupID, db.LDAPSyncOpResync,
			map[string]string{"group_name": groupName},
			time.Now().Add(1*time.Minute),
		)
	}
}

func (m *Manager) syncProjectGroupMembersWithError(ctx context.Context, groupID, groupName string, wantMembers []string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("project plugin sync: ldap not ready: %w", err)
	}

	cn := m.ProjectGroupCN(groupName)

	// Fetch current memberUid list from LDAP.
	haveMembers, err := dit.GetGroupMembers(cn)
	if err != nil {
		// Group may not exist yet — trigger create first.
		_ = m.ensureProjectGroupWithError(ctx, groupID, groupName)
		return fmt.Errorf("project plugin sync: get group members for %s: %w", cn, err)
	}

	haveSet := toStringSet(haveMembers)
	wantSet := toStringSet(wantMembers)

	// Add members that are in wantSet but not in haveSet.
	for uid := range wantSet {
		if !haveSet[uid] {
			if addErr := dit.AddGroupMember(cn, uid); addErr != nil {
				log.Warn().Err(addErr).Str("uid", uid).Str("cn", cn).
					Msg("ldap: project plugin: add member failed (non-fatal)")
			}
		}
	}

	// Never remove members that were added manually outside clustr (additive-only
	// policy per the spec: "idempotent on re-sync; never deletes manually-added
	// LDAP members").

	_ = m.db.SetLDAPSyncState(ctx, groupID, db.LDAPSyncStateSynced, "")
	return nil
}

// AddProjectGroupMember adds a single uid to the project posixGroup.
// Non-blocking: queues on LDAP failure.
func (m *Manager) AddProjectGroupMember(ctx context.Context, groupID, groupName, uid string) {
	if err := m.addProjectGroupMemberWithError(ctx, groupName, uid); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Str("uid", uid).
			Msg("ldap: project plugin: add member failed — queuing")
		_ = m.db.SetLDAPSyncState(ctx, groupID, db.LDAPSyncStateFailed, err.Error())
		_ = m.db.EnqueueLDAPSync(ctx, groupID, db.LDAPSyncOpAddMember,
			map[string]string{"uid": uid, "group_name": groupName},
			time.Now().Add(1*time.Minute),
		)
	}
}

func (m *Manager) addProjectGroupMemberWithError(ctx context.Context, groupName, uid string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("project plugin add member: ldap not ready: %w", err)
	}
	cn := m.ProjectGroupCN(groupName)
	return dit.AddGroupMember(cn, uid)
}

// RemoveProjectGroupMember removes a uid from the project posixGroup.
// Non-blocking: queues on failure.
func (m *Manager) RemoveProjectGroupMember(ctx context.Context, groupID, groupName, uid string) {
	if err := m.removeProjectGroupMemberWithError(ctx, groupName, uid); err != nil {
		log.Error().Err(err).Str("group_id", groupID).Str("uid", uid).
			Msg("ldap: project plugin: remove member failed — queuing")
		_ = m.db.SetLDAPSyncState(ctx, groupID, db.LDAPSyncStateFailed, err.Error())
		_ = m.db.EnqueueLDAPSync(ctx, groupID, db.LDAPSyncOpRemoveMember,
			map[string]string{"uid": uid, "group_name": groupName},
			time.Now().Add(1*time.Minute),
		)
	}
}

func (m *Manager) removeProjectGroupMemberWithError(ctx context.Context, groupName, uid string) error {
	dit, err := m.DIT(ctx)
	if err != nil {
		return fmt.Errorf("project plugin remove member: ldap not ready: %w", err)
	}
	cn := m.ProjectGroupCN(groupName)
	return dit.RemoveGroupMember(cn, uid)
}

// RunSyncQueue drains the ldap_sync_queue retry table. Called periodically by
// the background worker. At most 20 items per run to bound latency.
func (m *Manager) RunSyncQueue(ctx context.Context) {
	items, err := m.db.DequeueLDAPSync(ctx, 20)
	if err != nil {
		log.Error().Err(err).Msg("ldap: project plugin: dequeue sync queue failed")
		return
	}
	for _, item := range items {
		m.processSyncQueueItem(ctx, item)
	}
	// Prune very old items (>7 days) so the queue doesn't grow unbounded.
	_ = m.db.PruneStaleLDAPSyncQueue(ctx)
}

func (m *Manager) processSyncQueueItem(ctx context.Context, item db.LDAPSyncQueueItem) {
	var opErr error

	switch item.Operation {
	case db.LDAPSyncOpCreateGroup:
		groupName := item.Payload["group_name"]
		opErr = m.ensureProjectGroupWithError(ctx, item.GroupID, groupName)

	case db.LDAPSyncOpAddMember:
		groupName := item.Payload["group_name"]
		uid := item.Payload["uid"]
		opErr = m.addProjectGroupMemberWithError(ctx, groupName, uid)

	case db.LDAPSyncOpRemoveMember:
		groupName := item.Payload["group_name"]
		uid := item.Payload["uid"]
		opErr = m.removeProjectGroupMemberWithError(ctx, groupName, uid)

	case db.LDAPSyncOpResync:
		groupName := item.Payload["group_name"]
		// Re-fetch current members from DB.
		members, err := m.db.ListApprovedMembersForGroup(ctx, item.GroupID)
		if err != nil {
			opErr = fmt.Errorf("queue resync: list members: %w", err)
			break
		}
		opErr = m.syncProjectGroupMembersWithError(ctx, item.GroupID, groupName, members)

	default:
		log.Warn().Str("operation", item.Operation).Msg("ldap: project plugin: unknown queue operation, discarding")
		_ = m.db.MarkLDAPSyncSuccess(ctx, item.ID)
		return
	}

	if opErr != nil {
		// Exponential backoff: 1m, 2m, 4m, 8m, 16m, capped at 60m.
		backoff := time.Duration(1<<uint(item.Attempt+1)) * time.Minute
		if backoff > 60*time.Minute {
			backoff = 60 * time.Minute
		}
		_ = m.db.MarkLDAPSyncRetry(ctx, item.ID, opErr.Error(), time.Now().Add(backoff))
		log.Warn().Err(opErr).Str("item_id", item.ID).
			Int("attempt", item.Attempt+1).
			Msg("ldap: project plugin: queue item retry scheduled")
		return
	}

	// Success — remove from queue and mark group as synced.
	_ = m.db.MarkLDAPSyncSuccess(ctx, item.ID)
	_ = m.db.SetLDAPSyncState(ctx, item.GroupID, db.LDAPSyncStateSynced, "")
	log.Debug().Str("item_id", item.ID).Str("operation", item.Operation).
		Msg("ldap: project plugin: queue item processed successfully")
}

// ─── Background worker ────────────────────────────────────────────────────────

// StartProjectPluginWorker launches the background sync goroutine that drains
// the queue and periodically re-syncs stale groups.
// Called from Manager.StartBackgroundWorkers.
func (m *Manager) StartProjectPluginWorker(ctx context.Context) {
	go m.runProjectPluginWorker(ctx)
}

func (m *Manager) runProjectPluginWorker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	log.Info().Msg("ldap: project plugin worker started")
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("ldap: project plugin worker stopping")
			return
		case <-ticker.C:
			// Check if LDAP is ready before doing anything.
			row, err := m.db.LDAPGetConfig(ctx)
			if err != nil || !row.Enabled || row.Status != statusReady {
				continue
			}
			m.RunSyncQueue(ctx)
		}
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// toStringSet converts a slice to a map-based set for O(1) membership checks.
func toStringSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// deriveGIDForGroup produces a stable GID for a NodeGroup from its UUID.
// Uses the first 4 bytes of the UUID hex as a number in 10000–29999 range.
func (m *Manager) deriveGIDForGroup(groupID string) int {
	// Remove hyphens from UUID and take first 8 hex chars → uint32 → map to range.
	hex := strings.ReplaceAll(groupID, "-", "")
	if len(hex) >= 8 {
		var n int
		for _, ch := range hex[:8] {
			v := 0
			switch {
			case ch >= '0' && ch <= '9':
				v = int(ch - '0')
			case ch >= 'a' && ch <= 'f':
				v = int(ch-'a') + 10
			case ch >= 'A' && ch <= 'F':
				v = int(ch-'A') + 10
			}
			n = n*16 + v
		}
		// Map to 10000–29999 range.
		return 10000 + (n % 20000)
	}
	return 10000
}

// The projectPluginCfg field on Manager is initialized to defaultProjectPluginConfig()
// in New(). See manager.go.
