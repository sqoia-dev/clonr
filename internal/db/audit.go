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
	AuditActionNodeCreate       = "node.create"
	AuditActionNodeUpdate       = "node.update"
	AuditActionNodeDelete       = "node.delete"
	AuditActionNodeReimage      = "node.reimage"
	AuditActionImageCreate      = "image.create"
	AuditActionImageDelete      = "image.delete"
	AuditActionImageArchive     = "image.archive"
	AuditActionImageStatusChange = "image.status_change"
	AuditActionGroupCreate      = "node_group.create"
	AuditActionGroupUpdate      = "node_group.update"
	AuditActionGroupDelete      = "node_group.delete"
	AuditActionGroupReimage     = "node_group.reimage"
	AuditActionUserCreate       = "user.create"
	AuditActionUserUpdate       = "user.update"
	AuditActionUserDelete       = "user.delete"
	AuditActionUserResetPassword = "user.reset_password"
	AuditActionAPIKeyCreate     = "api_key.create"
	AuditActionAPIKeyRevoke     = "api_key.revoke"     //#nosec G101 -- audit event name string, not a credential
	AuditActionAPIKeyRotate     = "api_key.rotate"     //#nosec G101 -- audit event name string, not a credential
	AuditActionGroupMemberAdd   = "node_group.member_add"
	AuditActionGroupMemberRemove = "node_group.member_remove"
	AuditActionUserGroupMemberships = "user.group_memberships_update"
	AuditActionLDAPConfigChange = "ldap_config.update"
	AuditActionSlurmConfigChange = "slurm_config.update"
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
