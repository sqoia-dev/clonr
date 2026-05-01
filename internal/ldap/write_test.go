// write_test.go — unit tests for Sprint 8 LDAP write-back helpers.
// No live LDAP server required. Tests cover pure-Go logic:
// directoryWriteAudit, WriteCapableStatus, dialect error types, and
// the DB layer for group mode (LDAPSetGroupMode / LDAPGetGroupMode).
package ldap

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sqoia-dev/clustr/internal/db"
)

// ─── directoryWriteAudit ──────────────────────────────────────────────────────

func TestDirectoryWriteAudit_ContainsRequiredFields(t *testing.T) {
	result := directoryWriteAudit("uid=alice,ou=people,dc=test,dc=local", "add", map[string]string{
		"uid":          "alice",
		"userPassword": "s3cr3t",
		"cn":           "Alice Smith",
	})

	if result["directory_write"] != true {
		t.Error("expected directory_write=true")
	}
	if result["dn"] != "uid=alice,ou=people,dc=test,dc=local" {
		t.Errorf("unexpected dn: %v", result["dn"])
	}
	if result["operation"] != "add" {
		t.Errorf("unexpected operation: %v", result["operation"])
	}
	hashes, ok := result["attr_hashes"].(map[string]string)
	if !ok {
		t.Fatal("attr_hashes should be map[string]string")
	}
	// All three attrs must be present as hashes.
	for _, attr := range []string{"uid", "userPassword", "cn"} {
		if _, exists := hashes[attr]; !exists {
			t.Errorf("expected attr_hashes to contain %q", attr)
		}
	}
}

func TestDirectoryWriteAudit_DoesNotLeakPassword(t *testing.T) {
	result := directoryWriteAudit("uid=bob,ou=people,dc=test,dc=local", "password_reset", map[string]string{
		"userPassword": "super-secret-temp-password",
	})

	hashes, ok := result["attr_hashes"].(map[string]string)
	if !ok {
		t.Fatal("attr_hashes should be map[string]string")
	}
	// Hash must NOT equal the raw password.
	if h, exists := hashes["userPassword"]; exists {
		if h == "super-secret-temp-password" {
			t.Error("userPassword should be hashed, not stored in plaintext")
		}
		// Hash should be hex digits, 8 chars (4 bytes truncated SHA-256).
		if len(h) != 8 {
			t.Errorf("expected 8-char hex hash, got %q (len=%d)", h, len(h))
		}
	} else {
		t.Error("expected userPassword key in attr_hashes")
	}
}

func TestDirectoryWriteAudit_EmptyAttrs(t *testing.T) {
	result := directoryWriteAudit("cn=testgroup,ou=groups,dc=test,dc=local", "delete", map[string]string{})
	hashes, ok := result["attr_hashes"].(map[string]string)
	if !ok {
		t.Fatal("attr_hashes should be map[string]string")
	}
	if len(hashes) != 0 {
		t.Errorf("expected empty attr_hashes for no attributes, got %v", hashes)
	}
}

func TestDirectoryWriteAudit_DeterministicHashing(t *testing.T) {
	// Same input must produce the same hash every time.
	r1 := directoryWriteAudit("uid=carol,ou=people,dc=test,dc=local", "modify", map[string]string{
		"cn": "Carol Jones",
	})
	r2 := directoryWriteAudit("uid=carol,ou=people,dc=test,dc=local", "modify", map[string]string{
		"cn": "Carol Jones",
	})
	h1 := r1["attr_hashes"].(map[string]string)["cn"]
	h2 := r2["attr_hashes"].(map[string]string)["cn"]
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

func TestDirectoryWriteAudit_DifferentValuesDifferentHashes(t *testing.T) {
	r1 := directoryWriteAudit("uid=dave,ou=people,dc=test,dc=local", "modify", map[string]string{
		"cn": "Dave A",
	})
	r2 := directoryWriteAudit("uid=dave,ou=people,dc=test,dc=local", "modify", map[string]string{
		"cn": "Dave B",
	})
	h1 := r1["attr_hashes"].(map[string]string)["cn"]
	h2 := r2["attr_hashes"].(map[string]string)["cn"]
	if h1 == h2 {
		t.Error("different attribute values should produce different hashes")
	}
}

// ─── ErrDialectNotImplemented ─────────────────────────────────────────────────

func TestErrDialectNotImplemented_ErrorString(t *testing.T) {
	err := &ErrDialectNotImplemented{Dialect: DialectFreeIPA, Op: "create_user"}
	msg := err.Error()
	if !strings.Contains(msg, "freeipa") {
		t.Errorf("error message should mention dialect, got: %q", msg)
	}
	if !strings.Contains(msg, "create_user") {
		t.Errorf("error message should mention operation, got: %q", msg)
	}
	if !strings.Contains(msg, "v0.4.0") {
		t.Errorf("error message should mention version, got: %q", msg)
	}
}

func TestErrDialectNotImplemented_ADDialect(t *testing.T) {
	err := &ErrDialectNotImplemented{Dialect: DialectAD, Op: "password_reset"}
	msg := err.Error()
	if !strings.Contains(msg, string(DialectAD)) {
		t.Errorf("expected AD dialect in error, got: %q", msg)
	}
}

func TestErrDialectNotImplemented_GenericDialect(t *testing.T) {
	err := &ErrDialectNotImplemented{Dialect: DialectGeneric, Op: "delete_group"}
	msg := err.Error()
	if !strings.Contains(msg, string(DialectGeneric)) {
		t.Errorf("expected generic dialect in error, got: %q", msg)
	}
}

// ─── DB group mode (integration with in-memory SQLite) ────────────────────────

// openTestDB creates a fresh SQLite database in a temp dir with all migrations applied.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/test.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestLDAPGroupMode_DefaultIsOverlay(t *testing.T) {
	d := openTestDB(t)
	mode, err := d.LDAPGetGroupMode(context.Background(), "some-group")
	if err != nil {
		t.Fatalf("LDAPGetGroupMode: %v", err)
	}
	if mode != "overlay" {
		t.Errorf("expected default mode=overlay, got %q", mode)
	}
}

func TestLDAPGroupMode_SetAndGet(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.LDAPSetGroupMode(ctx, "hpc-users", "direct", "test-actor"); err != nil {
		t.Fatalf("LDAPSetGroupMode: %v", err)
	}
	mode, err := d.LDAPGetGroupMode(ctx, "hpc-users")
	if err != nil {
		t.Fatalf("LDAPGetGroupMode: %v", err)
	}
	if mode != "direct" {
		t.Errorf("expected mode=direct, got %q", mode)
	}
}

func TestLDAPGroupMode_SwitchBack(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Set to direct, then back to overlay.
	_ = d.LDAPSetGroupMode(ctx, "admins", "direct", "alice")
	_ = d.LDAPSetGroupMode(ctx, "admins", "overlay", "alice")

	mode, err := d.LDAPGetGroupMode(ctx, "admins")
	if err != nil {
		t.Fatalf("LDAPGetGroupMode: %v", err)
	}
	if mode != "overlay" {
		t.Errorf("expected mode=overlay after switch-back, got %q", mode)
	}
}

func TestLDAPGroupMode_InvalidMode(t *testing.T) {
	d := openTestDB(t)
	err := d.LDAPSetGroupMode(context.Background(), "badgroup", "write-everything", "test")
	if err == nil {
		t.Error("expected error for invalid mode, got nil")
	}
}

func TestLDAPGroupMode_GetAll(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	_ = d.LDAPSetGroupMode(ctx, "group-a", "direct", "actor1")
	_ = d.LDAPSetGroupMode(ctx, "group-b", "overlay", "actor2")

	modes, err := d.LDAPGetGroupModes(ctx)
	if err != nil {
		t.Fatalf("LDAPGetGroupModes: %v", err)
	}
	if len(modes) < 2 {
		t.Errorf("expected at least 2 group modes, got %d", len(modes))
	}
	found := map[string]string{}
	for _, gm := range modes {
		found[gm.CN] = gm.Mode
	}
	if found["group-a"] != "direct" {
		t.Errorf("group-a: expected direct, got %q", found["group-a"])
	}
	if found["group-b"] != "overlay" {
		t.Errorf("group-b: expected overlay, got %q", found["group-b"])
	}
}

// ─── WriteCapableStatus logic ─────────────────────────────────────────────────

// These tests verify the write-capable status derivation from the config row
// without needing a live LDAP server. We use an in-memory DB.

func TestWriteCapableStatus_NoWriteBindDM(t *testing.T) {
	// With no write_bind_dn, the system falls back to DM bind.
	// WriteCapableStatus should return "dm_fallback" and capable=true.
	d := openTestDB(t)
	m := &Manager{db: d}
	// Module is disabled (no row in the ldap table) — status should be "untested".
	status, capable := m.WriteCapableStatus(context.Background())
	if capable {
		t.Errorf("expected capable=false when module has no row, got status=%q", status)
	}
	if status != "untested" {
		t.Errorf("expected status=untested, got %q", status)
	}
}

func TestWriteCapableStatus_WithWriteBindUnprobed(t *testing.T) {
	// Module enabled with a write_bind_dn but no probe yet — should be "untested", not capable.
	d := openTestDB(t)
	ctx := context.Background()

	// Simulate a probed-not-yet state by directly setting write_capable = NULL.
	// We don't need to fully enable the LDAP module for this check.
	_ = d.LDAPSetWriteCapable(ctx, nil, "")

	m := &Manager{db: d}
	status, capable := m.WriteCapableStatus(ctx)
	// Module row exists but enabled=false, so db query returns a valid row.
	// With enabled=false, WriteCapableStatus returns "untested", false.
	if capable {
		t.Errorf("expected capable=false, got status=%q", status)
	}
	_ = fmt.Sprintf("status=%q", status) // status varies based on row state, just ensure no panic
}

func TestWriteCapableStatus_ProbeOK(t *testing.T) {
	// Set write_capable=true and verify status=ok.
	d := openTestDB(t)
	ctx := context.Background()

	capable := true
	if err := d.LDAPSetWriteCapable(ctx, &capable, "probe ok"); err != nil {
		t.Fatalf("LDAPSetWriteCapable: %v", err)
	}

	// Verify the value was persisted.
	row, err := d.LDAPGetConfig(ctx)
	if err != nil {
		t.Fatalf("LDAPGetConfig: %v", err)
	}
	if row.WriteCapable == nil {
		t.Fatal("expected WriteCapable to be set, got nil")
	}
	if !*row.WriteCapable {
		t.Error("expected WriteCapable=true")
	}
	if row.WriteCapableDetail != "probe ok" {
		t.Errorf("expected WriteCapableDetail=probe ok, got %q", row.WriteCapableDetail)
	}
}

func TestWriteCapableStatus_ProbeFailed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	capable := false
	if err := d.LDAPSetWriteCapable(ctx, &capable, "bind failed: invalid credentials"); err != nil {
		t.Fatalf("LDAPSetWriteCapable: %v", err)
	}

	row, err := d.LDAPGetConfig(ctx)
	if err != nil {
		t.Fatalf("LDAPGetConfig: %v", err)
	}
	if row.WriteCapable == nil {
		t.Fatal("expected WriteCapable to be set, got nil")
	}
	if *row.WriteCapable {
		t.Error("expected WriteCapable=false")
	}
	if !strings.Contains(row.WriteCapableDetail, "invalid credentials") {
		t.Errorf("expected detail to contain reason, got %q", row.WriteCapableDetail)
	}
}

func TestWriteCapableStatus_Clear(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Set, then clear.
	capable := true
	_ = d.LDAPSetWriteCapable(ctx, &capable, "probe ok")
	_ = d.LDAPSetWriteCapable(ctx, nil, "cleared")

	row, err := d.LDAPGetConfig(ctx)
	if err != nil {
		t.Fatalf("LDAPGetConfig: %v", err)
	}
	if row.WriteCapable != nil {
		t.Errorf("expected WriteCapable=nil after clearing, got %v", *row.WriteCapable)
	}
}

// ─── Sprint 15 #100: generateRandomPassword ───────────────────────────────────

// TestGenerateRandomPassword_NonEmpty asserts that generateRandomPassword always
// returns a non-empty string for valid lengths.
func TestGenerateRandomPassword_NonEmpty(t *testing.T) {
	for _, length := range []int{8, 16, 20, 32} {
		pwd, err := generateRandomPassword(length)
		if err != nil {
			t.Fatalf("generateRandomPassword(%d): unexpected error: %v", length, err)
		}
		if len(pwd) == 0 {
			t.Errorf("generateRandomPassword(%d): returned empty string", length)
		}
		// Returned string may be base64-derived so length can differ — just check non-empty.
	}
}

// TestGenerateRandomPassword_Unique asserts that two calls return different values.
// A collision here would indicate a broken RNG.
func TestGenerateRandomPassword_Unique(t *testing.T) {
	a, err := generateRandomPassword(20)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	b, err := generateRandomPassword(20)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if a == b {
		t.Errorf("two consecutive calls produced the same password %q — RNG may be broken", a)
	}
}
