package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// Silence represents an active alert silence record.
// A silence suppresses dispatch for the matching (rule_name, node_id) pair
// until ExpiresAt. ExpiresAt == -1 means the silence never expires.
type Silence struct {
	ID        string  `json:"id"`
	RuleName  string  `json:"rule_name"`
	NodeID    *string `json:"node_id,omitempty"` // nil = global (all nodes)
	ExpiresAt int64   `json:"expires_at"`        // unix seconds; -1 = forever
	CreatedAt int64   `json:"created_at"`
	CreatedBy string  `json:"created_by,omitempty"`
}

// SilenceStore persists and queries alert silences.
// It is used by the engine to skip dispatch for silenced alerts and by the
// HTTP handlers to manage the silence lifecycle.
//
// THREAD-SAFETY: all methods are safe for concurrent use. They perform
// DB-level reads/writes only — no in-memory state.
type SilenceStore struct {
	db *db.DB
}

// NewSilenceStore creates a SilenceStore backed by the given database.
func NewSilenceStore(database *db.DB) *SilenceStore {
	return &SilenceStore{db: database}
}

// Create inserts a new silence record.
func (s *SilenceStore) Create(ctx context.Context, sil Silence) error {
	var nodeID sql.NullString
	if sil.NodeID != nil && *sil.NodeID != "" {
		nodeID = sql.NullString{String: *sil.NodeID, Valid: true}
	}
	_, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO alert_silences (id, rule_name, node_id, expires_at, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)
	`, sil.ID, sil.RuleName, nodeID, sil.ExpiresAt, sil.CreatedAt, sil.CreatedBy)
	if err != nil {
		return fmt.Errorf("silences: create: %w", err)
	}
	return nil
}

// Delete removes a silence by ID.
func (s *SilenceStore) Delete(ctx context.Context, id string) error {
	res, err := s.db.SQL().ExecContext(ctx, `DELETE FROM alert_silences WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("silences: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("silences: not found: %s", id)
	}
	return nil
}

// List returns all silences (including expired ones). Callers can filter by
// checking ExpiresAt themselves if they only want active silences.
func (s *SilenceStore) List(ctx context.Context) ([]Silence, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, rule_name, node_id, expires_at, created_at, created_by
		FROM alert_silences
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("silences: list: %w", err)
	}
	defer rows.Close()
	return scanSilences(rows)
}

// ListActive returns silences that are currently active (not yet expired).
func (s *SilenceStore) ListActive(ctx context.Context) ([]Silence, error) {
	now := time.Now().Unix()
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, rule_name, node_id, expires_at, created_at, created_by
		FROM alert_silences
		WHERE expires_at = -1 OR expires_at > ?
		ORDER BY created_at DESC
	`, now)
	if err != nil {
		return nil, fmt.Errorf("silences: list active: %w", err)
	}
	defer rows.Close()
	return scanSilences(rows)
}

// IsSilenced returns true if the given (ruleName, nodeID) pair is currently
// covered by an active silence. A global silence (node_id IS NULL) covers
// all nodes for that rule.
func (s *SilenceStore) IsSilenced(ctx context.Context, ruleName, nodeID string) (bool, error) {
	now := time.Now().Unix()
	var count int
	err := s.db.SQL().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alert_silences
		WHERE rule_name = ?
		  AND (node_id IS NULL OR node_id = ?)
		  AND (expires_at = -1 OR expires_at > ?)
	`, ruleName, nodeID, now).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("silences: check: %w", err)
	}
	return count > 0, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func scanSilences(rows *sql.Rows) ([]Silence, error) {
	var out []Silence
	for rows.Next() {
		var (
			id, ruleName  string
			nodeID        sql.NullString
			expiresAt     int64
			createdAt     int64
			createdBy     sql.NullString
		)
		if err := rows.Scan(&id, &ruleName, &nodeID, &expiresAt, &createdAt, &createdBy); err != nil {
			return nil, fmt.Errorf("silences: scan: %w", err)
		}
		sil := Silence{
			ID:        id,
			RuleName:  ruleName,
			ExpiresAt: expiresAt,
			CreatedAt: createdAt,
		}
		if nodeID.Valid {
			sil.NodeID = &nodeID.String
		}
		if createdBy.Valid {
			sil.CreatedBy = createdBy.String
		}
		out = append(out, sil)
	}
	return out, rows.Err()
}
