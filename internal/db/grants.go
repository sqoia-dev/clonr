package db

import (
	"context"
	"fmt"
	"time"
)

// Grant represents a funding grant linked to a NodeGroup.
type Grant struct {
	ID              string
	NodeGroupID     string
	NodeGroupName   string // populated by joins
	Title           string
	FundingAgency   string
	GrantNumber     string
	Amount          string
	StartDate       string
	EndDate         string
	Status          string // active | no_cost_extension | expired | pending
	Notes           string
	CreatedByUserID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreateGrant inserts a new grant record.
func (db *DB) CreateGrant(ctx context.Context, g Grant) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO grants
			(id, node_group_id, title, funding_agency, grant_number, amount,
			 start_date, end_date, status, notes, created_by_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, g.ID, g.NodeGroupID, g.Title, g.FundingAgency, g.GrantNumber, g.Amount,
		g.StartDate, g.EndDate, g.Status, g.Notes, g.CreatedByUserID, now, now)
	if err != nil {
		return fmt.Errorf("db: create grant: %w", err)
	}
	return nil
}

// GetGrant returns a single grant by ID.
func (db *DB) GetGrant(ctx context.Context, id string) (Grant, error) {
	var g Grant
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx, `
		SELECT g.id, g.node_group_id, ng.name, g.title, g.funding_agency, g.grant_number,
		       g.amount, g.start_date, g.end_date, g.status, g.notes,
		       g.created_by_user_id, g.created_at, g.updated_at
		FROM grants g
		LEFT JOIN node_groups ng ON ng.id = g.node_group_id
		WHERE g.id = ?
	`, id).Scan(&g.ID, &g.NodeGroupID, &g.NodeGroupName, &g.Title, &g.FundingAgency,
		&g.GrantNumber, &g.Amount, &g.StartDate, &g.EndDate, &g.Status, &g.Notes,
		&g.CreatedByUserID, &createdAt, &updatedAt)
	if err != nil {
		return g, fmt.Errorf("db: get grant %s: %w", id, err)
	}
	g.CreatedAt = time.Unix(createdAt, 0).UTC()
	g.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return g, nil
}

// ListGrantsByGroup returns all grants for a NodeGroup.
func (db *DB) ListGrantsByGroup(ctx context.Context, nodeGroupID string) ([]Grant, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT g.id, g.node_group_id, ng.name, g.title, g.funding_agency, g.grant_number,
		       g.amount, g.start_date, g.end_date, g.status, g.notes,
		       g.created_by_user_id, g.created_at, g.updated_at
		FROM grants g
		LEFT JOIN node_groups ng ON ng.id = g.node_group_id
		WHERE g.node_group_id = ?
		ORDER BY g.created_at DESC
	`, nodeGroupID)
	if err != nil {
		return nil, fmt.Errorf("db: list grants by group: %w", err)
	}
	defer rows.Close()
	return scanGrants(rows)
}

// ListAllGrants returns all grants (director/admin view).
func (db *DB) ListAllGrants(ctx context.Context) ([]Grant, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT g.id, g.node_group_id, ng.name, g.title, g.funding_agency, g.grant_number,
		       g.amount, g.start_date, g.end_date, g.status, g.notes,
		       g.created_by_user_id, g.created_at, g.updated_at
		FROM grants g
		LEFT JOIN node_groups ng ON ng.id = g.node_group_id
		ORDER BY g.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list all grants: %w", err)
	}
	defer rows.Close()
	return scanGrants(rows)
}

// UpdateGrant updates an existing grant.
func (db *DB) UpdateGrant(ctx context.Context, g Grant) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE grants SET title=?, funding_agency=?, grant_number=?, amount=?,
		  start_date=?, end_date=?, status=?, notes=?, updated_at=?
		WHERE id=?
	`, g.Title, g.FundingAgency, g.GrantNumber, g.Amount,
		g.StartDate, g.EndDate, g.Status, g.Notes, time.Now().Unix(), g.ID)
	if err != nil {
		return fmt.Errorf("db: update grant: %w", err)
	}
	return nil
}

// DeleteGrant removes a grant by ID.
func (db *DB) DeleteGrant(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM grants WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete grant: %w", err)
	}
	return nil
}

// CountGrantsByGroup returns the number of grants for a NodeGroup.
func (db *DB) CountGrantsByGroup(ctx context.Context, nodeGroupID string) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM grants WHERE node_group_id = ?`, nodeGroupID,
	).Scan(&n)
	return n, err
}

func scanGrants(rows interface{ Next() bool; Scan(...interface{}) error; Err() error }) ([]Grant, error) {
	var out []Grant
	for rows.Next() {
		var g Grant
		var createdAt, updatedAt int64
		if err := rows.Scan(&g.ID, &g.NodeGroupID, &g.NodeGroupName, &g.Title, &g.FundingAgency,
			&g.GrantNumber, &g.Amount, &g.StartDate, &g.EndDate, &g.Status, &g.Notes,
			&g.CreatedByUserID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("db: scan grant: %w", err)
		}
		g.CreatedAt = time.Unix(createdAt, 0).UTC()
		g.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}
