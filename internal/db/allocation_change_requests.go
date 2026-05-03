package db

// allocation_change_requests DB layer — Sprint E (E1, CF-20).
//
// PIs can submit change requests of several types; admin reviews and decides.
// Existing pi_expansion_requests rows are migrated into this table (migration 064).

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AllocationChangeRequest represents a PI-submitted request for an allocation change.
type AllocationChangeRequest struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"group_id"`
	ProjectName     string     `json:"group_name"`    // populated by join
	RequesterUserID string     `json:"pi_user_id"`
	RequesterName   string     `json:"pi_username"`   // populated by join
	RequestType     string     `json:"request_type"`  // add_member | remove_member | increase_resources | extend_duration | archive_project
	Payload         string     `json:"payload"`       // JSON blob
	Justification   string     `json:"justification"`
	Status          string     `json:"status"`        // pending | approved | denied | expired | withdrawn
	ReviewedBy      string     `json:"reviewed_by"`
	ReviewedByName  string     `json:"reviewed_by_name"` // populated by join
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
	ReviewNotes     string     `json:"review_notes"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

// ErrChangeRequestNotFound is returned when a change request row does not exist.
var ErrChangeRequestNotFound = fmt.Errorf("db: allocation change request not found")

const acrSelectBase = `
SELECT
    acr.id,
    acr.project_id,
    COALESCE(ng.name, '') AS project_name,
    acr.requester_user_id,
    COALESCE(ru.username, '') AS requester_name,
    acr.request_type,
    acr.payload,
    acr.justification,
    acr.status,
    COALESCE(acr.reviewed_by, '') AS reviewed_by,
    COALESCE(rv.username, '') AS reviewed_by_name,
    acr.reviewed_at,
    acr.review_notes,
    acr.created_at,
    acr.expires_at
FROM allocation_change_requests acr
LEFT JOIN node_groups ng   ON ng.id  = acr.project_id
LEFT JOIN users       ru   ON ru.id  = acr.requester_user_id
LEFT JOIN users       rv   ON rv.id  = acr.reviewed_by
`

func scanACR(row interface {
	Scan(dest ...any) error
}) (*AllocationChangeRequest, error) {
	var acr AllocationChangeRequest
	var reviewedAt, expiresAt sql.NullInt64
	err := row.Scan(
		&acr.ID,
		&acr.ProjectID,
		&acr.ProjectName,
		&acr.RequesterUserID,
		&acr.RequesterName,
		&acr.RequestType,
		&acr.Payload,
		&acr.Justification,
		&acr.Status,
		&acr.ReviewedBy,
		&acr.ReviewedByName,
		&reviewedAt,
		&acr.ReviewNotes,
		&acr.CreatedAt,
		&expiresAt,
	)
	if err != nil {
		return nil, err
	}
	if reviewedAt.Valid {
		t := time.Unix(reviewedAt.Int64, 0)
		acr.ReviewedAt = &t
	}
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0)
		acr.ExpiresAt = &t
	}
	return &acr, nil
}

// CreateAllocationChangeRequest inserts a new change request row.
func (db *DB) CreateAllocationChangeRequest(ctx context.Context, acr *AllocationChangeRequest) error {
	var expiresAt sql.NullInt64
	if acr.ExpiresAt != nil {
		expiresAt = sql.NullInt64{Int64: acr.ExpiresAt.Unix(), Valid: true}
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO allocation_change_requests
		    (id, project_id, requester_user_id, request_type, payload, justification,
		     status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)
	`,
		acr.ID, acr.ProjectID, acr.RequesterUserID,
		acr.RequestType, acr.Payload, acr.Justification,
		time.Now().Unix(), expiresAt,
	)
	if err != nil {
		return fmt.Errorf("db: create allocation change request: %w", err)
	}
	return nil
}

// GetAllocationChangeRequest returns one request by ID.
func (db *DB) GetAllocationChangeRequest(ctx context.Context, id string) (*AllocationChangeRequest, error) {
	q := acrSelectBase + ` WHERE acr.id = ?`
	row := db.sql.QueryRowContext(ctx, q, id)
	acr, err := scanACR(row)
	if err == sql.ErrNoRows {
		return nil, ErrChangeRequestNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get allocation change request: %w", err)
	}
	return acr, nil
}

// ListAllocationChangeRequests returns requests matching the given filters.
// If projectID is non-empty, scoped to that NodeGroup.
// If status is non-empty, filtered to that status.
func (db *DB) ListAllocationChangeRequests(ctx context.Context, projectID, status string, limit, offset int) ([]AllocationChangeRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	q := acrSelectBase + ` WHERE 1=1`
	args := []interface{}{}
	if projectID != "" {
		q += ` AND acr.project_id = ?`
		args = append(args, projectID)
	}
	if status != "" {
		q += ` AND acr.status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY acr.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list allocation change requests: %w", err)
	}
	defer rows.Close()

	var out []AllocationChangeRequest
	for rows.Next() {
		acr, err := scanACR(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan allocation change request: %w", err)
		}
		out = append(out, *acr)
	}
	return out, rows.Err()
}

// ListAllocationChangeRequestsByPI returns all requests submitted by a specific user.
func (db *DB) ListAllocationChangeRequestsByPI(ctx context.Context, piUserID string, limit, offset int) ([]AllocationChangeRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	q := acrSelectBase + `
		WHERE acr.requester_user_id = ?
		ORDER BY acr.created_at DESC
		LIMIT ? OFFSET ?
	`
	rows, err := db.sql.QueryContext(ctx, q, piUserID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: list allocation change requests by PI: %w", err)
	}
	defer rows.Close()

	var out []AllocationChangeRequest
	for rows.Next() {
		acr, err := scanACR(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan allocation change request: %w", err)
		}
		out = append(out, *acr)
	}
	return out, rows.Err()
}

// ReviewAllocationChangeRequest sets the status, reviewer, and notes on a request.
func (db *DB) ReviewAllocationChangeRequest(ctx context.Context, id, reviewerID, status, notes string) error {
	if status != "approved" && status != "denied" {
		return fmt.Errorf("db: invalid review status %q", status)
	}
	res, err := db.sql.ExecContext(ctx, `
		UPDATE allocation_change_requests
		SET status = ?, reviewed_by = ?, reviewed_at = ?, review_notes = ?
		WHERE id = ? AND status = 'pending'
	`, status, reviewerID, time.Now().Unix(), notes, id)
	if err != nil {
		return fmt.Errorf("db: review allocation change request: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: allocation change request %q not found or already reviewed", id)
	}
	return nil
}

// WithdrawAllocationChangeRequest lets the original requester cancel their request.
func (db *DB) WithdrawAllocationChangeRequest(ctx context.Context, id, requesterUserID string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE allocation_change_requests
		SET status = 'withdrawn'
		WHERE id = ? AND requester_user_id = ? AND status = 'pending'
	`, id, requesterUserID)
	if err != nil {
		return fmt.Errorf("db: withdraw allocation change request: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: change request %q not found, not pending, or not owned by requester", id)
	}
	return nil
}

// CountPendingAllocationChangeRequests returns the total number of pending requests,
// optionally scoped to a project.
func (db *DB) CountPendingAllocationChangeRequests(ctx context.Context, projectID string) (int, error) {
	q := `SELECT COUNT(*) FROM allocation_change_requests WHERE status = 'pending'`
	args := []interface{}{}
	if projectID != "" {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	var count int
	err := db.sql.QueryRowContext(ctx, q, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("db: count pending allocation change requests: %w", err)
	}
	return count, nil
}
