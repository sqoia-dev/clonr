// Package db — node_config_history.go: S5-12 node configuration change history.
//
// Every mutable field change written by UpdateNodeConfig is recorded here so
// auditors can reconstruct who changed what and when. Rows are append-only;
// callers use RecordNodeConfigChange to insert and ListNodeConfigHistory to page.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// NodeConfigHistoryRow represents a single field-level change on a node config.
type NodeConfigHistoryRow struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	ActorLabel string `json:"actor_label"`
	ChangedAt  int64  `json:"changed_at"` // Unix timestamp (seconds UTC)
	FieldName  string `json:"field_name"`
	OldValue   string `json:"old_value"`
	NewValue   string `json:"new_value"`
}

// RecordNodeConfigChanges inserts one row per changed field into node_config_history.
// Callers pass a map of fieldName → (oldValue, newValue). Values are JSON-encoded
// strings or bare string values. Skips fields where old == new.
// Actor label format: "user:<id>" or "key:<label>" — same as audit_log.
func (db *DB) RecordNodeConfigChanges(ctx context.Context, nodeID, actorLabel string, changes map[string][2]string) error {
	if len(changes) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: node_config_history: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO node_config_history(id, node_id, actor_label, changed_at, field_name, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("db: node_config_history: prepare: %w", err)
	}
	defer stmt.Close()

	for field, pair := range changes {
		if pair[0] == pair[1] {
			continue // no change
		}
		_, err := stmt.ExecContext(ctx,
			uuid.New().String(), nodeID, actorLabel, now, field, pair[0], pair[1])
		if err != nil {
			return fmt.Errorf("db: node_config_history: insert %s: %w", field, err)
		}
	}
	return tx.Commit()
}

// ListNodeConfigHistory returns paginated config change history for a node.
// Results are newest-first. page is 1-based; perPage defaults to 50.
func (db *DB) ListNodeConfigHistory(ctx context.Context, nodeID string, page, perPage int) ([]NodeConfigHistoryRow, int, error) {
	if perPage <= 0 {
		perPage = 50
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * perPage

	var total int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_config_history WHERE node_id = ?`, nodeID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: node_config_history count: %w", err)
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, node_id, actor_label, changed_at, field_name, old_value, new_value
		FROM node_config_history
		WHERE node_id = ?
		ORDER BY changed_at DESC, id DESC
		LIMIT ? OFFSET ?`, nodeID, perPage, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: node_config_history list: %w", err)
	}
	defer rows.Close()

	var out []NodeConfigHistoryRow
	for rows.Next() {
		var r NodeConfigHistoryRow
		if err := rows.Scan(&r.ID, &r.NodeID, &r.ActorLabel, &r.ChangedAt, &r.FieldName, &r.OldValue, &r.NewValue); err != nil {
			return nil, 0, fmt.Errorf("db: node_config_history scan: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// diffNodeConfigFields computes a change map between two JSON-serialisable node
// config snapshots (represented as map[string]interface{}).
// Used by the nodes handler to produce the field-level diff fed to RecordNodeConfigChanges.
func DiffNodeConfigFields(before, after interface{}) (map[string][2]string, error) {
	bRaw, err := json.Marshal(before)
	if err != nil {
		return nil, fmt.Errorf("diff: marshal before: %w", err)
	}
	aRaw, err := json.Marshal(after)
	if err != nil {
		return nil, fmt.Errorf("diff: marshal after: %w", err)
	}

	var bMap, aMap map[string]interface{}
	if err := json.Unmarshal(bRaw, &bMap); err != nil {
		return nil, fmt.Errorf("diff: unmarshal before: %w", err)
	}
	if err := json.Unmarshal(aRaw, &aMap); err != nil {
		return nil, fmt.Errorf("diff: unmarshal after: %w", err)
	}

	changes := map[string][2]string{}
	// Collect all keys from both maps.
	keys := map[string]bool{}
	for k := range bMap { keys[k] = true }
	for k := range aMap { keys[k] = true }

	// Skip fields that are always noisy or internal.
	skip := map[string]bool{
		"updated_at": true, "created_at": true,
		"bmc": true, "power_provider": true, // credentials — never diff raw
		"bmc_config_encrypted": true, "power_provider_encrypted": true,
		"hardware_profile": true, // large blob — not useful as diff
		"ib_config": true,
		"ldap_config": true, "network_config": true,
		"system_accounts": true, "slurm_config": true,
		"sudoers_config": true,
	}

	for k := range keys {
		if skip[k] {
			continue
		}
		bv, aOK := bMap[k]
		av, _ := aMap[k]

		bStr := jsonStr(bv)
		aStr := jsonStr(av)
		_ = aOK

		if bStr != aStr {
			changes[k] = [2]string{bStr, aStr}
		}
	}
	return changes, nil
}

func jsonStr(v interface{}) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	// Unquote simple strings for readability.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
