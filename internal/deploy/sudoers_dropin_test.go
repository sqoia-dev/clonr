// sudoers_dropin_test.go — pin the v0.1.15 sudoers drop-in contract:
//   - the file is named after the configured GroupCN (clustr-admins post-rename)
//   - any legacy /etc/sudoers.d/clonr-admins is removed during the same write
//
// Regression context: GAP-S18-2 (#115) renamed the LDAP group from
// clonr-admins to clustr-admins, but installs that ran the LDAP enable
// flow before the rename had ldap_module_config.sudoers_group_cn still
// holding the legacy value. As a result, the deploy pipeline kept
// writing /etc/sudoers.d/clonr-admins on every reimage, and the file
// referenced a group CN that no longer exists in LDAP — sudo broken
// for clustr-admins members. Migration 104 fixes the DB column; this
// test pins the code-side contract.
package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sqoia-dev/clustr/pkg/api"
)

func TestWriteSudoersDropin_UsesClustrAdminsFilename(t *testing.T) {
	root := t.TempDir()
	cfg := &api.SudoersNodeConfig{GroupCN: "clustr-admins", NoPasswd: true}

	if err := writeSudoersDropin(root, cfg); err != nil {
		t.Fatalf("writeSudoersDropin: %v", err)
	}

	want := filepath.Join(root, "etc", "sudoers.d", "clustr-admins")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s to exist: %v", want, err)
	}

	// The legacy filename must NOT be created.
	legacy := filepath.Join(root, "etc", "sudoers.d", "clonr-admins")
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy %s should not exist; stat err = %v", legacy, err)
	}

	// The rule body references the configured group, NOT the legacy one.
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read drop-in: %v", err)
	}
	if got := string(body); got != "%clustr-admins ALL=(ALL) NOPASSWD:ALL\n" {
		t.Errorf("sudoers content = %q, want %q", got, "%clustr-admins ALL=(ALL) NOPASSWD:ALL\n")
	}
	// And explicitly does not contain the legacy CN.
	if string(body) == "%clonr-admins ALL=(ALL) NOPASSWD:ALL\n" {
		t.Errorf("sudoers content uses legacy clonr-admins — rename incomplete")
	}
}

func TestWriteSudoersDropin_RemovesLegacyClonrAdminsFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "sudoers.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Plant a stale legacy file as if the node was deployed before the rename.
	legacy := filepath.Join(dir, "clonr-admins")
	if err := os.WriteFile(legacy, []byte("%clonr-admins ALL=(ALL) NOPASSWD:ALL\n"), 0o440); err != nil {
		t.Fatalf("plant legacy: %v", err)
	}

	cfg := &api.SudoersNodeConfig{GroupCN: "clustr-admins", NoPasswd: true}
	if err := writeSudoersDropin(root, cfg); err != nil {
		t.Fatalf("writeSudoersDropin: %v", err)
	}

	// Legacy file must be gone.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy /etc/sudoers.d/clonr-admins should have been removed; stat err = %v", err)
	}

	// New file must exist.
	want := filepath.Join(dir, "clustr-admins")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("new /etc/sudoers.d/clustr-admins should exist: %v", err)
	}
}

func TestWriteSudoersDropin_NoLegacyCleanupWhenGroupCNIsClonrAdmins(t *testing.T) {
	// Defensive: if for some reason GroupCN is still "clonr-admins" (shouldn't
	// happen post-migration 104, but the writer must not delete its own output),
	// the cleanup branch must skip the unlink.
	root := t.TempDir()
	cfg := &api.SudoersNodeConfig{GroupCN: "clonr-admins", NoPasswd: true}

	if err := writeSudoersDropin(root, cfg); err != nil {
		t.Fatalf("writeSudoersDropin: %v", err)
	}

	want := filepath.Join(root, "etc", "sudoers.d", "clonr-admins")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file %s should exist (writer must not delete its own output): %v", want, err)
	}
}
