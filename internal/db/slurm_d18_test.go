// slurm_d18_test.go — tests for D18: is_clustr_default column (migration 052)
// and the updated SlurmSaveConfigVersion signature.
package db_test

import (
	"context"
	"testing"
)

// TestMigration052_IsClustrDefaultColumn verifies that the migration added the
// is_clustr_default column and that its default value is 0 for existing rows.
func TestMigration052_IsClustrDefaultColumn(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Write a row with isClustrDefault=false (operator write path).
	_, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "ClusterName=test\n",
		"test-actor", "initial write", false)
	if err != nil {
		t.Fatalf("SlurmSaveConfigVersion: %v", err)
	}

	row, err := d.SlurmGetCurrentConfig(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}

	if row.IsClustrDefault {
		t.Errorf("IsClustrDefault: got true, want false for operator-written row")
	}
	if row.Version != 1 {
		t.Errorf("Version: got %d, want 1", row.Version)
	}
}

// TestSlurmSaveConfigVersion_ClustrDefaultTrue verifies that seeded rows
// (isClustrDefault=true) are stored with the flag set.
func TestSlurmSaveConfigVersion_ClustrDefaultTrue(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	ver, err := d.SlurmSaveConfigVersion(ctx, "cgroup.conf", "CgroupPlugin=cgroup/v2\n",
		"clustr-system", "Initial default template", true)
	if err != nil {
		t.Fatalf("SlurmSaveConfigVersion: %v", err)
	}
	if ver != 1 {
		t.Errorf("version: got %d, want 1", ver)
	}

	row, err := d.SlurmGetCurrentConfig(ctx, "cgroup.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}
	if !row.IsClustrDefault {
		t.Errorf("IsClustrDefault: got false, want true for clustr-seeded row")
	}
}

// TestSlurmSaveConfigVersion_VersionBump verifies that successive saves bump the
// version monotonically regardless of the isClustrDefault flag.
func TestSlurmSaveConfigVersion_VersionBump(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Version 1: seeded by clustr.
	v1, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "v1 content",
		"clustr-system", "seed", true)
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	// Version 2: operator edit (clears the flag).
	v2, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "v2 content — operator edit",
		"operator-key", "manual fix", false)
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	// Version 3: reseed (sets the flag again).
	v3, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "v3 content — reseeded",
		"clustr-system", "reseed-defaults", true)
	if err != nil {
		t.Fatalf("v3: %v", err)
	}

	if v1 != 1 || v2 != 2 || v3 != 3 {
		t.Errorf("versions: got %d/%d/%d, want 1/2/3", v1, v2, v3)
	}

	// Current config should be v3 (IsClustrDefault=true).
	row, err := d.SlurmGetCurrentConfig(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmGetCurrentConfig: %v", err)
	}
	if row.Version != 3 {
		t.Errorf("current version: got %d, want 3", row.Version)
	}
	if !row.IsClustrDefault {
		t.Errorf("IsClustrDefault: got false, want true for reseeded v3")
	}

	// History should contain all three rows.
	history, err := d.SlurmListConfigHistory(ctx, "slurm.conf")
	if err != nil {
		t.Fatalf("SlurmListConfigHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len: got %d, want 3", len(history))
	}
	// Newest first — v3 should be first.
	if !history[0].IsClustrDefault {
		t.Errorf("history[0] IsClustrDefault: got false, want true")
	}
	// v2 (operator edit) should have false.
	if history[1].IsClustrDefault {
		t.Errorf("history[1] IsClustrDefault: got true, want false (operator row)")
	}
	// v1 (seed) should have true.
	if !history[2].IsClustrDefault {
		t.Errorf("history[2] IsClustrDefault: got false, want true (seed row)")
	}
}

// TestSlurmListCurrentConfigs_IsClustrDefault verifies that SlurmListCurrentConfigs
// correctly surfaces the is_clustr_default flag on current-version rows.
func TestSlurmListCurrentConfigs_IsClustrDefault(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// slurm.conf: seeded (default), then operator-edited (cleared).
	if _, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "content-v1", "clustr-system", "seed", true); err != nil {
		t.Fatalf("seed slurm.conf: %v", err)
	}
	if _, err := d.SlurmSaveConfigVersion(ctx, "slurm.conf", "content-v2", "operator", "edit", false); err != nil {
		t.Fatalf("operator edit slurm.conf: %v", err)
	}

	// cgroup.conf: seeded and never edited.
	if _, err := d.SlurmSaveConfigVersion(ctx, "cgroup.conf", "cgroup-content", "clustr-system", "seed", true); err != nil {
		t.Fatalf("seed cgroup.conf: %v", err)
	}

	rows, err := d.SlurmListCurrentConfigs(ctx)
	if err != nil {
		t.Fatalf("SlurmListCurrentConfigs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}

	byFile := make(map[string]bool)
	for _, r := range rows {
		byFile[r.Filename] = r.IsClustrDefault
	}

	if byFile["slurm.conf"] {
		t.Errorf("slurm.conf IsClustrDefault: got true, want false (operator-edited current version)")
	}
	if !byFile["cgroup.conf"] {
		t.Errorf("cgroup.conf IsClustrDefault: got false, want true (clustr-seeded, never edited)")
	}
}
