package db

import (
	"context"
	"fmt"
	"time"
)

// Publication represents an academic publication linked to a NodeGroup.
type Publication struct {
	ID              string
	NodeGroupID     string
	NodeGroupName   string // populated by joins
	DOI             string
	Title           string
	Authors         string
	Journal         string
	Year            int
	CreatedByUserID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CreatePublication inserts a new publication record.
func (db *DB) CreatePublication(ctx context.Context, p Publication) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO publications
			(id, node_group_id, doi, title, authors, journal, year,
			 created_by_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.NodeGroupID, p.DOI, p.Title, p.Authors, p.Journal, p.Year,
		p.CreatedByUserID, now, now)
	if err != nil {
		return fmt.Errorf("db: create publication: %w", err)
	}
	return nil
}

// GetPublication returns a single publication by ID.
func (db *DB) GetPublication(ctx context.Context, id string) (Publication, error) {
	var p Publication
	var createdAt, updatedAt int64
	err := db.sql.QueryRowContext(ctx, `
		SELECT p.id, p.node_group_id, ng.name, p.doi, p.title, p.authors, p.journal, p.year,
		       p.created_by_user_id, p.created_at, p.updated_at
		FROM publications p
		LEFT JOIN node_groups ng ON ng.id = p.node_group_id
		WHERE p.id = ?
	`, id).Scan(&p.ID, &p.NodeGroupID, &p.NodeGroupName, &p.DOI, &p.Title, &p.Authors,
		&p.Journal, &p.Year, &p.CreatedByUserID, &createdAt, &updatedAt)
	if err != nil {
		return p, fmt.Errorf("db: get publication %s: %w", id, err)
	}
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return p, nil
}

// ListPublicationsByGroup returns all publications for a NodeGroup.
func (db *DB) ListPublicationsByGroup(ctx context.Context, nodeGroupID string) ([]Publication, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT p.id, p.node_group_id, ng.name, p.doi, p.title, p.authors, p.journal, p.year,
		       p.created_by_user_id, p.created_at, p.updated_at
		FROM publications p
		LEFT JOIN node_groups ng ON ng.id = p.node_group_id
		WHERE p.node_group_id = ?
		ORDER BY p.year DESC, p.created_at DESC
	`, nodeGroupID)
	if err != nil {
		return nil, fmt.Errorf("db: list publications by group: %w", err)
	}
	defer rows.Close()
	return scanPublications(rows)
}

// ListAllPublications returns all publications (director/admin view).
func (db *DB) ListAllPublications(ctx context.Context) ([]Publication, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT p.id, p.node_group_id, ng.name, p.doi, p.title, p.authors, p.journal, p.year,
		       p.created_by_user_id, p.created_at, p.updated_at
		FROM publications p
		LEFT JOIN node_groups ng ON ng.id = p.node_group_id
		ORDER BY p.year DESC, p.created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list all publications: %w", err)
	}
	defer rows.Close()
	return scanPublications(rows)
}

// UpdatePublication updates an existing publication.
func (db *DB) UpdatePublication(ctx context.Context, p Publication) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE publications SET doi=?, title=?, authors=?, journal=?, year=?, updated_at=?
		WHERE id=?
	`, p.DOI, p.Title, p.Authors, p.Journal, p.Year, time.Now().Unix(), p.ID)
	if err != nil {
		return fmt.Errorf("db: update publication: %w", err)
	}
	return nil
}

// DeletePublication removes a publication by ID.
func (db *DB) DeletePublication(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM publications WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete publication: %w", err)
	}
	return nil
}

// CountPublicationsByGroup returns the number of publications for a NodeGroup.
func (db *DB) CountPublicationsByGroup(ctx context.Context, nodeGroupID string) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM publications WHERE node_group_id = ?`, nodeGroupID,
	).Scan(&n)
	return n, err
}

type publicationScanner interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
}

func scanPublications(rows publicationScanner) ([]Publication, error) {
	var out []Publication
	for rows.Next() {
		var p Publication
		var createdAt, updatedAt int64
		if err := rows.Scan(&p.ID, &p.NodeGroupID, &p.NodeGroupName, &p.DOI, &p.Title, &p.Authors,
			&p.Journal, &p.Year, &p.CreatedByUserID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("db: scan publication: %w", err)
		}
		p.CreatedAt = time.Unix(createdAt, 0).UTC()
		p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
