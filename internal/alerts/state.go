package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sqoia-dev/clustr/internal/db"
)

// AlertState represents the lifecycle state of an alert.
const (
	StateFiring   = "firing"
	StateResolved = "resolved"
)

// Alert is the server-side representation of a fired or resolved alert.
// This is the shape returned by GET /api/v1/alerts.
type Alert struct {
	ID           int64              `json:"id"`
	RuleName     string             `json:"rule_name"`
	NodeID       string             `json:"node_id"`
	Sensor       string             `json:"sensor"`
	Labels       map[string]string  `json:"labels,omitempty"`
	Severity     string             `json:"severity"`
	State        string             `json:"state"`
	FiredAt      time.Time          `json:"fired_at"`
	ResolvedAt   *time.Time         `json:"resolved_at,omitempty"`
	LastValue    float64            `json:"last_value"`
	ThresholdOp  string             `json:"threshold_op"`
	ThresholdVal float64            `json:"threshold_val"`
}

// alertStateKey uniquely identifies an active alert instance.
// (rule_name, node_id, sensor, labels_json) is the dedup key for the
// currently firing alert — we hold one active per group, not one per tick.
type alertStateKey struct {
	ruleName   string
	nodeID     string
	sensor     string
	labelsJSON string // canonical JSON, "" for no labels
}

// activeAlert is the in-memory record of a currently firing alert.
type activeAlert struct {
	dbID    int64
	key     alertStateKey
	firedAt time.Time
}

// StateStore holds the in-memory active-alert cache, backed by the alerts table.
// All methods are called from a single goroutine (the engine tick) so no
// locking is required.
type StateStore struct {
	db     *db.DB
	active map[alertStateKey]*activeAlert
}

// NewStateStore creates a StateStore and loads currently-firing alerts from the
// database into the in-memory cache so the engine survives server restarts.
func NewStateStore(database *db.DB) (*StateStore, error) {
	s := &StateStore{
		db:     database,
		active: make(map[alertStateKey]*activeAlert),
	}
	if err := s.loadActive(context.Background()); err != nil {
		return nil, fmt.Errorf("alerts: state store: %w", err)
	}
	return s, nil
}

// loadActive reads all firing rows from the alerts table and populates the
// in-memory cache.  Called once on startup.
func (s *StateStore) loadActive(ctx context.Context) error {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, rule_name, node_id, sensor, labels_json, severity, fired_at, last_value, threshold_op, threshold_val
		FROM alerts
		WHERE state = 'firing'
	`)
	if err != nil {
		return fmt.Errorf("load active: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id                       int64
			ruleName, nodeID, sensor string
			labelsJSON               sql.NullString
			severity                 string
			firedAtUnix              int64
			lastValue                float64
			thresholdOp              string
			thresholdVal             float64
		)
		if err := rows.Scan(&id, &ruleName, &nodeID, &sensor,
			&labelsJSON, &severity, &firedAtUnix,
			&lastValue, &thresholdOp, &thresholdVal); err != nil {
			return fmt.Errorf("load active: scan: %w", err)
		}
		lj := ""
		if labelsJSON.Valid {
			lj = labelsJSON.String
		}
		key := alertStateKey{
			ruleName:   ruleName,
			nodeID:     nodeID,
			sensor:     sensor,
			labelsJSON: lj,
		}
		s.active[key] = &activeAlert{
			dbID:    id,
			key:     key,
			firedAt: time.Unix(firedAtUnix, 0).UTC(),
		}
	}
	return rows.Err()
}

// IsActive returns true if the given key has an active firing alert.
func (s *StateStore) IsActive(key alertStateKey) bool {
	_, ok := s.active[key]
	return ok
}

// Fire persists a new alert row and registers it as active in the cache.
// Returns the new Alert for dispatch.
func (s *StateStore) Fire(ctx context.Context, r *Rule, nodeID string, labels map[string]string, lastValue float64) (*Alert, error) {
	lj := labelsToJSON(labels)
	key := alertStateKey{
		ruleName:   r.Name,
		nodeID:     nodeID,
		sensor:     r.Sensor,
		labelsJSON: lj,
	}

	now := time.Now().UTC()
	var ljNull sql.NullString
	if lj != "" {
		ljNull = sql.NullString{String: lj, Valid: true}
	}

	res, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO alerts (rule_name, node_id, sensor, labels_json, severity, state, fired_at, last_value, threshold_op, threshold_val)
		VALUES (?, ?, ?, ?, ?, 'firing', ?, ?, ?, ?)
	`, r.Name, nodeID, r.Sensor, ljNull, r.Severity,
		now.Unix(), lastValue, string(r.Threshold.Op), r.Threshold.Value)
	if err != nil {
		return nil, fmt.Errorf("alerts: fire: insert: %w", err)
	}
	id, _ := res.LastInsertId()

	s.active[key] = &activeAlert{dbID: id, key: key, firedAt: now}

	alert := &Alert{
		ID:           id,
		RuleName:     r.Name,
		NodeID:       nodeID,
		Sensor:       r.Sensor,
		Labels:       labels,
		Severity:     r.Severity,
		State:        StateFiring,
		FiredAt:      now,
		LastValue:    lastValue,
		ThresholdOp:  string(r.Threshold.Op),
		ThresholdVal: r.Threshold.Value,
	}
	return alert, nil
}

// Resolve marks the active alert as resolved, updates the DB, and removes it
// from the in-memory cache.  Returns the resolved Alert for dispatch.
func (s *StateStore) Resolve(ctx context.Context, key alertStateKey, lastValue float64) (*Alert, error) {
	aa, ok := s.active[key]
	if !ok {
		return nil, nil // not active; nothing to resolve
	}

	now := time.Now().UTC()
	_, err := s.db.SQL().ExecContext(ctx, `
		UPDATE alerts SET state = 'resolved', resolved_at = ?, last_value = ?
		WHERE id = ?
	`, now.Unix(), lastValue, aa.dbID)
	if err != nil {
		return nil, fmt.Errorf("alerts: resolve: update: %w", err)
	}
	delete(s.active, key)

	// Fetch the full row to build the response.
	return s.fetchByID(ctx, aa.dbID)
}

// UpdateLastValue updates the last_value for an active alert (no state change).
func (s *StateStore) UpdateLastValue(ctx context.Context, key alertStateKey, lastValue float64) {
	aa, ok := s.active[key]
	if !ok {
		return
	}
	// Best-effort; ignore error.
	_, _ = s.db.SQL().ExecContext(ctx, `UPDATE alerts SET last_value = ? WHERE id = ?`, lastValue, aa.dbID)
}

// fetchByID loads a single alert row by its DB ID.
func (s *StateStore) fetchByID(ctx context.Context, id int64) (*Alert, error) {
	row := s.db.SQL().QueryRowContext(ctx, `
		SELECT id, rule_name, node_id, sensor, labels_json, severity, state,
		       fired_at, resolved_at, last_value, threshold_op, threshold_val
		FROM alerts WHERE id = ?
	`, id)
	return scanAlert(row)
}

// QueryActive returns all currently firing alerts.
func (s *StateStore) QueryActive(ctx context.Context) ([]Alert, error) {
	return s.queryAlerts(ctx, `SELECT id, rule_name, node_id, sensor, labels_json, severity, state,
		fired_at, resolved_at, last_value, threshold_op, threshold_val
		FROM alerts WHERE state = 'firing' ORDER BY fired_at DESC`)
}

// QueryRecent returns alerts resolved within the last 24 hours.
func (s *StateStore) QueryRecent(ctx context.Context) ([]Alert, error) {
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	return s.queryAlerts(ctx, `SELECT id, rule_name, node_id, sensor, labels_json, severity, state,
		fired_at, resolved_at, last_value, threshold_op, threshold_val
		FROM alerts WHERE state = 'resolved' AND resolved_at >= ?
		ORDER BY resolved_at DESC`, cutoff)
}

// QueryFiltered applies the optional filters: severity (comma-separated),
// node_id, state.  Used by the GET /api/v1/alerts handler.
func (s *StateStore) QueryFiltered(ctx context.Context, severities []string, nodeID, state string) ([]Alert, error) {
	q := `SELECT id, rule_name, node_id, sensor, labels_json, severity, state,
		fired_at, resolved_at, last_value, threshold_op, threshold_val
		FROM alerts WHERE 1=1`
	var args []interface{}

	if len(severities) > 0 {
		placeholders := ""
		for i, sv := range severities {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, sv)
		}
		q += " AND severity IN (" + placeholders + ")"
	}
	if nodeID != "" {
		q += " AND node_id = ?"
		args = append(args, nodeID)
	}
	if state != "" {
		q += " AND state = ?"
		args = append(args, state)
	}
	q += " ORDER BY fired_at DESC LIMIT 1000"
	return s.queryAlerts(ctx, q, args...)
}

func (s *StateStore) queryAlerts(ctx context.Context, q string, args ...interface{}) ([]Alert, error) {
	rows, err := s.db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("alerts: query: %w", err)
	}
	defer rows.Close()

	var out []Alert
	for rows.Next() {
		var (
			id                              int64
			ruleName, nodeID, sensor        string
			labelsJSON                      sql.NullString
			severity, state                 string
			firedAtUnix                     int64
			resolvedAtUnix                  sql.NullInt64
			lastValue                       float64
			thresholdOp                     string
			thresholdVal                    float64
		)
		if err := rows.Scan(&id, &ruleName, &nodeID, &sensor,
			&labelsJSON, &severity, &state, &firedAtUnix, &resolvedAtUnix,
			&lastValue, &thresholdOp, &thresholdVal); err != nil {
			return nil, fmt.Errorf("alerts: scan: %w", err)
		}
		a := Alert{
			ID:           id,
			RuleName:     ruleName,
			NodeID:       nodeID,
			Sensor:       sensor,
			Severity:     severity,
			State:        state,
			FiredAt:      time.Unix(firedAtUnix, 0).UTC(),
			LastValue:    lastValue,
			ThresholdOp:  thresholdOp,
			ThresholdVal: thresholdVal,
		}
		if labelsJSON.Valid && labelsJSON.String != "" {
			if err := json.Unmarshal([]byte(labelsJSON.String), &a.Labels); err != nil {
				return nil, fmt.Errorf("alerts: unmarshal labels: %w", err)
			}
		}
		if resolvedAtUnix.Valid {
			t := time.Unix(resolvedAtUnix.Int64, 0).UTC()
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─── helper row scanner ───────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanAlert(row rowScanner) (*Alert, error) {
	var (
		id                              int64
		ruleName, nodeID, sensor        string
		labelsJSON                      sql.NullString
		severity, state                 string
		firedAtUnix                     int64
		resolvedAtUnix                  sql.NullInt64
		lastValue                       float64
		thresholdOp                     string
		thresholdVal                    float64
	)
	if err := row.Scan(&id, &ruleName, &nodeID, &sensor,
		&labelsJSON, &severity, &state, &firedAtUnix, &resolvedAtUnix,
		&lastValue, &thresholdOp, &thresholdVal); err != nil {
		return nil, fmt.Errorf("alerts: scanAlert: %w", err)
	}
	a := &Alert{
		ID:           id,
		RuleName:     ruleName,
		NodeID:       nodeID,
		Sensor:       sensor,
		Severity:     severity,
		State:        state,
		FiredAt:      time.Unix(firedAtUnix, 0).UTC(),
		LastValue:    lastValue,
		ThresholdOp:  thresholdOp,
		ThresholdVal: thresholdVal,
	}
	if labelsJSON.Valid && labelsJSON.String != "" {
		if err := json.Unmarshal([]byte(labelsJSON.String), &a.Labels); err != nil {
			return nil, fmt.Errorf("alerts: scanAlert: unmarshal labels: %w", err)
		}
	}
	if resolvedAtUnix.Valid {
		t := time.Unix(resolvedAtUnix.Int64, 0).UTC()
		a.ResolvedAt = &t
	}
	return a, nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// labelsToJSON returns a stable JSON encoding of labels, or "" for nil/empty.
func labelsToJSON(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return ""
	}
	return string(b)
}
