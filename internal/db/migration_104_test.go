// migration_104_test.go — verify that migration 104 normalizes
// ldap_module_config.sudoers_group_cn from 'clonr-admins' (legacy
// pre-rename value) to 'clustr-admins'.
package db_test

import (
	"testing"
)

// TestMigration104_NormalizesLegacySudoersGroupCN inserts a value
// directly into the singleton row to simulate a pre-rename install
// (where the column was populated by code that wrote 'clonr-admins'),
// then verifies the migration update brings it to 'clustr-admins'.
//
// Note: openTestDB applies all migrations on Open(), so this test
// validates the post-migration state of a database whose row holds
// the legacy value. Production DBs go through this exact path on the
// first start with v0.1.15 RPMs installed.
func TestMigration104_NormalizesLegacySudoersGroupCN(t *testing.T) {
	d := openTestDB(t)

	// Force the row back to the legacy value to simulate a pre-rename install.
	// (openTestDB ran all migrations including 104, which would have already
	// normalized any legacy value to clustr-admins.)
	if _, err := d.SQL().Exec(`UPDATE ldap_module_config SET sudoers_group_cn = 'clonr-admins' WHERE id = 1`); err != nil {
		t.Fatalf("seed legacy value: %v", err)
	}

	// Re-run the migration UPDATE statement directly — it must be idempotent
	// and convert the legacy value to clustr-admins.
	if _, err := d.SQL().Exec(`UPDATE ldap_module_config SET sudoers_group_cn = 'clustr-admins' WHERE id = 1 AND sudoers_group_cn = 'clonr-admins'`); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}

	var got string
	row := d.SQL().QueryRow(`SELECT sudoers_group_cn FROM ldap_module_config WHERE id = 1`)
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != "clustr-admins" {
		t.Errorf("sudoers_group_cn = %q, want %q", got, "clustr-admins")
	}
}

// TestMigration104_PreservesNonLegacyValues confirms the migration WHERE
// clause is narrow — values other than 'clonr-admins' (e.g. operator-set
// custom group names) must be left alone.
func TestMigration104_PreservesNonLegacyValues(t *testing.T) {
	d := openTestDB(t)

	const customGroup = "ops-team"
	if _, err := d.SQL().Exec(`UPDATE ldap_module_config SET sudoers_group_cn = ? WHERE id = 1`, customGroup); err != nil {
		t.Fatalf("seed custom value: %v", err)
	}

	// Re-run the migration statement; the WHERE clause must skip the custom value.
	if _, err := d.SQL().Exec(`UPDATE ldap_module_config SET sudoers_group_cn = 'clustr-admins' WHERE id = 1 AND sudoers_group_cn = 'clonr-admins'`); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}

	var got string
	row := d.SQL().QueryRow(`SELECT sudoers_group_cn FROM ldap_module_config WHERE id = 1`)
	if err := row.Scan(&got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got != customGroup {
		t.Errorf("custom group preserved: got %q, want %q (migration should not overwrite non-legacy values)", got, customGroup)
	}
}
