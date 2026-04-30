package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Audit action constants — used as the action column in audit_log.
const (
	AuditActionNodeCreate           = "node.create"
	AuditActionNodeUpdate           = "node.update"
	AuditActionNodeDelete           = "node.delete"
	AuditActionNodeReimage          = "node.reimage"
	AuditActionNodeProviderChanged  = "node.provider.changed"
	AuditActionImageCreate          = "image.create"
	AuditActionImageDelete          = "image.delete"
	AuditActionImageArchive         = "image.archive"
	AuditActionImageStatusChange    = "image.status_change"
	AuditActionGroupCreate          = "node_group.create"
	AuditActionGroupUpdate          = "node_group.update"
	AuditActionGroupDelete          = "node_group.delete"
	AuditActionGroupReimage         = "node_group.reimage"
	AuditActionUserCreate           = "user.create"
	AuditActionUserUpdate           = "user.update"
	AuditActionUserDelete           = "user.delete"
	AuditActionUserResetPassword    = "user.reset_password"
	AuditActionAPIKeyCreate         = "api_key.create"
	AuditActionAPIKeyRevoke         = "api_key.revoke" //#nosec G101 -- audit event name string, not a credential
	AuditActionAPIKeyRotate         = "api_key.rotate" //#nosec G101 -- audit event name string, not a credential
	AuditActionGroupMemberAdd       = "node_group.member_add"
	AuditActionGroupMemberRemove    = "node_group.member_remove"
	AuditActionUserGroupMemberships = "user.group_memberships_update"
	AuditActionLDAPConfigChange     = "ldap_config.update"
	AuditActionSlurmConfigChange    = "slurm_config.update"
	// AuditActionSlurmInstallFailed is recorded when the in-chroot dnf Slurm
	// install step fails during a node deploy. Query with:
	//   GET /api/v1/audit?action=slurm.install.failed
	// The new_value JSON contains "repo_url" and "detail" (last 2KB of dnf output).
	AuditActionSlurmInstallFailed = "slurm.install.failed"

	// Sprint F expiration events.
	AuditActionGroupExpirationSet     = "node_group.expiration_set"
	AuditActionGroupExpirationCleared = "node_group.expiration_cleared"
	AuditActionExpirationWarning      = "node_group.expiration_warning"

	// Sprint D notification + grant/pub/review events.
	AuditActionNotificationSent     = "notification.sent"
	AuditActionNotificationFailed   = "notification.failed"
	AuditActionNotificationSkipped  = "notification.skipped"
	AuditActionBroadcastSent        = "broadcast.sent"
	AuditActionBroadcastSkipped     = "broadcast.skipped"
	AuditActionSMTPConfigUpdate     = "smtp_config.update"
	AuditActionSMTPTestSend         = "smtp_config.test_send"
	AuditActionGrantCreate          = "grant.create"
	AuditActionGrantUpdate          = "grant.update"
	AuditActionGrantDelete          = "grant.delete"
	AuditActionPublicationCreate    = "publication.create"
	AuditActionPublicationUpdate    = "publication.update"
	AuditActionPublicationDelete    = "publication.delete"
	AuditActionReviewCycleCreate    = "review_cycle.create"
	AuditActionReviewResponseSubmit = "review_response.submit"

	// Sprint 6 — power, BMC, image lifecycle events (X6-1).
	AuditActionNodePowerOn          = "node.power.on"
	AuditActionNodePowerOff         = "node.power.off"
	AuditActionNodePowerCycled      = "node.power.cycled"
	AuditActionNodePowerReset       = "node.power.reset"
	AuditActionNodeBootPXE          = "node.power.boot_pxe"
	AuditActionNodeBootDisk         = "node.power.boot_disk"
	AuditActionNodeBMCUpdated       = "node.bmc.updated"
	AuditActionImageCaptured        = "image.captured"
	AuditActionImageShellStart      = "image.shell.started"
	AuditActionImageShellEnd        = "image.shell.ended"
	AuditActionImageShellDepMissing = "image.shell.dep_missing"

	// Sprint 7 — Identity surface events (X7-1).
	AuditActionNodeSudoerAdded   = "node.sudoer.added"
	AuditActionNodeSudoerRemoved = "node.sudoer.removed"
	AuditActionNodeSudoerSynced  = "node.sudoer.synced"
	AuditActionLDAPConfigUpdated = "ldap.config.updated"
	AuditActionLDAPTestRun       = "ldap.test.run"
	AuditActionSysAccountCreated = "system-account.created"
	AuditActionSysAccountUpdated = "system-account.updated"
	AuditActionSysAccountDeleted = "system-account.deleted"

	// Sprint 8 — LDAP directory write events (WRITE-AUDIT-1).
	// All directory writes carry directory_write:true in new_value JSON.
	AuditActionLDAPUserCreated      = "ldap.directory.user.created"
	AuditActionLDAPUserUpdated      = "ldap.directory.user.updated"
	AuditActionLDAPUserDeleted      = "ldap.directory.user.deleted"
	AuditActionLDAPPasswordReset    = "ldap.directory.user.password_reset"    //#nosec G101 -- audit event name, not a credential
	AuditActionLDAPGroupCreated     = "ldap.directory.group.created"
	AuditActionLDAPGroupUpdated     = "ldap.directory.group.updated"
	AuditActionLDAPGroupDeleted     = "ldap.directory.group.deleted"
	AuditActionLDAPGroupModeChanged = "ldap.directory.group.mode_changed"
	AuditActionLDAPWriteBindSaved   = "ldap.write_bind.saved"
	AuditActionLDAPWriteProbe       = "ldap.write_bind.probe"
)

// AuditRecord is one row in audit_log.
type AuditRecord struct {
	ID           string
	ActorID      string
	ActorLabel   string
	Action       string
	ResourceType string
	ResourceID   string
	OldValue     *json.RawMessage
	NewValue     *json.RawMessage
	IPAddr       string
	CreatedAt    time.Time
}

// AuditService records audit events to the database.
// It is safe to call Record from multiple goroutines.
type AuditService struct {
	db *DB
}

// NewAuditService constructs an AuditService backed by db.
func NewAuditService(db *DB) *AuditService {
	return &AuditService{db: db}
}

// RecordEntry inserts a fully-constructed AuditRecord.
func (a *AuditService) RecordEntry(ctx context.Context, rec AuditRecord) error {
	var oldJSON, newJSON sql.NullString
	if rec.OldValue != nil {
		oldJSON = sql.NullString{String: string(*rec.OldValue), Valid: true}
	}
	if rec.NewValue != nil {
		newJSON = sql.NullString{String: string(*rec.NewValue), Valid: true}
	}

	_, err := a.db.sql.ExecContext(ctx, `
		INSERT INTO audit_log
			(id, actor_id, actor_label, action, resource_type, resource_id,
			 old_value, new_value, ip_addr, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID,
		rec.ActorID,
		rec.ActorLabel,
		rec.Action,
		rec.ResourceType,
		rec.ResourceID,
		oldJSON,
		newJSON,
		rec.IPAddr,
		rec.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: audit record: %w", err)
	}
	return nil
}

// Record is a convenience wrapper that marshals oldVal and newVal to JSON.
// Pass nil for oldVal/newVal when not applicable (creates / deletes).
// Non-fatal: errors are logged but do not cause the caller to fail.
func (a *AuditService) Record(ctx context.Context, actorID, actorLabel, action, resourceType, resourceID, ipAddr string, oldVal, newVal interface{}) {
	rec := AuditRecord{
		ID:           fmt.Sprintf("aud-%d", time.Now().UnixNano()),
		ActorID:      actorID,
		ActorLabel:   actorLabel,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddr:       ipAddr,
		CreatedAt:    time.Now().UTC(),
	}
	if oldVal != nil {
		b, err := json.Marshal(oldVal)
		if err == nil {
			raw := json.RawMessage(b)
			rec.OldValue = &raw
		}
	}
	if newVal != nil {
		b, err := json.Marshal(newVal)
		if err == nil {
			raw := json.RawMessage(b)
			rec.NewValue = &raw
		}
	}
	// Best-effort; caller's workflow continues on error.
	_ = a.RecordEntry(ctx, rec)
}

// AuditQueryParams are the filters for GET /api/v1/audit.
type AuditQueryParams struct {
	Since        time.Time
	Until        time.Time
	ActorID      string
	Action       string
	ResourceType string
	Limit        int
	Offset       int
}

// QueryAuditLog returns paginated audit log records matching the given filters.
func (db *DB) QueryAuditLog(ctx context.Context, p AuditQueryParams) ([]AuditRecord, int, error) {
	if p.Limit <= 0 || p.Limit > 500 {
		p.Limit = 100
	}

	// Build dynamic WHERE clause.
	where := "WHERE 1=1"
	args := []interface{}{}

	if !p.Since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, p.Since.Unix())
	}
	if !p.Until.IsZero() {
		where += " AND created_at <= ?"
		args = append(args, p.Until.Unix())
	}
	if p.ActorID != "" {
		where += " AND actor_id = ?"
		args = append(args, p.ActorID)
	}
	if p.Action != "" {
		where += " AND action = ?"
		args = append(args, p.Action)
	}
	if p.ResourceType != "" {
		where += " AND resource_type = ?"
		args = append(args, p.ResourceType)
	}

	// Count total.
	var total int
	if err := db.sql.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM audit_log "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: query audit log count: %w", err)
	}

	// Fetch page.
	query := "SELECT id, actor_id, actor_label, action, resource_type, resource_id, old_value, new_value, ip_addr, created_at FROM audit_log " +
		where + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit, p.Offset)

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("db: query audit log: %w", err)
	}
	defer rows.Close()

	var out []AuditRecord
	for rows.Next() {
		var rec AuditRecord
		var createdAt int64
		var oldVal, newVal sql.NullString
		if err := rows.Scan(
			&rec.ID, &rec.ActorID, &rec.ActorLabel, &rec.Action,
			&rec.ResourceType, &rec.ResourceID, &oldVal, &newVal, &rec.IPAddr, &createdAt,
		); err != nil {
			return nil, 0, fmt.Errorf("db: query audit log scan: %w", err)
		}
		rec.CreatedAt = time.Unix(createdAt, 0).UTC()
		if oldVal.Valid {
			raw := json.RawMessage(oldVal.String)
			rec.OldValue = &raw
		}
		if newVal.Valid {
			raw := json.RawMessage(newVal.String)
			rec.NewValue = &raw
		}
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// StreamAuditLog streams audit records matching p to fn in ascending created_at
// order (oldest first — SIEM consumers expect chronological order).
// Unlike QueryAuditLog this uses an unbounded query with no pagination and
// calls fn for each record row; fn should write to the HTTP response and flush.
// The caller is responsible for enforcing reasonable time bounds via p.Since/Until.
func (db *DB) StreamAuditLog(ctx context.Context, p AuditQueryParams, fn func(AuditRecord) error) error {
	// Build dynamic WHERE clause (same as QueryAuditLog).
	where := "WHERE 1=1"
	args := []interface{}{}

	if !p.Since.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, p.Since.Unix())
	}
	if !p.Until.IsZero() {
		where += " AND created_at <= ?"
		args = append(args, p.Until.Unix())
	}
	if p.ActorID != "" {
		where += " AND actor_id = ?"
		args = append(args, p.ActorID)
	}
	if p.Action != "" {
		where += " AND action = ?"
		args = append(args, p.Action)
	}
	if p.ResourceType != "" {
		where += " AND resource_type = ?"
		args = append(args, p.ResourceType)
	}

	query := "SELECT id, actor_id, actor_label, action, resource_type, resource_id, " +
		"old_value, new_value, ip_addr, created_at " +
		"FROM audit_log " + where + " ORDER BY created_at ASC"

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("db: stream audit log: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rec AuditRecord
		var createdAt int64
		var oldVal, newVal sql.NullString
		if err := rows.Scan(
			&rec.ID, &rec.ActorID, &rec.ActorLabel, &rec.Action,
			&rec.ResourceType, &rec.ResourceID, &oldVal, &newVal, &rec.IPAddr, &createdAt,
		); err != nil {
			return fmt.Errorf("db: stream audit log scan: %w", err)
		}
		rec.CreatedAt = time.Unix(createdAt, 0).UTC()
		if oldVal.Valid {
			raw := json.RawMessage(oldVal.String)
			rec.OldValue = &raw
		}
		if newVal.Valid {
			raw := json.RawMessage(newVal.String)
			rec.NewValue = &raw
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DeleteAuditRecord removes a single audit log entry by ID.
// ACT-DEL-1 (Sprint 4). Returns sql.ErrNoRows if the record does not exist.
func (db *DB) DeleteAuditRecord(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM audit_log WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete audit record: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete audit record rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("audit record not found: %s", id)
	}
	return nil
}

// BulkDeleteAuditRecords removes audit log entries matching the given params.
// ACT-DEL-1 (Sprint 4). Returns the count of deleted records.
// If p.Until is zero the call is rejected (require an explicit time bound for safety).
func (db *DB) BulkDeleteAuditRecords(ctx context.Context, p AuditQueryParams) (int, error) {
	if p.Until.IsZero() {
		return 0, fmt.Errorf("bulk delete requires a 'before' time bound")
	}

	where := "WHERE created_at <= ?"
	args := []interface{}{p.Until.Unix()}
	if p.Action != "" {
		where += " AND action = ?"
		args = append(args, p.Action)
	}
	if p.ResourceType != "" {
		where += " AND resource_type = ?"
		args = append(args, p.ResourceType)
	}
	// Protect audit.purged records from bulk deletion.
	where += " AND action != 'audit.purged'"

	res, err := db.sql.ExecContext(ctx, `DELETE FROM audit_log `+where, args...)
	if err != nil {
		return 0, fmt.Errorf("db: bulk delete audit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("db: bulk delete audit rows affected: %w", err)
	}
	return int(n), nil
}
