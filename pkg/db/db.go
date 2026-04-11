// Package db provides the SQLite persistence layer for clonr.
// It uses modernc.org/sqlite (pure-Go, CGO_ENABLED=0 compatible).
package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/sqoia-dev/clonr/pkg/api"
	_ "modernc.org/sqlite" // register "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps sql.DB with typed clonr operations.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies all pending migrations.
func Open(dbPath string) (*DB, error) {
	// WAL mode gives better concurrent read performance; journal_mode must be
	// set before any DDL runs.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000", dbPath)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", dbPath, err)
	}
	// SQLite handles concurrency via WAL; a single writer is fine.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: ping %s: %w", dbPath, err)
	}

	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	return db, nil
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	return db.sql.Close()
}

// migrate applies all SQL migration files in order. Each file is applied once;
// applied migrations are tracked in the schema_migrations table.
func (db *DB) migrate() error {
	// Ensure tracking table exists.
	if _, err := db.sql.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename to guarantee ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		var count int
		if err := db.sql.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, entry.Name(),
		).Scan(&count); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if count > 0 {
			continue // already applied
		}

		sql, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		if _, err := db.sql.Exec(string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := db.sql.Exec(
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
			entry.Name(), time.Now().Unix(),
		); err != nil {
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

// ─── BaseImage operations ────────────────────────────────────────────────────

// CreateBaseImage inserts a new BaseImage record. Status is set to "building".
func (db *DB) CreateBaseImage(ctx context.Context, img api.BaseImage) error {
	diskLayout, err := json.Marshal(img.DiskLayout)
	if err != nil {
		return fmt.Errorf("db: marshal disk_layout: %w", err)
	}
	tags, err := json.Marshal(img.Tags)
	if err != nil {
		return fmt.Errorf("db: marshal tags: %w", err)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO base_images
			(id, name, version, os, arch, status, format, size_bytes, checksum,
			 blob_path, disk_layout, tags, source_url, notes, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		img.ID, img.Name, img.Version, img.OS, img.Arch,
		string(img.Status), string(img.Format),
		img.SizeBytes, img.Checksum, "",
		string(diskLayout), string(tags),
		img.SourceURL, img.Notes, img.ErrorMessage,
		img.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create base image: %w", err)
	}
	return nil
}

// GetBaseImage retrieves a single BaseImage by ID.
func (db *DB) GetBaseImage(ctx context.Context, id string) (api.BaseImage, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, version, os, arch, status, format, size_bytes, checksum,
		       blob_path, disk_layout, tags, source_url, notes, error_message,
		       created_at, finalized_at
		FROM base_images WHERE id = ?
	`, id)

	return scanBaseImage(row)
}

// GetBlobPath returns the server-local filesystem path for an image's blob file.
func (db *DB) GetBlobPath(ctx context.Context, id string) (string, error) {
	var blobPath string
	err := db.sql.QueryRowContext(ctx, `SELECT blob_path FROM base_images WHERE id = ?`, id).Scan(&blobPath)
	if err == sql.ErrNoRows {
		return "", api.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: get blob path: %w", err)
	}
	return blobPath, nil
}

// SetBlobPath updates the blob_path for an image (called after blob is written to disk).
func (db *DB) SetBlobPath(ctx context.Context, id, blobPath string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE base_images SET blob_path = ? WHERE id = ?`, blobPath, id)
	if err != nil {
		return fmt.Errorf("db: set blob path: %w", err)
	}
	return requireOneRow(res, "base_images", id)
}

// ListBaseImages returns all BaseImages. If status is non-empty, it filters by that status.
func (db *DB) ListBaseImages(ctx context.Context, status string) ([]api.BaseImage, error) {
	var rows *sql.Rows
	var err error

	if status != "" {
		rows, err = db.sql.QueryContext(ctx, `
			SELECT id, name, version, os, arch, status, format, size_bytes, checksum,
			       blob_path, disk_layout, tags, source_url, notes, error_message,
			       created_at, finalized_at
			FROM base_images WHERE status = ? ORDER BY created_at DESC
		`, status)
	} else {
		rows, err = db.sql.QueryContext(ctx, `
			SELECT id, name, version, os, arch, status, format, size_bytes, checksum,
			       blob_path, disk_layout, tags, source_url, notes, error_message,
			       created_at, finalized_at
			FROM base_images ORDER BY created_at DESC
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list base images: %w", err)
	}
	defer rows.Close()

	var images []api.BaseImage
	for rows.Next() {
		img, err := scanBaseImage(rows)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// UpdateBaseImageStatus updates the status and error_message for an image.
func (db *DB) UpdateBaseImageStatus(ctx context.Context, id string, status api.ImageStatus, errMsg string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE base_images SET status = ?, error_message = ? WHERE id = ?
	`, string(status), errMsg, id)
	if err != nil {
		return fmt.Errorf("db: update image status: %w", err)
	}
	return requireOneRow(res, "base_images", id)
}

// FinalizeBaseImage sets size, checksum, finalized_at and status=ready.
func (db *DB) FinalizeBaseImage(ctx context.Context, id string, sizeBytes int64, checksum string) error {
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE base_images
		SET size_bytes = ?, checksum = ?, status = 'ready', finalized_at = ?
		WHERE id = ?
	`, sizeBytes, checksum, now, id)
	if err != nil {
		return fmt.Errorf("db: finalize base image: %w", err)
	}
	return requireOneRow(res, "base_images", id)
}

// ArchiveBaseImage sets status=archived.
func (db *DB) ArchiveBaseImage(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `
		UPDATE base_images SET status = 'archived' WHERE id = ?
	`, id)
	if err != nil {
		return fmt.Errorf("db: archive base image: %w", err)
	}
	return requireOneRow(res, "base_images", id)
}

// UpdateDiskLayout replaces the disk_layout JSON for a BaseImage.
func (db *DB) UpdateDiskLayout(ctx context.Context, id string, layout api.DiskLayout) error {
	data, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("db: marshal disk_layout: %w", err)
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE base_images SET disk_layout = ? WHERE id = ?`, string(data), id)
	if err != nil {
		return fmt.Errorf("db: update disk layout: %w", err)
	}
	return requireOneRow(res, "base_images", id)
}

// ─── NodeConfig operations ───────────────────────────────────────────────────

// CreateNodeConfig inserts a new NodeConfig record.
func (db *DB) CreateNodeConfig(ctx context.Context, cfg api.NodeConfig) error {
	interfaces, err := json.Marshal(cfg.Interfaces)
	if err != nil {
		return fmt.Errorf("db: marshal interfaces: %w", err)
	}
	sshKeys, err := json.Marshal(cfg.SSHKeys)
	if err != nil {
		return fmt.Errorf("db: marshal ssh_keys: %w", err)
	}
	groups, err := json.Marshal(cfg.Groups)
	if err != nil {
		return fmt.Errorf("db: marshal groups: %w", err)
	}
	customVars, err := json.Marshal(cfg.CustomVars)
	if err != nil {
		return fmt.Errorf("db: marshal custom_vars: %w", err)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO node_configs
			(id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
			 groups, custom_vars, base_image_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		cfg.ID, cfg.Hostname, cfg.FQDN, cfg.PrimaryMAC,
		string(interfaces), string(sshKeys), cfg.KernelArgs,
		string(groups), string(customVars), cfg.BaseImageID,
		cfg.CreatedAt.Unix(), cfg.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create node config: %w", err)
	}
	return nil
}

// GetNodeConfig retrieves a NodeConfig by its UUID.
func (db *DB) GetNodeConfig(ctx context.Context, id string) (api.NodeConfig, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
		       groups, custom_vars, base_image_id, created_at, updated_at
		FROM node_configs WHERE id = ?
	`, id)
	return scanNodeConfig(row)
}

// GetNodeConfigByMAC retrieves the NodeConfig whose primary_mac matches mac.
func (db *DB) GetNodeConfigByMAC(ctx context.Context, mac string) (api.NodeConfig, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
		       groups, custom_vars, base_image_id, created_at, updated_at
		FROM node_configs WHERE primary_mac = ?
	`, mac)
	return scanNodeConfig(row)
}

// ListNodeConfigs returns all NodeConfigs. If baseImageID is non-empty, filters by it.
func (db *DB) ListNodeConfigs(ctx context.Context, baseImageID string) ([]api.NodeConfig, error) {
	var rows *sql.Rows
	var err error

	if baseImageID != "" {
		rows, err = db.sql.QueryContext(ctx, `
			SELECT id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
			       groups, custom_vars, base_image_id, created_at, updated_at
			FROM node_configs WHERE base_image_id = ? ORDER BY hostname ASC
		`, baseImageID)
	} else {
		rows, err = db.sql.QueryContext(ctx, `
			SELECT id, hostname, fqdn, primary_mac, interfaces, ssh_keys, kernel_args,
			       groups, custom_vars, base_image_id, created_at, updated_at
			FROM node_configs ORDER BY hostname ASC
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list node configs: %w", err)
	}
	defer rows.Close()

	var cfgs []api.NodeConfig
	for rows.Next() {
		cfg, err := scanNodeConfig(rows)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs, rows.Err()
}

// UpdateNodeConfig replaces the mutable fields of a NodeConfig.
func (db *DB) UpdateNodeConfig(ctx context.Context, cfg api.NodeConfig) error {
	interfaces, err := json.Marshal(cfg.Interfaces)
	if err != nil {
		return fmt.Errorf("db: marshal interfaces: %w", err)
	}
	sshKeys, err := json.Marshal(cfg.SSHKeys)
	if err != nil {
		return fmt.Errorf("db: marshal ssh_keys: %w", err)
	}
	groups, err := json.Marshal(cfg.Groups)
	if err != nil {
		return fmt.Errorf("db: marshal groups: %w", err)
	}
	customVars, err := json.Marshal(cfg.CustomVars)
	if err != nil {
		return fmt.Errorf("db: marshal custom_vars: %w", err)
	}

	res, err := db.sql.ExecContext(ctx, `
		UPDATE node_configs
		SET hostname = ?, fqdn = ?, primary_mac = ?, interfaces = ?, ssh_keys = ?,
		    kernel_args = ?, groups = ?, custom_vars = ?, base_image_id = ?, updated_at = ?
		WHERE id = ?
	`,
		cfg.Hostname, cfg.FQDN, cfg.PrimaryMAC,
		string(interfaces), string(sshKeys), cfg.KernelArgs,
		string(groups), string(customVars), cfg.BaseImageID,
		time.Now().Unix(), cfg.ID,
	)
	if err != nil {
		return fmt.Errorf("db: update node config: %w", err)
	}
	return requireOneRow(res, "node_configs", cfg.ID)
}

// DeleteNodeConfig removes a NodeConfig by ID.
func (db *DB) DeleteNodeConfig(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM node_configs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete node config: %w", err)
	}
	return requireOneRow(res, "node_configs", id)
}

// ─── Internal scan helpers ───────────────────────────────────────────────────

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanBaseImage(s scanner) (api.BaseImage, error) {
	var (
		img             api.BaseImage
		status          string
		format          string
		diskLayoutJSON  string
		tagsJSON        string
		createdAtUnix   int64
		finalizedAtUnix sql.NullInt64
		blobPath        string // scanned but not exposed in API type
	)

	err := s.Scan(
		&img.ID, &img.Name, &img.Version, &img.OS, &img.Arch,
		&status, &format,
		&img.SizeBytes, &img.Checksum, &blobPath,
		&diskLayoutJSON, &tagsJSON,
		&img.SourceURL, &img.Notes, &img.ErrorMessage,
		&createdAtUnix, &finalizedAtUnix,
	)
	if err == sql.ErrNoRows {
		return api.BaseImage{}, api.ErrNotFound
	}
	if err != nil {
		return api.BaseImage{}, fmt.Errorf("db: scan base image: %w", err)
	}

	img.Status = api.ImageStatus(status)
	img.Format = api.ImageFormat(format)
	img.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if finalizedAtUnix.Valid {
		t := time.Unix(finalizedAtUnix.Int64, 0).UTC()
		img.FinalizedAt = &t
	}

	if err := json.Unmarshal([]byte(diskLayoutJSON), &img.DiskLayout); err != nil {
		return api.BaseImage{}, fmt.Errorf("db: unmarshal disk_layout: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &img.Tags); err != nil {
		return api.BaseImage{}, fmt.Errorf("db: unmarshal tags: %w", err)
	}
	if img.Tags == nil {
		img.Tags = []string{}
	}

	return img, nil
}

func scanNodeConfig(s scanner) (api.NodeConfig, error) {
	var (
		cfg            api.NodeConfig
		interfacesJSON string
		sshKeysJSON    string
		groupsJSON     string
		customVarsJSON string
		createdAtUnix  int64
		updatedAtUnix  int64
	)

	err := s.Scan(
		&cfg.ID, &cfg.Hostname, &cfg.FQDN, &cfg.PrimaryMAC,
		&interfacesJSON, &sshKeysJSON, &cfg.KernelArgs,
		&groupsJSON, &customVarsJSON, &cfg.BaseImageID,
		&createdAtUnix, &updatedAtUnix,
	)
	if err == sql.ErrNoRows {
		return api.NodeConfig{}, api.ErrNotFound
	}
	if err != nil {
		return api.NodeConfig{}, fmt.Errorf("db: scan node config: %w", err)
	}

	cfg.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	cfg.UpdatedAt = time.Unix(updatedAtUnix, 0).UTC()

	if err := json.Unmarshal([]byte(interfacesJSON), &cfg.Interfaces); err != nil {
		return api.NodeConfig{}, fmt.Errorf("db: unmarshal interfaces: %w", err)
	}
	if err := json.Unmarshal([]byte(sshKeysJSON), &cfg.SSHKeys); err != nil {
		return api.NodeConfig{}, fmt.Errorf("db: unmarshal ssh_keys: %w", err)
	}
	if err := json.Unmarshal([]byte(groupsJSON), &cfg.Groups); err != nil {
		return api.NodeConfig{}, fmt.Errorf("db: unmarshal groups: %w", err)
	}
	if err := json.Unmarshal([]byte(customVarsJSON), &cfg.CustomVars); err != nil {
		return api.NodeConfig{}, fmt.Errorf("db: unmarshal custom_vars: %w", err)
	}

	if cfg.Interfaces == nil {
		cfg.Interfaces = []api.InterfaceConfig{}
	}
	if cfg.SSHKeys == nil {
		cfg.SSHKeys = []string{}
	}
	if cfg.Groups == nil {
		cfg.Groups = []string{}
	}
	if cfg.CustomVars == nil {
		cfg.CustomVars = map[string]string{}
	}

	return cfg, nil
}

// requireOneRow returns ErrNotFound if no rows were affected.
func requireOneRow(res sql.Result, table, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: rows affected (%s %s): %w", table, id, err)
	}
	if n == 0 {
		return api.ErrNotFound
	}
	return nil
}
