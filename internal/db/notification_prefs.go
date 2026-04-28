package db

// notification_prefs DB layer — Sprint E (E4, CF-11/CF-15 enhancements).
//
// Per-user, per-event-type notification preferences.
// The Notifier uses these to decide whether to send immediately, queue for digest,
// or skip entirely.

import (
	"context"
	"fmt"
	"time"
)

// NotificationPref stores one user's delivery preference for one event type.
type NotificationPref struct {
	UserID       string
	EventType    string
	DeliveryMode string // immediate | hourly | daily | weekly | disabled
	Language     string // 'en' in v1.4; scaffold for future i18n
	UpdatedAt    time.Time
}

// GetNotificationPref returns the delivery mode for a user+event combination.
// Falls back to the global event default, then to 'daily' if no default found.
func (db *DB) GetNotificationPref(ctx context.Context, userID, eventType string) (string, error) {
	// Per-user preference.
	var mode string
	err := db.sql.QueryRowContext(ctx, `
		SELECT delivery_mode FROM user_notification_prefs
		WHERE user_id = ? AND event_type = ?
	`, userID, eventType).Scan(&mode)
	if err == nil {
		return mode, nil
	}

	// Global event default.
	err = db.sql.QueryRowContext(ctx, `
		SELECT delivery_mode FROM notification_event_defaults WHERE event_type = ?
	`, eventType).Scan(&mode)
	if err == nil {
		return mode, nil
	}

	// Fallback: daily for unknown event types.
	return "daily", nil
}

// ListUserNotificationPrefs returns all preference rows for a user.
func (db *DB) ListUserNotificationPrefs(ctx context.Context, userID string) ([]NotificationPref, error) {
	// Return the effective preference per known event type:
	// user-specific row if it exists, otherwise the global default.
	rows, err := db.sql.QueryContext(ctx, `
		SELECT
			ned.event_type,
			COALESCE(unp.delivery_mode, ned.delivery_mode) AS delivery_mode,
			COALESCE(unp.language, 'en')                   AS language,
			COALESCE(unp.updated_at, 0)                    AS updated_at
		FROM notification_event_defaults ned
		LEFT JOIN user_notification_prefs unp
		    ON unp.user_id = ? AND unp.event_type = ned.event_type
		ORDER BY ned.event_type
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list user notification prefs: %w", err)
	}
	defer rows.Close()

	var out []NotificationPref
	for rows.Next() {
		var p NotificationPref
		var updatedAt int64
		p.UserID = userID
		if err := rows.Scan(&p.EventType, &p.DeliveryMode, &p.Language, &updatedAt); err != nil {
			return nil, fmt.Errorf("db: scan notification pref: %w", err)
		}
		p.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetNotificationPref upserts a per-user preference.
func (db *DB) SetNotificationPref(ctx context.Context, userID, eventType, deliveryMode string) error {
	validModes := map[string]bool{
		"immediate": true, "hourly": true, "daily": true, "weekly": true, "disabled": true,
	}
	if !validModes[deliveryMode] {
		return fmt.Errorf("db: invalid delivery mode %q", deliveryMode)
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO user_notification_prefs (user_id, event_type, delivery_mode, language, updated_at)
		VALUES (?, ?, ?, 'en', ?)
		ON CONFLICT(user_id, event_type) DO UPDATE SET
		    delivery_mode = excluded.delivery_mode,
		    updated_at    = excluded.updated_at
	`, userID, eventType, deliveryMode, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("db: set notification pref: %w", err)
	}
	return nil
}

// ResetNotificationPrefs deletes all per-user overrides (reverts to defaults).
func (db *DB) ResetNotificationPrefs(ctx context.Context, userID string) error {
	_, err := db.sql.ExecContext(ctx, `
		DELETE FROM user_notification_prefs WHERE user_id = ?
	`, userID)
	if err != nil {
		return fmt.Errorf("db: reset notification prefs: %w", err)
	}
	return nil
}

// DigestQueueEntry is one pending digest notification.
type DigestQueueEntry struct {
	ID             string
	UserID         string
	EventType      string
	RecipientEmail string
	Subject        string
	BodyText       string
	BodyHTML       string
	ScheduledFor   time.Time
	CreatedAt      time.Time
}

// EnqueueDigest adds an email to the digest queue for later batched delivery.
func (db *DB) EnqueueDigest(ctx context.Context, entry *DigestQueueEntry) error {
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO notification_digest_queue
		    (id, user_id, event_type, recipient_email, subject, body_text, body_html, scheduled_for, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID, entry.UserID, entry.EventType, entry.RecipientEmail,
		entry.Subject, entry.BodyText, entry.BodyHTML,
		entry.ScheduledFor.Unix(), time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: enqueue digest: %w", err)
	}
	return nil
}

// PollDigestQueue returns all digest entries scheduled at or before the given time.
func (db *DB) PollDigestQueue(ctx context.Context, before time.Time) ([]DigestQueueEntry, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, user_id, event_type, recipient_email, subject, body_text, body_html, scheduled_for, created_at
		FROM notification_digest_queue
		WHERE scheduled_for <= ?
		ORDER BY user_id, event_type, created_at
	`, before.Unix())
	if err != nil {
		return nil, fmt.Errorf("db: poll digest queue: %w", err)
	}
	defer rows.Close()

	var out []DigestQueueEntry
	for rows.Next() {
		var e DigestQueueEntry
		var scheduledFor, createdAt int64
		if err := rows.Scan(&e.ID, &e.UserID, &e.EventType, &e.RecipientEmail,
			&e.Subject, &e.BodyText, &e.BodyHTML, &scheduledFor, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan digest entry: %w", err)
		}
		e.ScheduledFor = time.Unix(scheduledFor, 0)
		e.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteDigestEntries removes delivered digest queue rows by ID.
func (db *DB) DeleteDigestEntries(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// Build IN clause.
	placeholders := ""
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}
	_, err := db.sql.ExecContext(ctx, `DELETE FROM notification_digest_queue WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return fmt.Errorf("db: delete digest entries: %w", err)
	}
	return nil
}
