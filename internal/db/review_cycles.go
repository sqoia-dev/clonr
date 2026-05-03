package db

import (
	"context"
	"fmt"
	"time"
)

// ReviewCycle is one center-wide annual review cycle.
type ReviewCycle struct {
	ID          string
	Name        string
	Deadline    time.Time
	CreatedBy   string
	CreatedAt   time.Time
}

// ReviewResponse is one PI's response to a review cycle.
type ReviewResponse struct {
	ID          string
	CycleID     string
	NodeGroupID string
	GroupName   string // populated by joins
	PIUserID    string
	PIUsername  string // populated by joins
	Status      string // pending | affirmed | archive_requested | no_response
	Notes       string
	RespondedAt *time.Time
}

// CreateReviewCycle inserts a new review cycle.
func (db *DB) CreateReviewCycle(ctx context.Context, c ReviewCycle) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO review_cycles (id, name, deadline, created_by, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, c.ID, c.Name, c.Deadline.Unix(), c.CreatedBy, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("db: create review cycle: %w", err)
	}
	return nil
}

// GetReviewCycle returns a single review cycle by ID.
func (db *DB) GetReviewCycle(ctx context.Context, id string) (ReviewCycle, error) {
	var c ReviewCycle
	var deadline, createdAt int64
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, name, deadline, created_by, created_at FROM review_cycles WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &deadline, &c.CreatedBy, &createdAt)
	if err != nil {
		return c, fmt.Errorf("db: get review cycle %s: %w", id, err)
	}
	c.Deadline = time.Unix(deadline, 0).UTC()
	c.CreatedAt = time.Unix(createdAt, 0).UTC()
	return c, nil
}

// ListReviewCycles returns all review cycles, most recent first.
func (db *DB) ListReviewCycles(ctx context.Context) ([]ReviewCycle, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, name, deadline, created_by, created_at FROM review_cycles ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("db: list review cycles: %w", err)
	}
	defer rows.Close()
	var out []ReviewCycle
	for rows.Next() {
		var c ReviewCycle
		var deadline, createdAt int64
		if err := rows.Scan(&c.ID, &c.Name, &deadline, &c.CreatedBy, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan review cycle: %w", err)
		}
		c.Deadline = time.Unix(deadline, 0).UTC()
		c.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateReviewResponses creates pending response rows for all PI-owned NodeGroups.
// Called immediately after creating a review cycle.
func (db *DB) CreateReviewResponses(ctx context.Context, cycleID string) (int, error) {
	// Find all NodeGroups with a PI assigned.
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, pi_user_id FROM node_groups WHERE pi_user_id IS NOT NULL AND pi_user_id != ''`)
	if err != nil {
		return 0, fmt.Errorf("db: list pi groups for review: %w", err)
	}
	defer rows.Close()
	type groupPI struct{ groupID, piID string }
	var pairs []groupPI
	for rows.Next() {
		var gp groupPI
		if err := rows.Scan(&gp.groupID, &gp.piID); err != nil {
			return 0, err
		}
		pairs = append(pairs, gp)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	count := 0
	for _, gp := range pairs {
		id := fmt.Sprintf("rr-%d-%s-%s", time.Now().UnixNano(), cycleID[:8], gp.groupID[:8])
		_, err := db.sql.ExecContext(ctx, `
			INSERT OR IGNORE INTO review_responses
				(id, cycle_id, node_group_id, pi_user_id, status)
			VALUES (?, ?, ?, ?, 'pending')
		`, id, cycleID, gp.groupID, gp.piID)
		if err != nil {
			return count, fmt.Errorf("db: create review response: %w", err)
		}
		count++
	}
	return count, nil
}

// ListReviewResponsesByCycle returns all responses for a cycle with group/PI names.
func (db *DB) ListReviewResponsesByCycle(ctx context.Context, cycleID string) ([]ReviewResponse, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT rr.id, rr.cycle_id, rr.node_group_id, ng.name,
		       rr.pi_user_id, COALESCE(u.username,''),
		       rr.status, rr.notes, rr.responded_at
		FROM review_responses rr
		LEFT JOIN node_groups ng ON ng.id = rr.node_group_id
		LEFT JOIN users u ON u.id = rr.pi_user_id
		WHERE rr.cycle_id = ?
		ORDER BY ng.name ASC
	`, cycleID)
	if err != nil {
		return nil, fmt.Errorf("db: list review responses: %w", err)
	}
	defer rows.Close()
	return scanReviewResponses(rows)
}

// GetReviewResponseByGroupAndCycle returns the response for a specific group in a cycle.
func (db *DB) GetReviewResponseByGroupAndCycle(ctx context.Context, cycleID, groupID string) (ReviewResponse, error) {
	var rr ReviewResponse
	var respondedAt *int64
	err := db.sql.QueryRowContext(ctx, `
		SELECT rr.id, rr.cycle_id, rr.node_group_id, COALESCE(ng.name,''),
		       rr.pi_user_id, COALESCE(u.username,''),
		       rr.status, rr.notes, rr.responded_at
		FROM review_responses rr
		LEFT JOIN node_groups ng ON ng.id = rr.node_group_id
		LEFT JOIN users u ON u.id = rr.pi_user_id
		WHERE rr.cycle_id = ? AND rr.node_group_id = ?
	`, cycleID, groupID).Scan(
		&rr.ID, &rr.CycleID, &rr.NodeGroupID, &rr.GroupName,
		&rr.PIUserID, &rr.PIUsername,
		&rr.Status, &rr.Notes, &respondedAt,
	)
	if err != nil {
		return rr, fmt.Errorf("db: get review response: %w", err)
	}
	if respondedAt != nil {
		t := time.Unix(*respondedAt, 0).UTC()
		rr.RespondedAt = &t
	}
	return rr, nil
}

// SubmitReviewResponse records a PI's response to a review cycle.
func (db *DB) SubmitReviewResponse(ctx context.Context, cycleID, nodeGroupID, status, notes string) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		UPDATE review_responses SET status=?, notes=?, responded_at=?
		WHERE cycle_id=? AND node_group_id=?
	`, status, notes, now, cycleID, nodeGroupID)
	if err != nil {
		return fmt.Errorf("db: submit review response: %w", err)
	}
	return nil
}

// ListReviewResponsesByPI returns all review responses for groups owned by a PI.
func (db *DB) ListReviewResponsesByPI(ctx context.Context, piUserID string) ([]ReviewResponse, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT rr.id, rr.cycle_id, rr.node_group_id, COALESCE(ng.name,''),
		       rr.pi_user_id, COALESCE(u.username,''),
		       rr.status, rr.notes, rr.responded_at
		FROM review_responses rr
		LEFT JOIN node_groups ng ON ng.id = rr.node_group_id
		LEFT JOIN users u ON u.id = rr.pi_user_id
		WHERE rr.pi_user_id = ?
		ORDER BY rr.cycle_id DESC
	`, piUserID)
	if err != nil {
		return nil, fmt.Errorf("db: list review responses by pi: %w", err)
	}
	defer rows.Close()
	return scanReviewResponses(rows)
}

type reviewResponseScanner interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
}

func scanReviewResponses(rows reviewResponseScanner) ([]ReviewResponse, error) {
	var out []ReviewResponse
	for rows.Next() {
		var rr ReviewResponse
		var respondedAt *int64
		if err := rows.Scan(
			&rr.ID, &rr.CycleID, &rr.NodeGroupID, &rr.GroupName,
			&rr.PIUserID, &rr.PIUsername,
			&rr.Status, &rr.Notes, &respondedAt,
		); err != nil {
			return nil, fmt.Errorf("db: scan review response: %w", err)
		}
		if respondedAt != nil {
			t := time.Unix(*respondedAt, 0).UTC()
			rr.RespondedAt = &t
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}
