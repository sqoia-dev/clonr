package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ─── PI member requests ───────────────────────────────────────────────────────

// PIMemberRequest is a pending or resolved PI-submitted member-add request.
type PIMemberRequest struct {
	ID           string
	GroupID      string
	GroupName    string // populated by joins
	PIUserID     string
	LDAPUsername string
	Status       string // pending | approved | denied
	RequestedAt  time.Time
	ResolvedAt   *time.Time
	ResolvedBy   string
	Note         string
}

// PIExpansionRequest is a pending or resolved PI node-expansion request.
type PIExpansionRequest struct {
	ID            string
	GroupID       string
	GroupName     string // populated by joins
	PIUserID      string
	Justification string
	Status        string // pending | acknowledged | dismissed
	RequestedAt   time.Time
	ResolvedAt    *time.Time
	ResolvedBy    string
}

// ErrRequestNotFound is returned when a PI request row does not exist.
var ErrRequestNotFound = fmt.Errorf("db: pi request not found")

// ─── NodeGroup PI ownership ───────────────────────────────────────────────────

// SetNodeGroupPI sets the pi_user_id on a NodeGroup. Pass empty string to clear.
func (db *DB) SetNodeGroupPI(ctx context.Context, groupID, piUserID string) error {
	var piNull sql.NullString
	if piUserID != "" {
		piNull = sql.NullString{String: piUserID, Valid: true}
	}
	res, err := db.sql.ExecContext(ctx,
		`UPDATE node_groups SET pi_user_id = ?, updated_at = ? WHERE id = ?`,
		piNull, time.Now().Unix(), groupID,
	)
	if err != nil {
		return fmt.Errorf("db: set node group pi: %w", err)
	}
	return requireOneRow(res, "node_groups", groupID)
}

// ListAllNodeGroupSummaries returns all NodeGroups with utilization summary columns.
// Used by admin users viewing the PI management surface.
func (db *DB) ListAllNodeGroupSummaries(ctx context.Context) ([]NodeGroupSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.pi_user_id,
		       u.username AS pi_username,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		LEFT JOIN users u ON u.id = ng.pi_user_id
		ORDER BY ng.name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list all node group summaries: %w", err)
	}
	defer rows.Close()
	return scanNodeGroupSummaries(rows)
}

// ListNodeGroupsByPI returns all NodeGroups where pi_user_id = piUserID.
func (db *DB) ListNodeGroupsByPI(ctx context.Context, piUserID string) ([]NodeGroupSummary, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.pi_user_id,
		       u.username AS pi_username,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		LEFT JOIN users u ON u.id = ng.pi_user_id
		WHERE ng.pi_user_id = ?
		ORDER BY ng.name ASC`,
		piUserID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list node groups by pi: %w", err)
	}
	defer rows.Close()
	return scanNodeGroupSummaries(rows)
}

// GetNodeGroupSummary returns a NodeGroupSummary for a single group.
func (db *DB) GetNodeGroupSummary(ctx context.Context, groupID string) (NodeGroupSummary, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT ng.id, ng.name, ng.description, ng.role,
		       (SELECT COUNT(*) FROM node_group_memberships m WHERE m.group_id = ng.id) AS node_count,
		       (SELECT COUNT(*) FROM node_configs nc
		         LEFT JOIN node_group_memberships m2 ON m2.node_id = nc.id AND m2.is_primary = 1
		         WHERE m2.group_id = ng.id AND nc.deploy_completed_preboot_at IS NOT NULL) AS deployed_count,
		       ng.pi_user_id,
		       u.username AS pi_username,
		       ng.created_at, ng.updated_at
		FROM node_groups ng
		LEFT JOIN users u ON u.id = ng.pi_user_id
		WHERE ng.id = ?`,
		groupID,
	)
	var s NodeGroupSummary
	var piUserID, piUsername sql.NullString
	var role sql.NullString
	var createdAt, updatedAt int64
	err := row.Scan(
		&s.ID, &s.Name, &s.Description, &role,
		&s.NodeCount, &s.DeployedCount,
		&piUserID, &piUsername,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeGroupSummary{}, ErrRequestNotFound
	}
	if err != nil {
		return NodeGroupSummary{}, fmt.Errorf("db: get node group summary: %w", err)
	}
	s.Role = role.String
	s.PIUserID = piUserID.String
	s.PIUsername = piUsername.String
	s.CreatedAt = time.Unix(createdAt, 0)
	s.UpdatedAt = time.Unix(updatedAt, 0)
	return s, nil
}

// NodeGroupSummary is a lightweight view of a NodeGroup used by the PI portal.
type NodeGroupSummary struct {
	ID            string
	Name          string
	Description   string
	Role          string
	NodeCount     int
	DeployedCount int
	PIUserID      string
	PIUsername    string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func scanNodeGroupSummaries(rows *sql.Rows) ([]NodeGroupSummary, error) {
	var out []NodeGroupSummary
	for rows.Next() {
		var s NodeGroupSummary
		var piUserID, piUsername sql.NullString
		var role sql.NullString
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&s.ID, &s.Name, &s.Description, &role,
			&s.NodeCount, &s.DeployedCount,
			&piUserID, &piUsername,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scan node group summary: %w", err)
		}
		s.Role = role.String
		s.PIUserID = piUserID.String
		s.PIUsername = piUsername.String
		s.CreatedAt = time.Unix(createdAt, 0)
		s.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── PI utilization ───────────────────────────────────────────────────────────

// PIGroupUtilization holds aggregated stats for a NodeGroup, sourced from existing
// tables with no new schema. Gaps (e.g. no job data) are surfaced as zero or nil.
type PIGroupUtilization struct {
	GroupID        string
	GroupName      string
	NodeCount      int
	DeployedCount  int
	UndeployedCount int
	LastDeployAt   *time.Time
	FailedDeploys30d int
	MemberCount    int
	PartitionState string // from slurm_partitions if available
}

// GetPIGroupUtilization returns aggregated utilization stats for a NodeGroup.
// Pure SQL aggregation over existing tables — no rollup tables.
func (db *DB) GetPIGroupUtilization(ctx context.Context, groupID string) (PIGroupUtilization, error) {
	var u PIGroupUtilization
	u.GroupID = groupID

	// Basic group info.
	err := db.sql.QueryRowContext(ctx,
		`SELECT name FROM node_groups WHERE id = ?`, groupID,
	).Scan(&u.GroupName)
	if errors.Is(err, sql.ErrNoRows) {
		return PIGroupUtilization{}, ErrRequestNotFound
	}
	if err != nil {
		return PIGroupUtilization{}, fmt.Errorf("db: get pi utilization group: %w", err)
	}

	// Node counts. deploy_completed_preboot_at is the canonical deploy success timestamp
	// (last_deploy_succeeded_at was dropped in migration 049).
	err = db.sql.QueryRowContext(ctx, `
		SELECT
			COUNT(*) AS total,
			SUM(CASE WHEN nc.deploy_completed_preboot_at IS NOT NULL THEN 1 ELSE 0 END) AS deployed
		FROM node_configs nc
		LEFT JOIN node_group_memberships m ON m.node_id = nc.id AND m.is_primary = 1
		WHERE m.group_id = ?`,
		groupID,
	).Scan(&u.NodeCount, &u.DeployedCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PIGroupUtilization{}, fmt.Errorf("db: get pi utilization counts: %w", err)
	}
	u.UndeployedCount = u.NodeCount - u.DeployedCount

	// Last deploy timestamp.
	var lastDeployUnix sql.NullInt64
	_ = db.sql.QueryRowContext(ctx, `
		SELECT MAX(nc.deploy_completed_preboot_at)
		FROM node_configs nc
		LEFT JOIN node_group_memberships m ON m.node_id = nc.id AND m.is_primary = 1
		WHERE m.group_id = ?`,
		groupID,
	).Scan(&lastDeployUnix)
	if lastDeployUnix.Valid && lastDeployUnix.Int64 > 0 {
		t := time.Unix(lastDeployUnix.Int64, 0)
		u.LastDeployAt = &t
	}

	// Failed deploys in last 30 days — sourced from audit_log.
	cutoff := time.Now().AddDate(0, 0, -30).Unix()
	_ = db.sql.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM audit_log al
		JOIN node_configs nc ON nc.id = al.resource_id
		LEFT JOIN node_group_memberships m ON m.node_id = nc.id AND m.is_primary = 1
		WHERE m.group_id = ?
		  AND al.action = 'node.reimage'
		  AND al.created_at >= ?
		  AND json_extract(al.new_value, '$.status') IN ('failed','verify_timeout')`,
		groupID, cutoff,
	).Scan(&u.FailedDeploys30d)

	// Member count (LDAP group memberships — approximated from the request table).
	// If LDAP is available, this is the active member count from pi_member_requests
	// with status='approved'. If not, we return 0 and let the handler label it "unavailable".
	_ = db.sql.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT ldap_username)
		FROM pi_member_requests
		WHERE group_id = ? AND status = 'approved'`,
		groupID,
	).Scan(&u.MemberCount)

	// Partition state is not available from the DB (slurm_partitions is not a
	// persistent table — Slurm status is fetched live via the Slurm manager).
	// The handler surfaces this as "unavailable" when empty.
	u.PartitionState = ""

	return u, nil
}

// ─── PI member request operations ────────────────────────────────────────────

// CreatePIMemberRequest inserts a new pending member request.
func (db *DB) CreatePIMemberRequest(ctx context.Context, req PIMemberRequest) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO pi_member_requests (id, group_id, pi_user_id, ldap_username, status, requested_at)
		VALUES (?, ?, ?, ?, 'pending', ?)`,
		req.ID, req.GroupID, req.PIUserID, req.LDAPUsername, req.RequestedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create pi member request: %w", err)
	}
	return nil
}

// ListPIMemberRequests returns pending PI member requests, optionally filtered by groupID.
func (db *DB) ListPIMemberRequests(ctx context.Context, groupID string, status string) ([]PIMemberRequest, error) {
	query := `
		SELECT r.id, r.group_id, ng.name, r.pi_user_id, r.ldap_username,
		       r.status, r.requested_at, r.resolved_at, r.resolved_by, r.note
		FROM pi_member_requests r
		JOIN node_groups ng ON ng.id = r.group_id
		WHERE 1=1`
	args := []interface{}{}
	if groupID != "" {
		query += " AND r.group_id = ?"
		args = append(args, groupID)
	}
	if status != "" {
		query += " AND r.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY r.requested_at DESC"

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list pi member requests: %w", err)
	}
	defer rows.Close()

	var out []PIMemberRequest
	for rows.Next() {
		var r PIMemberRequest
		var requestedAt int64
		var resolvedAt sql.NullInt64
		var resolvedBy sql.NullString
		if err := rows.Scan(
			&r.ID, &r.GroupID, &r.GroupName, &r.PIUserID, &r.LDAPUsername,
			&r.Status, &requestedAt, &resolvedAt, &resolvedBy, &r.Note,
		); err != nil {
			return nil, fmt.Errorf("db: scan pi member request: %w", err)
		}
		r.RequestedAt = time.Unix(requestedAt, 0)
		if resolvedAt.Valid {
			t := time.Unix(resolvedAt.Int64, 0)
			r.ResolvedAt = &t
		}
		r.ResolvedBy = resolvedBy.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolvePIMemberRequest marks a request as approved or denied.
func (db *DB) ResolvePIMemberRequest(ctx context.Context, requestID, status, resolvedBy string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE pi_member_requests
		SET status = ?, resolved_at = ?, resolved_by = ?
		WHERE id = ? AND status = 'pending'`,
		status, time.Now().Unix(), resolvedBy, requestID,
	)
	if err != nil {
		return fmt.Errorf("db: resolve pi member request: %w", err)
	}
	return requireOneRow(res, "pi_member_requests", requestID)
}

// GetPIMemberRequest fetches a single request by ID.
func (db *DB) GetPIMemberRequest(ctx context.Context, requestID string) (PIMemberRequest, error) {
	var r PIMemberRequest
	var requestedAt int64
	var resolvedAt sql.NullInt64
	var resolvedBy sql.NullString
	err := db.sql.QueryRowContext(ctx, `
		SELECT r.id, r.group_id, ng.name, r.pi_user_id, r.ldap_username,
		       r.status, r.requested_at, r.resolved_at, r.resolved_by, r.note
		FROM pi_member_requests r
		JOIN node_groups ng ON ng.id = r.group_id
		WHERE r.id = ?`,
		requestID,
	).Scan(
		&r.ID, &r.GroupID, &r.GroupName, &r.PIUserID, &r.LDAPUsername,
		&r.Status, &requestedAt, &resolvedAt, &resolvedBy, &r.Note,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PIMemberRequest{}, ErrRequestNotFound
	}
	if err != nil {
		return PIMemberRequest{}, fmt.Errorf("db: get pi member request: %w", err)
	}
	r.RequestedAt = time.Unix(requestedAt, 0)
	if resolvedAt.Valid {
		t := time.Unix(resolvedAt.Int64, 0)
		r.ResolvedAt = &t
	}
	r.ResolvedBy = resolvedBy.String
	return r, nil
}

// ─── PI expansion request operations ─────────────────────────────────────────

// CreatePIExpansionRequest inserts a new pending expansion request.
func (db *DB) CreatePIExpansionRequest(ctx context.Context, req PIExpansionRequest) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO pi_expansion_requests (id, group_id, pi_user_id, justification, status, requested_at)
		VALUES (?, ?, ?, ?, 'pending', ?)`,
		req.ID, req.GroupID, req.PIUserID, req.Justification, req.RequestedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create pi expansion request: %w", err)
	}
	return nil
}

// ListPIExpansionRequests returns expansion requests, optionally filtered by groupID/status.
func (db *DB) ListPIExpansionRequests(ctx context.Context, groupID, status string) ([]PIExpansionRequest, error) {
	query := `
		SELECT r.id, r.group_id, ng.name, r.pi_user_id, r.justification,
		       r.status, r.requested_at, r.resolved_at, r.resolved_by
		FROM pi_expansion_requests r
		JOIN node_groups ng ON ng.id = r.group_id
		WHERE 1=1`
	args := []interface{}{}
	if groupID != "" {
		query += " AND r.group_id = ?"
		args = append(args, groupID)
	}
	if status != "" {
		query += " AND r.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY r.requested_at DESC"

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list pi expansion requests: %w", err)
	}
	defer rows.Close()

	var out []PIExpansionRequest
	for rows.Next() {
		var r PIExpansionRequest
		var requestedAt int64
		var resolvedAt sql.NullInt64
		var resolvedBy sql.NullString
		if err := rows.Scan(
			&r.ID, &r.GroupID, &r.GroupName, &r.PIUserID, &r.Justification,
			&r.Status, &requestedAt, &resolvedAt, &resolvedBy,
		); err != nil {
			return nil, fmt.Errorf("db: scan pi expansion request: %w", err)
		}
		r.RequestedAt = time.Unix(requestedAt, 0)
		if resolvedAt.Valid {
			t := time.Unix(resolvedAt.Int64, 0)
			r.ResolvedAt = &t
		}
		r.ResolvedBy = resolvedBy.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResolvePIExpansionRequest marks an expansion request as acknowledged or dismissed.
func (db *DB) ResolvePIExpansionRequest(ctx context.Context, requestID, status, resolvedBy string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE pi_expansion_requests
		SET status = ?, resolved_at = ?, resolved_by = ?
		WHERE id = ? AND status = 'pending'`,
		status, time.Now().Unix(), resolvedBy, requestID,
	)
	if err != nil {
		return fmt.Errorf("db: resolve pi expansion request: %w", err)
	}
	return requireOneRow(res, "pi_expansion_requests", requestID)
}

// ─── PI auto-approve config ───────────────────────────────────────────────────

// GetPIAutoApprove returns true when CLUSTR_PI_AUTO_APPROVE is set or the DB flag is 1.
func (db *DB) GetPIAutoApprove(ctx context.Context) bool {
	var flag int
	_ = db.sql.QueryRowContext(ctx,
		`SELECT pi_auto_approve FROM portal_config WHERE id = 1`,
	).Scan(&flag)
	return flag == 1
}

// SetPIAutoApprove updates the PI auto-approve flag in portal_config.
func (db *DB) SetPIAutoApprove(ctx context.Context, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE portal_config SET pi_auto_approve = ?, updated_at = ? WHERE id = 1`,
		val, time.Now().Unix(),
	)
	return err
}

// IsNodeGroupOwnedByPI returns true if the group's pi_user_id matches piUserID.
func (db *DB) IsNodeGroupOwnedByPI(ctx context.Context, groupID, piUserID string) (bool, error) {
	var ownerID sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT pi_user_id FROM node_groups WHERE id = ?`, groupID,
	).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: check pi group ownership: %w", err)
	}
	return ownerID.Valid && ownerID.String == piUserID, nil
}
