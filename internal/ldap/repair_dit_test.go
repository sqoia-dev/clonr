// repair_dit_test.go — preflight / precondition tests for Manager.RepairDIT.
//
// The actual seed-against-running-slapd path requires a live OpenLDAP server
// and is exercised by integration tests + manual cherry-pick validation on
// the cloner host. These tests cover the pure-Go branches that decide whether
// to attempt the repair at all:
//   - module disabled                      → error, no slapd contact
//   - module enabled but base_dn empty     → error, never reached production
//   - admin password unavailable           → error with actionable hint
//   - service password unavailable         → error pointing to re-Enable
//
// This is the regression guard for v0.1.15: silent-success on an empty DIT.
// If a future change removes one of the precondition checks and lets the
// seed silently no-op, these tests fail.
package ldap

import (
	"context"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
	"github.com/sqoia-dev/clustr/internal/db"
)

// makeRepairManager builds a Manager wired to a fresh in-memory DB. No slapd
// runtime is required; RepairDIT bails before any LDAP socket is opened in
// every branch tested here.
//
// LDAPSaveConfig requires a strong CLUSTR_SECRET_KEY to encrypt credentials,
// so callers that exercise the save path must invoke this constructor.
func makeRepairManager(t *testing.T) (*Manager, *db.DB) {
	t.Helper()
	t.Setenv("CLUSTR_SECRET_KEY", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	database := openTestDB(t)
	return New(config.ServerConfig{}, database), database
}

func TestRepairDIT_RejectsWhenModuleDisabled(t *testing.T) {
	m, _ := makeRepairManager(t)
	err := m.RepairDIT(context.Background())
	if err == nil {
		t.Fatal("RepairDIT against disabled module: want error, got nil")
	}
	if !strings.Contains(err.Error(), "module is not enabled") {
		t.Errorf("RepairDIT error = %q, want contains %q", err.Error(), "module is not enabled")
	}
}

func TestRepairDIT_RejectsWhenBaseDNMissing(t *testing.T) {
	m, database := makeRepairManager(t)
	ctx := context.Background()

	// Mark the module enabled but leave base_dn empty — simulates a corrupt
	// row from a partially-completed migration. RepairDIT must refuse rather
	// than try to bind cn=Directory Manager, with no base context.
	if err := database.LDAPSaveConfig(ctx, db.LDAPModuleConfig{
		Enabled: true,
		Status:  "ready",
		BaseDN:  "",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	err := m.RepairDIT(ctx)
	if err == nil {
		t.Fatal("RepairDIT with empty base_dn: want error, got nil")
	}
	if !strings.Contains(err.Error(), "base_dn is empty") {
		t.Errorf("RepairDIT error = %q, want contains %q", err.Error(), "base_dn is empty")
	}
}

func TestRepairDIT_RejectsWhenAdminPasswordUnavailable(t *testing.T) {
	m, database := makeRepairManager(t)
	ctx := context.Background()

	// Module is enabled and base_dn is set, but neither in-memory nor DB has
	// an admin password. This is the realistic failure mode after a server
	// restart on a pre-028 install — the operator must call Enable() once
	// to restore credentials.
	if err := database.LDAPSaveConfig(ctx, db.LDAPModuleConfig{
		Enabled:             true,
		Status:              "ready",
		BaseDN:              "dc=cluster,dc=local",
		AdminPasswd:         "", // explicitly empty
		ServiceBindPassword: "service-bind-password-placeholder",
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	err := m.RepairDIT(ctx)
	if err == nil {
		t.Fatal("RepairDIT with no admin password: want error, got nil")
	}
	if !strings.Contains(err.Error(), "admin password not available") {
		t.Errorf("RepairDIT error = %q, want contains %q", err.Error(), "admin password not available")
	}
}

func TestRepairDIT_RejectsWhenServicePasswordUnavailable(t *testing.T) {
	m, database := makeRepairManager(t)
	ctx := context.Background()

	// Admin password is available (in DB), but service password is missing —
	// without it the seed cannot template the node-reader entry. This is a
	// distinct failure mode that should not silently be substituted with a
	// generated value (would invalidate every existing sssd.conf in the cluster).
	if err := database.LDAPSaveConfig(ctx, db.LDAPModuleConfig{
		Enabled:             true,
		Status:              "ready",
		BaseDN:              "dc=cluster,dc=local",
		AdminPasswd:         "admin-password-placeholder",
		ServiceBindPassword: "", // explicitly empty
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	err := m.RepairDIT(ctx)
	if err == nil {
		t.Fatal("RepairDIT with no service password: want error, got nil")
	}
	if !strings.Contains(err.Error(), "service bind password unavailable") {
		t.Errorf("RepairDIT error = %q, want contains %q", err.Error(), "service bind password unavailable")
	}
}
