// Package auth provides RBAC role resolution and permission checking for
// clustr-serverd. The resolution path reads from the roles and role_assignments
// tables (Sprint 41) with a legacy fallback to users.role for one release.
//
// This package is intentionally not a sub-package of internal/server/ so that
// future consumers (JOURNAL-ENDPOINT, CLI auth, background workers) can import
// it without creating an import cycle through the server package.
//
// All exported functions in this package are read-only. No table mutations.
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sqoia-dev/clustr/internal/db"
)

// Resolution is the cached result of role resolution for one user request.
// It is computed once per request (in middleware) and stored in the request
// context for downstream handlers to consult cheaply.
//
// THREAD-SAFETY: Resolution is immutable after construction. Safe to read from
// multiple goroutines without additional synchronisation.
type Resolution struct {
	// UserID is the users.id of the resolved user.
	UserID string
	// IsAdmin is true when the union of permissions contains the "*" wildcard.
	// Shortcut for common "is this user an admin?" checks.
	IsAdmin bool
	// Roles is the sorted list of role names the user holds. Sorted for
	// deterministic comparison and logging.
	Roles []string
	// Permissions is the union of all role permissions. The wildcard "*" causes
	// IsAdmin=true and matches every Allow query.
	Permissions map[string]bool
	// Groups is the list of posix CNs from users.groups_json (LDAP memberOf
	// cache). Used by ResolveRoles to match posix_group role assignments.
	Groups []string
}

// ResolveRoles computes the role/permission union for userID, considering:
//  1. Direct user assignments in role_assignments (subject_kind='user').
//  2. Posix group assignments in role_assignments (subject_kind='posix_group')
//     where the user's cached LDAP groups include the assigned subject_id.
//  3. The legacy users.role column, as a fallback when no role_assignment row
//     exists for the user (one-release deprecation path).
//
// The function is read-only. It MUST NOT mutate any table.
//
// A missing user (ErrUserNotFound) returns an empty Resolution and no error —
// the caller (middleware) handles unauthenticated requests before reaching here.
// Any other DB error is returned as-is.
func ResolveRoles(ctx context.Context, database *db.DB, userID string) (*Resolution, error) {
	sqlDB := database.SQL()

	// 1. Load the user row to get the legacy role and the groups cache.
	var (
		legacyRole string
		groupsJSON sql.NullString
	)
	err := sqlDB.QueryRowContext(ctx,
		`SELECT COALESCE(role,''), COALESCE(groups_json,'[]') FROM users WHERE id = ?`,
		userID,
	).Scan(&legacyRole, &groupsJSON)
	if err == sql.ErrNoRows {
		// Unknown user — return empty resolution; middleware handles 401.
		return &Resolution{UserID: userID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth.ResolveRoles: fetch user %s: %w", userID, err)
	}

	// Parse cached posix groups.
	var groups []string
	if groupsJSON.Valid && groupsJSON.String != "" && groupsJSON.String != "[]" {
		if jerr := json.Unmarshal([]byte(groupsJSON.String), &groups); jerr != nil {
			// Corrupt cache — treat as no groups; do not fail the request.
			groups = nil
		}
	}

	// 2. Load direct role assignments for this user.
	roleNames, permissions, err := loadAssignments(ctx, sqlDB, "user", userID)
	if err != nil {
		return nil, fmt.Errorf("auth.ResolveRoles: load user assignments for %s: %w", userID, err)
	}

	// 3. Load posix group assignments for each group the user belongs to.
	for _, cn := range groups {
		gNames, gPerms, gerr := loadAssignments(ctx, sqlDB, "posix_group", cn)
		if gerr != nil {
			return nil, fmt.Errorf("auth.ResolveRoles: load group assignments for %s/%s: %w", userID, cn, gerr)
		}
		for _, n := range gNames {
			roleNames = appendUnique(roleNames, n)
		}
		for k, v := range gPerms {
			if v {
				permissions[k] = true
			}
		}
	}

	// 4. Legacy fallback: if no role_assignments exist, derive from users.role.
	if len(roleNames) == 0 && legacyRole != "" {
		fallbackName, fallbackPerms := legacyRolePermissions(legacyRole)
		if fallbackName != "" {
			roleNames = []string{fallbackName}
			permissions = fallbackPerms
		}
	}

	sort.Strings(roleNames)

	isAdmin := permissions["*"]

	return &Resolution{
		UserID:      userID,
		IsAdmin:     isAdmin,
		Roles:       roleNames,
		Permissions: permissions,
		Groups:      groups,
	}, nil
}

// Allow reports whether the Resolution grants the named permission verb.
//
// Matching rules (wildcard only in the grant, not the query):
//  1. The Resolution's IsAdmin flag (wildcard "*") grants everything.
//  2. An exact match of verb in Permissions.
//  3. A namespace wildcard: if Permissions contains "node.*" and verb is
//     "node.read", the match succeeds.
//
// The query verb must be an exact dot-delimited resource.action string.
// Querying with a wildcard (e.g. "node.*") returns false — handlers must
// ask for exact verbs to prevent accidental over-grants.
func Allow(r *Resolution, verb string) bool {
	if r == nil {
		return false
	}
	if r.IsAdmin {
		return true
	}
	if r.Permissions[verb] {
		return true
	}
	// Namespace wildcard check: "node.*" in grants matches "node.read" query.
	if idx := strings.LastIndexByte(verb, '.'); idx >= 0 {
		ns := verb[:idx] + ".*"
		if r.Permissions[ns] {
			return true
		}
	}
	return false
}

// ─── internal helpers ──────────────────────────────────────────────────────

// loadAssignments queries role_assignments for (subjectKind, subjectID) and
// returns the role names and merged permission map for all matching roles.
func loadAssignments(ctx context.Context, sqlDB *sql.DB, subjectKind, subjectID string) ([]string, map[string]bool, error) {
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT r.name, r.permissions_json
		FROM role_assignments ra
		JOIN roles r ON r.id = ra.role_id
		WHERE ra.subject_kind = ? AND ra.subject_id = ?
	`, subjectKind, subjectID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var names []string
	perms := make(map[string]bool)

	for rows.Next() {
		var name, permJSON string
		if serr := rows.Scan(&name, &permJSON); serr != nil {
			return nil, nil, serr
		}
		names = append(names, name)
		var p map[string]bool
		if jerr := json.Unmarshal([]byte(permJSON), &p); jerr == nil {
			for k, v := range p {
				if v {
					perms[k] = true
				}
			}
		}
	}
	return names, perms, rows.Err()
}

// legacyRolePermissions maps the old users.role string to a built-in role name
// and its permissions. Used as a one-release fallback when no role_assignments
// row exists for a user.
func legacyRolePermissions(role string) (string, map[string]bool) {
	switch role {
	case "admin":
		return "admin", map[string]bool{"*": true}
	case "operator":
		return "operator", map[string]bool{
			"node.read": true, "node.write": true, "node.reimage": true,
		}
	case "readonly", "viewer", "pi", "director":
		return "viewer", map[string]bool{"node.read": true}
	default:
		return "", nil
	}
}

// appendUnique appends s to slice only if it is not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
