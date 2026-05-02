package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/sqoia-dev/clustr/internal/multicast"
)

// MulticastInsertSession inserts a new session row in state=staging.
func (db *DB) MulticastInsertSession(ctx context.Context, s multicast.Session) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO multicast_sessions
			(id, image_id, layout_id, state, multicast_group, sender_port,
			 rate_bps, started_at, fire_at, member_count, success_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		s.ID,
		s.ImageID,
		nullableString(s.LayoutID),
		string(multicast.StateStaging),
		s.MulticastGroup,
		s.SenderPort,
		s.RateBPS,
		s.StartedAt.Unix(),
		s.FireAt.Unix(),
		s.MemberCount,
		s.SuccessCount,
	)
	if err != nil {
		return fmt.Errorf("db: insert multicast session: %w", err)
	}
	return nil
}

// MulticastUpdateSessionState persists a state transition and optional metadata.
func (db *DB) MulticastUpdateSessionState(ctx context.Context, id string, state multicast.State, extra multicast.SessionUpdate) error {
	// Build the SET clause dynamically based on which extra fields are provided.
	query := `UPDATE multicast_sessions SET state = ?`
	args := []any{string(state)}

	if extra.TransmitStartedAt != nil {
		query += `, transmit_started_at = ?`
		args = append(args, extra.TransmitStartedAt.Unix())
	}
	if extra.CompletedAt != nil {
		query += `, completed_at = ?`
		args = append(args, extra.CompletedAt.Unix())
	}
	if extra.Error != "" {
		query += `, error = ?`
		args = append(args, extra.Error)
	}
	if extra.MemberCount != nil {
		query += `, member_count = ?`
		args = append(args, *extra.MemberCount)
	}
	if extra.SuccessCount != nil {
		query += `, success_count = ?`
		args = append(args, *extra.SuccessCount)
	}

	query += ` WHERE id = ?`
	args = append(args, id)

	_, err := db.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("db: update multicast session state: %w", err)
	}
	return nil
}

// MulticastInsertMember adds a member row for a node joining a session.
func (db *DB) MulticastInsertMember(ctx context.Context, m multicast.Member) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT OR IGNORE INTO multicast_session_members
			(session_id, node_id, joined_at)
		VALUES (?, ?, ?)
	`, m.SessionID, m.NodeID, m.JoinedAt.Unix())
	if err != nil {
		return fmt.Errorf("db: insert multicast member: %w", err)
	}
	return nil
}

// MulticastUpdateMember records per-node outcome / notification fields.
func (db *DB) MulticastUpdateMember(ctx context.Context, sessionID, nodeID string, u multicast.MemberUpdate) error {
	query := `UPDATE multicast_session_members SET`
	args := []any{}
	sep := " "

	if u.NotifiedAt != nil {
		query += sep + `notified_at = ?`
		args = append(args, u.NotifiedAt.Unix())
		sep = ", "
	}
	if u.FinishedAt != nil {
		query += sep + `finished_at = ?`
		args = append(args, u.FinishedAt.Unix())
		sep = ", "
	}
	if u.Outcome != "" {
		query += sep + `outcome = ?`
		args = append(args, string(u.Outcome))
	}

	if len(args) == 0 {
		return nil // nothing to update
	}
	query += ` WHERE session_id = ? AND node_id = ?`
	args = append(args, sessionID, nodeID)

	_, err := db.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("db: update multicast member: %w", err)
	}
	return nil
}

// MulticastListActive returns all non-terminal sessions (staging or transmitting).
// Used on serverd startup to recover orphaned sessions.
func (db *DB) MulticastListActive(ctx context.Context) ([]multicast.Session, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, image_id, COALESCE(layout_id,''), state, multicast_group,
		       sender_port, rate_bps, started_at, fire_at,
		       transmit_started_at, completed_at, COALESCE(error,''),
		       member_count, success_count
		FROM multicast_sessions
		WHERE state IN ('staging', 'transmitting')
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list active multicast sessions: %w", err)
	}
	defer rows.Close()

	var out []multicast.Session
	for rows.Next() {
		var s multicast.Session
		var stateStr string
		var transmitStartedAt, completedAt sql.NullInt64
		if err := rows.Scan(
			&s.ID, &s.ImageID, &s.LayoutID, &stateStr, &s.MulticastGroup,
			&s.SenderPort, &s.RateBPS,
			(*unixTime)(&s.StartedAt), (*unixTime)(&s.FireAt),
			&transmitStartedAt, &completedAt, &s.Error,
			&s.MemberCount, &s.SuccessCount,
		); err != nil {
			return nil, fmt.Errorf("db: scan multicast session: %w", err)
		}
		s.State = multicast.State(stateStr)
		if transmitStartedAt.Valid {
			t := time.Unix(transmitStartedAt.Int64, 0)
			s.TransmitStartedAt = &t
		}
		if completedAt.Valid {
			t := time.Unix(completedAt.Int64, 0)
			s.CompletedAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MulticastGetConfig reads the multicast_config table into a Config struct.
// Returns DefaultConfig() if the table is empty or missing expected keys.
func (db *DB) MulticastGetConfig(ctx context.Context) (multicast.Config, error) {
	cfg := multicast.DefaultConfig()

	rows, err := db.sql.QueryContext(ctx, `SELECT key, value FROM multicast_config`)
	if err != nil {
		return cfg, fmt.Errorf("db: read multicast_config: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			continue
		}
		switch key {
		case "enabled":
			cfg.Enabled = val == "true" || val == "1"
		case "window_seconds":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				cfg.WindowSeconds = n
			}
		case "threshold":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				cfg.Threshold = n
			}
		case "rate_bps":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n > 0 {
				cfg.RateBPS = n
			}
		case "group_base":
			if val != "" {
				cfg.GroupBase = val
			}
		}
	}
	return cfg, rows.Err()
}

// MulticastGetSession returns a single session by ID.
func (db *DB) MulticastGetSession(ctx context.Context, id string) (multicast.Session, error) {
	var s multicast.Session
	var stateStr string
	var transmitStartedAt, completedAt sql.NullInt64
	err := db.sql.QueryRowContext(ctx, `
		SELECT id, image_id, COALESCE(layout_id,''), state, multicast_group,
		       sender_port, rate_bps, started_at, fire_at,
		       transmit_started_at, completed_at, COALESCE(error,''),
		       member_count, success_count
		FROM multicast_sessions WHERE id = ?
	`, id).Scan(
		&s.ID, &s.ImageID, &s.LayoutID, &stateStr, &s.MulticastGroup,
		&s.SenderPort, &s.RateBPS,
		(*unixTime)(&s.StartedAt), (*unixTime)(&s.FireAt),
		&transmitStartedAt, &completedAt, &s.Error,
		&s.MemberCount, &s.SuccessCount,
	)
	if err != nil {
		return s, fmt.Errorf("db: get multicast session: %w", err)
	}
	s.State = multicast.State(stateStr)
	if transmitStartedAt.Valid {
		t := time.Unix(transmitStartedAt.Int64, 0)
		s.TransmitStartedAt = &t
	}
	if completedAt.Valid {
		t := time.Unix(completedAt.Int64, 0)
		s.CompletedAt = &t
	}
	return s, nil
}

// unixTime is a helper type for scanning Unix epoch integers into time.Time.
type unixTime time.Time

func (u *unixTime) Scan(v any) error {
	switch val := v.(type) {
	case int64:
		*u = unixTime(time.Unix(val, 0))
	case nil:
		*u = unixTime(time.Time{})
	default:
		return fmt.Errorf("unixTime: unexpected type %T", v)
	}
	return nil
}
