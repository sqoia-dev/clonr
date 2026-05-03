package main

// bootstrap_admin_test.go — unit and integration tests for the
// bootstrap-admin subcommand and its password validator.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// --- Unit tests: validateBootstrapPassword ---

func TestValidateBootstrapPassword_TooShort(t *testing.T) {
	if err := validateBootstrapPassword("Ab1"); err == nil {
		t.Error("expected error for password shorter than 8 chars, got nil")
	}
}

func TestValidateBootstrapPassword_NoUppercase(t *testing.T) {
	if err := validateBootstrapPassword("alllower1"); err == nil {
		t.Error("expected error for password with no uppercase letter, got nil")
	}
}

func TestValidateBootstrapPassword_NoDigit(t *testing.T) {
	if err := validateBootstrapPassword("AllLowerUpper"); err == nil {
		t.Error("expected error for password with no digit, got nil")
	}
}

func TestValidateBootstrapPassword_NoLowercase(t *testing.T) {
	if err := validateBootstrapPassword("ALLUPPER1"); err == nil {
		t.Error("expected error for password with no lowercase letter, got nil")
	}
}

func TestValidateBootstrapPassword_Valid(t *testing.T) {
	if err := validateBootstrapPassword("Clustr123"); err != nil {
		t.Errorf("expected no error for valid password, got: %v", err)
	}
}

// TestValidateBootstrapPassword_WeakButNotCalledOnBypass confirms that
// "clustr" would fail validation when called directly (the bypass does not
// call validateBootstrapPassword at all — this test documents the contract).
func TestValidateBootstrapPassword_WeakBypassExample(t *testing.T) {
	if err := validateBootstrapPassword("clustr"); err == nil {
		t.Error("'clustr' should fail complexity validation — bypass flag must be used to set it")
	}
}

// --- Integration tests: runBootstrapAdmin ---

// openTestDB opens a fresh in-memory SQLite DB backed by a temp file.
// Caller is responsible for closing and removing it.
func openTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "clustr.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return database, dbPath
}

// TestRunBootstrapAdmin_BypassComplexity_Succeeds verifies that a simple
// password (normally rejected by the complexity validator) is accepted when
// --bypass-complexity is set, the user is created, and the bypass is recorded
// in the audit log.
func TestRunBootstrapAdmin_BypassComplexity_Succeeds(t *testing.T) {
	_, dbPath := openTestDB(t)

	// Point CLUSTR_DB_PATH at the temp DB so runBootstrapAdmin picks it up.
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	err := runBootstrapAdmin("testrecovery", "weak", false, false, true)
	if err != nil {
		t.Fatalf("runBootstrapAdmin with bypass should succeed: %v", err)
	}

	// Verify the user was created.
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	u, err := database.GetUserByUsername(ctx, "testrecovery")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Username != "testrecovery" {
		t.Errorf("username = %q, want testrecovery", u.Username)
	}
	if u.Role != db.UserRoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if u.MustChangePassword {
		t.Error("must_change_password should be false when created via bootstrap-admin")
	}

	// Verify the bypass audit entry exists.
	auditRecords, _, err := database.QueryAuditLog(ctx, db.AuditQueryParams{
		Action: AuditActionBootstrapAdminBypassComplexity,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("QueryAuditLog: %v", err)
	}
	if len(auditRecords) == 0 {
		t.Error("expected audit entry for bypass_complexity, found none")
	}
}

// TestRunBootstrapAdmin_NoBypass_WeakPasswordFails verifies that without
// --bypass-complexity, a simple password is rejected with an error.
func TestRunBootstrapAdmin_NoBypass_WeakPasswordFails(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	err := runBootstrapAdmin("testuser", "weak", false, false, false)
	if err == nil {
		t.Fatal("expected error for weak password without bypass, got nil")
	}
}

// TestRunBootstrapAdmin_NoBypass_StrongPasswordSucceeds confirms normal path
// still works after the bypass flag was added.
func TestRunBootstrapAdmin_NoBypass_StrongPasswordSucceeds(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	err := runBootstrapAdmin("adminuser", "Clustr123!", false, false, false)
	if err != nil {
		t.Fatalf("runBootstrapAdmin with strong password should succeed: %v", err)
	}

	// Verify user was created.
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	u, err := database.GetUserByUsername(ctx, "adminuser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Role != db.UserRoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}

	// Verify NO bypass audit entry exists.
	auditRecords, _, err := database.QueryAuditLog(ctx, db.AuditQueryParams{
		Action: AuditActionBootstrapAdminBypassComplexity,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("QueryAuditLog: %v", err)
	}
	if len(auditRecords) != 0 {
		t.Errorf("expected no bypass audit entries, got %d", len(auditRecords))
	}
}

// TestRunBootstrapAdmin_ForceWithBypass verifies --force + --bypass-complexity
// together: wipes existing users and creates a new admin with a weak password.
func TestRunBootstrapAdmin_ForceWithBypass(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	// Create a strong initial user first.
	if err := runBootstrapAdmin("original", "Clustr123!", false, false, false); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Force-replace with a weak password via bypass.
	if err := runBootstrapAdmin("recovery", "clustr", true, false, true); err != nil {
		t.Fatalf("runBootstrapAdmin --force --bypass-complexity: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// Original user should be gone.
	if _, err := database.GetUserByUsername(ctx, "original"); err == nil {
		t.Error("expected original user to be deleted after --force, but found it")
	}

	// Recovery user should exist.
	u, err := database.GetUserByUsername(ctx, "recovery")
	if err != nil {
		t.Fatalf("GetUserByUsername recovery: %v", err)
	}
	if u.Role != db.UserRoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}

	// Bypass audit entry must exist.
	auditRecords, _, err := database.QueryAuditLog(ctx, db.AuditQueryParams{
		Action: AuditActionBootstrapAdminBypassComplexity,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("QueryAuditLog: %v", err)
	}
	if len(auditRecords) == 0 {
		t.Error("expected bypass audit entry after --force --bypass-complexity, found none")
	}
}

// TestBootstrapAdmin_DefaultCredentials_AntiRegression is the permanent anti-regression
// test for the clustr/clustr default credentials contract.
//
// POLICY: The default admin path (no --username/--password flags, no env vars) MUST:
//  1. Create a user named "clustr"
//  2. Set that user's password to "clustr" (hash matches)
//  3. Set MustChangePassword=false — the user works permanently, no forced change
//
// If this test fails, someone broke the default-credentials contract.
func TestBootstrapAdmin_DefaultCredentials_AntiRegression(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)
	// Explicitly clear env vars so no credentials can sneak in.
	t.Setenv("CLUSTR_BOOTSTRAP_USERNAME", "")
	t.Setenv("CLUSTR_BOOTSTRAP_PASSWORD", "")

	// Call with empty username and password to exercise the default path.
	if err := runBootstrapAdmin("", "", false, false, false); err != nil {
		t.Fatalf("runBootstrapAdmin with defaults should succeed: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	u, err := database.GetUserByUsername(ctx, DefaultAdminUsername)
	if err != nil {
		t.Fatalf("default user %q not found: %v", DefaultAdminUsername, err)
	}

	// Invariant 1: MustChangePassword MUST be false — no forced change flow.
	if u.MustChangePassword {
		t.Error("ANTI-REGRESSION: default clustr/clustr path must set must_change_password=false")
	}

	// Invariant 2: The stored hash must match the default password "clustr".
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(DefaultAdminPassword)); err != nil {
		t.Errorf("ANTI-REGRESSION: default password hash does not match %q: %v", DefaultAdminPassword, err)
	}
}

// TestBootstrapAdmin_AddUsername_PreservesExisting is the anti-regression test for
// the ADD behavior introduced in Sprint 11 (#82).
//
// CONTRACT: running "bootstrap-admin --username ops" against a DB that already has
// the "clustr" default admin MUST result in BOTH "clustr" AND "ops" admins existing.
// The prior "clustr" admin must NOT be deleted or modified.
func TestBootstrapAdmin_AddUsername_PreservesExisting(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)
	t.Setenv("CLUSTR_BOOTSTRAP_USERNAME", "")
	t.Setenv("CLUSTR_BOOTSTRAP_PASSWORD", "")

	// Step 1: create the default clustr/clustr admin.
	if err := runBootstrapAdmin("", "", false, false, false); err != nil {
		t.Fatalf("setup default admin: %v", err)
	}

	// Step 2: add "ops" admin alongside clustr.
	if err := runBootstrapAdmin("ops", "OpsAdmin99!", false, false, false); err != nil {
		t.Fatalf("add ops admin: %v", err)
	}

	// Verify both users exist.
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	clustrUser, err := database.GetUserByUsername(ctx, "clustr")
	if err != nil {
		t.Fatalf("ANTI-REGRESSION: 'clustr' admin missing after --username ops: %v", err)
	}
	if clustrUser.Role != db.UserRoleAdmin {
		t.Errorf("clustr role = %q, want admin", clustrUser.Role)
	}

	opsUser, err := database.GetUserByUsername(ctx, "ops")
	if err != nil {
		t.Fatalf("'ops' admin missing: %v", err)
	}
	if opsUser.Role != db.UserRoleAdmin {
		t.Errorf("ops role = %q, want admin", opsUser.Role)
	}

	// Total user count must be exactly 2.
	count, err := database.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 2 {
		t.Errorf("ANTI-REGRESSION: expected 2 users after adding ops, got %d", count)
	}
}

// TestBootstrapAdmin_DefaultIdempotent verifies that calling bootstrap-admin with
// no flags twice is a no-op on the second call (clustr already exists).
func TestBootstrapAdmin_DefaultIdempotent(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)
	t.Setenv("CLUSTR_BOOTSTRAP_USERNAME", "")
	t.Setenv("CLUSTR_BOOTSTRAP_PASSWORD", "")

	// First call creates clustr/clustr.
	if err := runBootstrapAdmin("", "", false, false, false); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call must succeed without error (idempotent no-op).
	if err := runBootstrapAdmin("", "", false, false, false); err != nil {
		t.Fatalf("ANTI-REGRESSION: second default call should be idempotent, got: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	// Still exactly one user.
	count, err := database.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 user after idempotent default call, got %d", count)
	}
}

// TestBootstrapAdmin_ReplaceDefault removes only clustr admin and recreates it,
// leaving any other users untouched.
func TestBootstrapAdmin_ReplaceDefault(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)
	t.Setenv("CLUSTR_BOOTSTRAP_USERNAME", "")
	t.Setenv("CLUSTR_BOOTSTRAP_PASSWORD", "")

	// Create clustr/clustr and an ops admin.
	if err := runBootstrapAdmin("", "", false, false, false); err != nil {
		t.Fatalf("setup clustr admin: %v", err)
	}
	if err := runBootstrapAdmin("ops", "OpsAdmin99!", false, false, false); err != nil {
		t.Fatalf("setup ops admin: %v", err)
	}

	// --replace-default should remove clustr, recreate it, leave ops.
	if err := runBootstrapAdmin("", "", false, true, false); err != nil {
		t.Fatalf("--replace-default: %v", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// ops must still be present.
	if _, err := database.GetUserByUsername(ctx, "ops"); err != nil {
		t.Fatalf("ops admin should survive --replace-default: %v", err)
	}

	// clustr must exist (recreated).
	clustrUser, err := database.GetUserByUsername(ctx, "clustr")
	if err != nil {
		t.Fatalf("clustr admin should be re-created by --replace-default: %v", err)
	}
	if clustrUser.Role != db.UserRoleAdmin {
		t.Errorf("clustr role = %q, want admin", clustrUser.Role)
	}

	// Still exactly 2 users.
	count, err := database.CountUsers(ctx)
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 users after --replace-default, got %d", count)
	}
}

// Ensure the test file compiles even when CLUSTR_DB_PATH is not set by
// using t.Setenv which restores the env after each test.
var _ = os.Getenv
