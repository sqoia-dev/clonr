// ldap_project_plugin.go — DB operations for G1 OpenLDAP project plugin (Sprint G / CF-24).
//
// Every NodeGroup can optionally maintain a corresponding posixGroup in LDAP.
// These helpers manage the sync state columns added in migration 069 and the
// ldap_sync_queue retry table.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LDAPSyncState values for node_groups.ldap_sync_state.
const (
	LDAPSyncStateDisabled = "disabled"
	LDAPSyncStatePending  = "pending"
	LDAPSyncStateSynced   = "synced"
	LDAPSyncStateFailed   = "failed"
)

// LDAPSyncOperation values for ldap_sync_queue.operation.
const (
	LDAPSyncOpCreateGroup   = "create_group"
	LDAPSyncOpDeleteGroup   = "delete_group"
	LDAPSyncOpAddMember     = "add_member"
	LDAPSyncOpRemoveMember  = "remove_member"
	LDAPSyncOpResync        = "resync"
)

// LDAPProjectStatus holds the LDAP sync columns for a NodeGroup.
type LDAPProjectStatus struct {
	GroupID       string
	GroupName     string
	LDAPGroupDN   sql.NullString
	SyncState     string
	SyncLastAt    *time.Time
	SyncError     string
	SyncEnabled   bool
}

// LDAPSyncQueueItem is one row in ldap_sync_queue.
type LDAPSyncQueueItem struct {
	ID          string
	GroupID     string
	Operation   string
	Payload     map[string]string // decoded JSON
	Attempt     int
	LastError   string
	CreatedAt   time.Time
	NextRetryAt time.Time
}

// GetLDAPProjectStatus reads the LDAP sync columns for a single NodeGroup.
func (db *DB) GetLDAPProjectStatus(ctx context.Context, groupID string) (*LDAPProjectStatus, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT ng.id, ng.name,
		       ng.ldap_group_dn, ng.ldap_sync_state, ng.ldap_sync_last_at,
		       ng.ldap_sync_error, ng.ldap_sync_enabled
		FROM node_groups ng
		WHERE ng.id = ?`, groupID)

	var s LDAPProjectStatus
	var lastAtUnix sql.NullInt64
	if err := row.Scan(
		&s.GroupID, &s.GroupName,
		&s.LDAPGroupDN, &s.SyncState, &lastAtUnix,
		&s.SyncError, &s.SyncEnabled,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("db: node group %s not found", groupID)
		}
		return nil, fmt.Errorf("db: get ldap project status: %w", err)
	}
	if lastAtUnix.Valid {
		t := time.Unix(lastAtUnix.Int64, 0)
		s.SyncLastAt = &t
	}
	return &s, nil
}

// SetLDAPGroupDN updates the ldap_group_dn and sync state on a NodeGroup.
// Called after successfully creating the posixGroup in LDAP.
func (db *DB) SetLDAPGroupDN(ctx context.Context, groupID, dn string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET ldap_group_dn = ?, ldap_sync_state = ?, ldap_sync_last_at = ?, ldap_sync_error = ''
		WHERE id = ?`,
		dn, LDAPSyncStateSynced, now, groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set ldap group dn: %w", err)
	}
	return nil
}

// SetLDAPSyncState updates the sync state and error fields for a NodeGroup.
func (db *DB) SetLDAPSyncState(ctx context.Context, groupID, state, errMsg string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET ldap_sync_state = ?, ldap_sync_last_at = ?, ldap_sync_error = ?
		WHERE id = ?`,
		state, now, errMsg, groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set ldap sync state: %w", err)
	}
	return nil
}

// SetLDAPSyncEnabled enables or disables LDAP project sync for a NodeGroup.
func (db *DB) SetLDAPSyncEnabled(ctx context.Context, groupID string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	state := LDAPSyncStateDisabled
	if enabled {
		state = LDAPSyncStatePending
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE node_groups
		SET ldap_sync_enabled = ?, ldap_sync_state = ?
		WHERE id = ?`,
		v, state, groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set ldap sync enabled: %w", err)
	}
	return nil
}

// ListGroupsNeedingLDAPSync returns groups where LDAP sync is enabled but state
// is 'pending' or 'failed'. Used by the background sync worker.
func (db *DB) ListGroupsNeedingLDAPSync(ctx context.Context) ([]LDAPProjectStatus, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name,
		       ng.ldap_group_dn, ng.ldap_sync_state, ng.ldap_sync_last_at,
		       ng.ldap_sync_error, ng.ldap_sync_enabled
		FROM node_groups ng
		WHERE ng.ldap_sync_enabled = 1
		  AND ng.ldap_sync_state IN ('pending', 'failed')
		ORDER BY ng.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: list groups needing ldap sync: %w", err)
	}
	defer rows.Close()
	return scanLDAPProjectStatuses(rows)
}

// ListAllLDAPProjectStatuses returns LDAP sync status for all groups.
// Used by admin LDAP project status view.
func (db *DB) ListAllLDAPProjectStatuses(ctx context.Context) ([]LDAPProjectStatus, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name,
		       ng.ldap_group_dn, ng.ldap_sync_state, ng.ldap_sync_last_at,
		       ng.ldap_sync_error, ng.ldap_sync_enabled
		FROM node_groups ng
		ORDER BY ng.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("db: list all ldap project statuses: %w", err)
	}
	defer rows.Close()
	return scanLDAPProjectStatuses(rows)
}

func scanLDAPProjectStatuses(rows *sql.Rows) ([]LDAPProjectStatus, error) {
	var out []LDAPProjectStatus
	for rows.Next() {
		var s LDAPProjectStatus
		var lastAtUnix sql.NullInt64
		if err := rows.Scan(
			&s.GroupID, &s.GroupName,
			&s.LDAPGroupDN, &s.SyncState, &lastAtUnix,
			&s.SyncError, &s.SyncEnabled,
		); err != nil {
			return nil, fmt.Errorf("db: scan ldap project status: %w", err)
		}
		if lastAtUnix.Valid {
			t := time.Unix(lastAtUnix.Int64, 0)
			s.SyncLastAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── LDAP sync queue ──────────────────────────────────────────────────────────

// EnqueueLDAPSync adds an operation to the retry queue.
// nextRetry is when the worker should first attempt this item.
func (db *DB) EnqueueLDAPSync(ctx context.Context, groupID, operation string, payload map[string]string, nextRetry time.Time) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("db: enqueue ldap sync: marshal payload: %w", err)
	}
	id := uuid.New().String()
	now := time.Now().Unix()
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO ldap_sync_queue
		       (id, group_id, operation, payload, attempt, last_error, created_at, next_retry_at)
		VALUES (?,  ?,        ?,         ?,        0,        '',         ?,          ?)`,
		id, groupID, operation, string(payloadJSON), now, nextRetry.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: enqueue ldap sync: %w", err)
	}
	return nil
}

// DequeueLDAPSync returns up to limit items due for retry (next_retry_at <= now).
func (db *DB) DequeueLDAPSync(ctx context.Context, limit int) ([]LDAPSyncQueueItem, error) {
	now := time.Now().Unix()
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, group_id, operation, payload, attempt, last_error, created_at, next_retry_at
		FROM ldap_sync_queue
		WHERE next_retry_at <= ?
		ORDER BY next_retry_at ASC
		LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("db: dequeue ldap sync: %w", err)
	}
	defer rows.Close()

	var out []LDAPSyncQueueItem
	for rows.Next() {
		var item LDAPSyncQueueItem
		var payloadStr string
		var createdUnix, nextUnix int64
		if err := rows.Scan(
			&item.ID, &item.GroupID, &item.Operation, &payloadStr,
			&item.Attempt, &item.LastError, &createdUnix, &nextUnix,
		); err != nil {
			return nil, fmt.Errorf("db: scan ldap sync queue: %w", err)
		}
		item.CreatedAt = time.Unix(createdUnix, 0)
		item.NextRetryAt = time.Unix(nextUnix, 0)
		if err := json.Unmarshal([]byte(payloadStr), &item.Payload); err != nil {
			item.Payload = map[string]string{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkLDAPSyncSuccess deletes a queue item on successful execution.
func (db *DB) MarkLDAPSyncSuccess(ctx context.Context, itemID string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM ldap_sync_queue WHERE id = ?`, itemID)
	return err
}

// MarkLDAPSyncRetry increments attempt, sets last_error, and schedules the next retry.
func (db *DB) MarkLDAPSyncRetry(ctx context.Context, itemID, errMsg string, nextRetry time.Time) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE ldap_sync_queue
		SET attempt = attempt + 1, last_error = ?, next_retry_at = ?
		WHERE id = ?`,
		errMsg, nextRetry.Unix(), itemID,
	)
	return err
}

// PruneStaleLDAPSyncQueue removes items older than 7 days (they've failed too many times).
func (db *DB) PruneStaleLDAPSyncQueue(ctx context.Context) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM ldap_sync_queue WHERE created_at < ?`, cutoff)
	return err
}

// ListApprovedMembersForGroup returns the LDAP usernames of all approved members
// of a NodeGroup. Alias for ListApprovedMemberEmails (same data, different name
// clarifies intent in the project plugin context).
func (db *DB) ListApprovedMembersForGroup(ctx context.Context, groupID string) ([]string, error) {
	return db.ListApprovedMemberEmails(ctx, groupID)
}
