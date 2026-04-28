package main

// bootstrap_admin_test.go — unit and integration tests for the
// bootstrap-admin subcommand and its password validator.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/internal/db"
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
	// openTestDB already ran migrations via db.Open; close it so
	// runBootstrapAdmin can reopen via its own db.Open call.
	// (db.Open returns the *DB directly; the file is already initialised.)

	// Point CLUSTR_DB_PATH at the temp DB so runBootstrapAdmin picks it up.
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	err := runBootstrapAdmin("testrecovery", "weak", false, true)
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

	err := runBootstrapAdmin("testuser", "weak", false, false)
	if err == nil {
		t.Fatal("expected error for weak password without bypass, got nil")
	}
}

// TestRunBootstrapAdmin_NoBypass_StrongPasswordSucceeds confirms normal path
// still works after the bypass flag was added.
func TestRunBootstrapAdmin_NoBypass_StrongPasswordSucceeds(t *testing.T) {
	_, dbPath := openTestDB(t)
	t.Setenv("CLUSTR_DB_PATH", dbPath)

	err := runBootstrapAdmin("adminuser", "Clustr123!", false, false)
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
	if err := runBootstrapAdmin("original", "Clustr123!", false, false); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Force-replace with a weak password via bypass.
	if err := runBootstrapAdmin("recovery", "clustr", true, true); err != nil {
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

// Ensure the test file compiles even when CLUSTR_DB_PATH is not set by
// using t.Setenv which restores the env after each test.
var _ = os.Getenv
