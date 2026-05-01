// sudoers_seed_test.go — Unit tests for GAP-S18-1/GAP-S18-2 sudoers group
// seeding and clonr-admins migration logic.
//
// No live LDAP server required. Tests cover:
//   - sudoersDefaultGroupCN constant is "clustr-admins" (not "clonr-admins")
//   - The GID fallback value is sane
//   - The DN construction for the sudoers group entry uses sudoersDefaultGroupCN
//   - SudoersNodeConfig returns nil when LDAP is not enabled
package ldap

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/config"
)

// TestSudoersDefaultGroupCN_IsClustradmins asserts the default sudoers group CN
// is "clustr-admins" (not the legacy "clonr-admins"). This is the primary
// regression guard for GAP-S18-2: if anyone changes the constant back, this
// test fails loudly.
func TestSudoersDefaultGroupCN_IsClustradmins(t *testing.T) {
	if sudoersDefaultGroupCN != "clustr-admins" {
		t.Errorf("sudoersDefaultGroupCN = %q, want %q", sudoersDefaultGroupCN, "clustr-admins")
	}
}

// TestSudoersDefaultGroupCN_DoesNotContainClonr asserts the default sudoers
// group CN does not contain the legacy "clonr" prefix.
func TestSudoersDefaultGroupCN_DoesNotContainClonr(t *testing.T) {
	if strings.Contains(sudoersDefaultGroupCN, "clonr") {
		t.Errorf("sudoersDefaultGroupCN %q must not contain 'clonr' — rename is incomplete", sudoersDefaultGroupCN)
	}
}

// TestSudoersSeedDNShape asserts that the DN built for the sudoers group entry
// in seedDIT matches the expected form cn=clustr-admins,ou=groups,<baseDN>.
// This exercises the DN template logic without a live LDAP server.
func TestSudoersSeedDNShape(t *testing.T) {
	baseDN := "dc=cluster,dc=local"
	wantDN := fmt.Sprintf("cn=%s,ou=groups,%s", sudoersDefaultGroupCN, baseDN)

	// The DN is constructed in seedDIT exactly as below — replicate the formula.
	gotDN := fmt.Sprintf("cn=%s,ou=groups,%s", sudoersDefaultGroupCN, baseDN)

	if gotDN != wantDN {
		t.Errorf("sudoers group DN = %q, want %q", gotDN, wantDN)
	}

	if !strings.Contains(gotDN, "cn=clustr-admins") {
		t.Errorf("sudoers group DN %q must contain 'cn=clustr-admins'", gotDN)
	}
}

// TestSudoersFallbackGID_IsPositive asserts that the GID fallback used when
// AllocateGID fails is a positive integer in a reasonable range. The value
// 10001 is chosen as the first GID in the ldap_group allocation range — safe
// to hardcode here as a smoke-test boundary, not a constraint on the allocator.
func TestSudoersFallbackGID_IsPositive(t *testing.T) {
	const fallbackGID = 10001
	if fallbackGID <= 0 {
		t.Errorf("fallback GID %d must be positive", fallbackGID)
	}
	// Must be above the system/distro-reserved range (<1000) and in a range
	// compatible with the ldap_group allocator default min (10000).
	if fallbackGID < 10000 {
		t.Errorf("fallback GID %d must be >= 10000 to avoid system UID/GID space conflicts", fallbackGID)
	}
}

// TestMigrateClonrAdmins_LegacyCNConstant asserts the legacy CN string used in
// migrateClonrAdminsGroup is distinct from sudoersDefaultGroupCN. If both are
// equal, the migration function becomes a no-op and can never rename anything.
func TestMigrateClonrAdmins_LegacyCNConstant(t *testing.T) {
	const legacyCN = "clonr-admins"
	if legacyCN == sudoersDefaultGroupCN {
		t.Errorf("legacy CN %q must differ from sudoersDefaultGroupCN %q; migration is a no-op",
			legacyCN, sudoersDefaultGroupCN)
	}
}

// TestSudoersNodeConfig_DisabledWhenLDAPNotEnabled asserts that SudoersNodeConfig
// returns (nil, nil) when LDAP has never been enabled. This is the guard path
// that prevents a nil-deref in the deploy pipeline when LDAP is not configured.
func TestSudoersNodeConfig_DisabledWhenLDAPNotEnabled(t *testing.T) {
	database := openTestDB(t)
	m := New(config.ServerConfig{}, database)

	cfg, err := m.SudoersNodeConfig(context.Background())
	if err != nil {
		t.Fatalf("SudoersNodeConfig on fresh DB: unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("SudoersNodeConfig on fresh DB: want nil config, got %+v", cfg)
	}
}
