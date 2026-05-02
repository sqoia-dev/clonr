package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// ─── racks CRUD (#149) ────────────────────────────────────────────────────────

// CreateRack inserts a new rack row.
func (db *DB) CreateRack(ctx context.Context, r api.Rack) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO racks (id, name, height_u, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, r.ID, r.Name, r.HeightU, r.CreatedAt.Unix(), r.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("db: create rack: %w", err)
	}
	return nil
}

// GetRack returns a rack by ID. Returns api.ErrNotFound when absent.
func (db *DB) GetRack(ctx context.Context, id string) (api.Rack, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, height_u, created_at, updated_at
		FROM racks WHERE id = ?
	`, id)
	return scanRack(row)
}

// ListRacks returns all racks ordered by name.
func (db *DB) ListRacks(ctx context.Context) ([]api.Rack, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, height_u, created_at, updated_at
		FROM racks ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list racks: %w", err)
	}
	defer rows.Close()

	var racks []api.Rack
	for rows.Next() {
		r, err := scanRack(rows)
		if err != nil {
			return nil, err
		}
		racks = append(racks, r)
	}
	return racks, rows.Err()
}

// UpdateRack updates the name and/or height_u of an existing rack.
func (db *DB) UpdateRack(ctx context.Context, id, name string, heightU int) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE racks SET name = ?, height_u = ?, updated_at = ? WHERE id = ?
	`, name, heightU, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("db: update rack: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// DeleteRack removes a rack by ID. Cascades to node_rack_position via FK.
func (db *DB) DeleteRack(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM racks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete rack: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// ─── node_rack_position ───────────────────────────────────────────────────────

// SetNodeRackPosition upserts the rack position for a node.
func (db *DB) SetNodeRackPosition(ctx context.Context, pos api.NodeRackPosition) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO node_rack_position (node_id, rack_id, slot_u, height_u)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			rack_id  = excluded.rack_id,
			slot_u   = excluded.slot_u,
			height_u = excluded.height_u
	`, pos.NodeID, pos.RackID, pos.SlotU, pos.HeightU)
	if err != nil {
		return fmt.Errorf("db: set node rack position: %w", err)
	}
	return nil
}

// DeleteNodeRackPosition removes a node's rack position assignment.
func (db *DB) DeleteNodeRackPosition(ctx context.Context, nodeID string) error {
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM node_rack_position WHERE node_id = ?
	`, nodeID)
	if err != nil {
		return fmt.Errorf("db: delete node rack position: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}

// ListPositionsByRack returns all node_rack_position rows for the given rack ID.
func (db *DB) ListPositionsByRack(ctx context.Context, rackID string) ([]api.NodeRackPosition, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT node_id, rack_id, slot_u, height_u
		FROM node_rack_position WHERE rack_id = ?
		ORDER BY slot_u ASC
	`, rackID)
	if err != nil {
		return nil, fmt.Errorf("db: list positions by rack: %w", err)
	}
	defer rows.Close()

	var positions []api.NodeRackPosition
	for rows.Next() {
		var p api.NodeRackPosition
		if err := rows.Scan(&p.NodeID, &p.RackID, &p.SlotU, &p.HeightU); err != nil {
			return nil, fmt.Errorf("db: scan node rack position: %w", err)
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

// ListNodeIDsByRackNames returns all node IDs for nodes assigned to any of the
// named racks. Used by the selector to resolve --racks.
func (db *DB) ListNodeIDsByRackNames(ctx context.Context, rackNames []string) ([]string, error) {
	if len(rackNames) == 0 {
		return nil, nil
	}

	// Build the IN clause dynamically — rackNames is operator-supplied but
	// bounded (comma-separated CLI input), so a simple loop is fine here.
	placeholders := make([]interface{}, len(rackNames))
	inClause := ""
	for i, name := range rackNames {
		placeholders[i] = name
		if i > 0 {
			inClause += ","
		}
		inClause += "?"
	}

	query := `
		SELECT nrp.node_id
		FROM node_rack_position nrp
		JOIN racks r ON r.id = nrp.rack_id
		WHERE r.name IN (` + inClause + `)
		ORDER BY nrp.node_id ASC
	`
	rows, err := db.sql.QueryContext(ctx, query, placeholders...)
	if err != nil {
		return nil, fmt.Errorf("db: list nodes by rack names: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scan node id by rack: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── scan helpers ─────────────────────────────────────────────────────────────

type rackScanner interface {
	Scan(dest ...any) error
}

func scanRack(s rackScanner) (api.Rack, error) {
	var (
		r             api.Rack
		createdAtUnix int64
		updatedAtUnix int64
	)
	err := s.Scan(&r.ID, &r.Name, &r.HeightU, &createdAtUnix, &updatedAtUnix)
	if err == sql.ErrNoRows {
		return api.Rack{}, api.ErrNotFound
	}
	if err != nil {
		return api.Rack{}, fmt.Errorf("db: scan rack: %w", err)
	}
	r.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	r.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()
	return r, nil
}
