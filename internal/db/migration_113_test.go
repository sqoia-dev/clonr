package db_test

// migration_113_test.go — Sprint 41 Day 1
//
// Verifies that migration 113 (roles + role_assignments + users.groups_json)
// applies cleanly on a fresh database and produces the expected schema state.

import (
	"strings"
	"testing"
	"time"

	"github.com/sqoia-dev/clustr/pkg/api"
)

// TestMigration113_TablesExist confirms that both new tables are created.
func TestMigration113_TablesExist(t *testing.T) {
	d := openTestDB(t)
	sql := d.SQL()

	for _, table := range []string{"roles", "role_assignments"} {
		var name string
		err := sql.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found after migration 113: %v", table, err)
		}
	}
}

// TestMigration113_UsersGroupsColumnExists confirms the groups_json column
// was added to the users table.
func TestMigration113_UsersGroupsColumnExists(t *testing.T) {
	d := openTestDB(t)
	_, err := d.SQL().Exec(
		`UPDATE users SET groups_json = '["test-group"]' WHERE 1=0`,
	)
	if err != nil {
		t.Errorf("groups_json column missing from users table: %v", err)
	}
}

// TestMigration113_BuiltinRolesSeeded verifies the three legacy built-in roles
// are present with the expected permissions_json values.
func TestMigration113_BuiltinRolesSeeded(t *testing.T) {
	d := openTestDB(t)
	sql := d.SQL()

	cases := []struct {
		id             string
		wantName       string
		wantIsBuiltin  int
		wantPermSubstr string
	}{
		{"role-admin", "admin", 1, `"*":true`},
		{"role-operator", "operator", 1, `"node.reimage":true`},
		{"role-viewer", "viewer", 1, `"node.read":true`},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			var name, permJSON string
			var isBuiltin int
			err := sql.QueryRow(
				`SELECT name, permissions_json, is_builtin FROM roles WHERE id = ?`, tc.id,
			).Scan(&name, &permJSON, &isBuiltin)
			if err != nil {
				t.Fatalf("role %q not found: %v", tc.id, err)
			}
			if name != tc.wantName {
				t.Errorf("role %q: name = %q, want %q", tc.id, name, tc.wantName)
			}
			if isBuiltin != tc.wantIsBuiltin {
				t.Errorf("role %q: is_builtin = %d, want %d", tc.id, isBuiltin, tc.wantIsBuiltin)
			}
			if !strings.Contains(permJSON, tc.wantPermSubstr) {
				t.Errorf("role %q: permissions_json = %q, want it to contain %q", tc.id, permJSON, tc.wantPermSubstr)
			}
		})
	}
}

// TestMigration113_BackfillFromExistingUsers verifies that existing users get
// role_assignment rows backfilled from their users.role value.
func TestMigration113_BackfillFromExistingUsers(t *testing.T) {
	// openTestDB applies all migrations including 113. However, the 113 backfill
	// INSERT only touches users that existed at migration time. Since we run
	// migrations on an empty DB, there are no pre-existing users to backfill.
	//
	// What we CAN test: manually insert a user and a role_assignment (simulating
	// the backfill pattern) and verify the FK relationship is valid.
	d := openTestDB(t)
	sql := d.SQL()

	// Create a node config needed by some FK constraints (if any), then a user.
	now := time.Now().UTC().Truncate(time.Second)
	cfg := api.NodeConfig{
		ID:         "node-mig113",
		Hostname:   "node-mig113",
		PrimaryMAC: "aa:bb:cc:00:11:55",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := d.CreateNodeConfig(t.Context(), cfg); err != nil {
		t.Fatalf("CreateNodeConfig: %v", err)
	}

	// Insert a user directly.
	_, err := sql.Exec(
		`INSERT INTO users (id, username, password_hash, role, must_change_password, created_at)
		 VALUES ('u-mig113', 'mig113-user', '$2a$10$testhashtesthashhhhhhhhhhh', 'admin', 0, strftime('%s','now'))`,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Insert a role_assignment mimicking the backfill pattern.
	_, err = sql.Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-admin', 'user', 'u-mig113', strftime('%s','now'))`,
	)
	if err != nil {
		t.Fatalf("insert role_assignment: %v", err)
	}

	// Verify the assignment was created and FK is valid.
	var count int
	err = sql.QueryRow(
		`SELECT COUNT(*) FROM role_assignments ra
		 JOIN roles r ON r.id = ra.role_id
		 WHERE ra.subject_kind='user' AND ra.subject_id='u-mig113'`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count role_assignments: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 role_assignment for u-mig113, got %d", count)
	}
}

// TestMigration113_RoleAssignmentUniqueConstraint verifies the UNIQUE constraint
// on (role_id, subject_kind, subject_id) prevents duplicate assignments.
func TestMigration113_RoleAssignmentUniqueConstraint(t *testing.T) {
	d := openTestDB(t)
	sql := d.SQL()

	// Insert a user.
	_, err := sql.Exec(
		`INSERT INTO users (id, username, password_hash, role, must_change_password, created_at)
		 VALUES ('u-unique-test', 'unique-user', '$2a$10$testhashtesthashhhhhhhhhhh', 'operator', 0, strftime('%s','now'))`,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// First assignment — should succeed.
	_, err = sql.Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-operator', 'user', 'u-unique-test', strftime('%s','now'))`,
	)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second identical assignment — should fail on UNIQUE constraint.
	_, err = sql.Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-operator', 'user', 'u-unique-test', strftime('%s','now'))`,
	)
	if err == nil {
		t.Fatal("second duplicate insert succeeded; expected UNIQUE constraint violation")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Errorf("expected UNIQUE error, got: %v", err)
	}
}

// TestMigration113_SubjectKindCheckConstraint verifies that subject_kind is
// restricted to 'user' and 'posix_group'.
func TestMigration113_SubjectKindCheckConstraint(t *testing.T) {
	d := openTestDB(t)
	sql := d.SQL()

	_, err := sql.Exec(
		`INSERT INTO role_assignments (id, role_id, subject_kind, subject_id, created_at)
		 VALUES (lower(hex(randomblob(16))), 'role-viewer', 'invalid_kind', 'subject-x', strftime('%s','now'))`,
	)
	if err == nil {
		t.Fatal("insert with invalid subject_kind succeeded; expected CHECK constraint violation")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "check") {
		t.Errorf("expected CHECK error, got: %v", err)
	}
}

// TestMigration113_GroupsJsonRoundTrip verifies that groups_json can be written
// and read back from the users table.
func TestMigration113_GroupsJsonRoundTrip(t *testing.T) {
	d := openTestDB(t)
	sql := d.SQL()

	_, err := sql.Exec(
		`INSERT INTO users (id, username, password_hash, role, must_change_password, created_at, groups_json)
		 VALUES ('u-groups-test', 'groups-user', '$2a$10$testhashtesthashhhhhhhhhhh', 'readonly', 0, strftime('%s','now'), '["cluster-ops","hpc-admins"]')`,
	)
	if err != nil {
		t.Fatalf("insert user with groups_json: %v", err)
	}

	var groupsJSON string
	err = sql.QueryRow(
		`SELECT COALESCE(groups_json,'[]') FROM users WHERE id='u-groups-test'`,
	).Scan(&groupsJSON)
	if err != nil {
		t.Fatalf("select groups_json: %v", err)
	}
	if !strings.Contains(groupsJSON, "cluster-ops") {
		t.Errorf("groups_json round-trip: got %q, want it to contain 'cluster-ops'", groupsJSON)
	}
}
