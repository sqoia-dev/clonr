// slurm_dep_matrix_test.go — tests for SlurmSeedDepMatrix / SlurmListDepMatrix
// duplicate-prevention behaviour (migration 117 + query GROUP BY guard).
package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sqoia-dev/clustr/internal/db"
)

// TestSlurmListDepMatrix_NoDuplicatesAfterRepeatedSeed asserts that calling
// SlurmSeedDepMatrix multiple times with identical content rows results in
// exactly one row per distinct tuple in the output of SlurmListDepMatrix.
func TestSlurmListDepMatrix_NoDuplicatesAfterRepeatedSeed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	entry := db.SlurmDepMatrixRow{
		ID:              uuid.New().String(),
		SlurmVersionMin: "24.05.0",
		SlurmVersionMax: "25.00.0",
		DepName:         "hwloc",
		DepVersionMin:   "2.9.0",
		DepVersionMax:   "3.0.0",
		Source:          "bundled",
		CreatedAt:       time.Now().Unix(),
	}

	// Seed the same content six times (simulating six server restarts).
	// Each call uses a new UUID for id, so INSERT OR IGNORE on the PK alone
	// would admit all six rows.  After migration 117 the content UNIQUE
	// constraint must collapse them to one.
	for i := 0; i < 6; i++ {
		clone := entry
		clone.ID = uuid.New().String() // different id each time
		if err := d.SlurmSeedDepMatrix(ctx, []db.SlurmDepMatrixRow{clone}); err != nil {
			t.Fatalf("seed round %d: %v", i+1, err)
		}
	}

	rows, err := d.SlurmListDepMatrix(ctx)
	if err != nil {
		t.Fatalf("SlurmListDepMatrix: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row after 6 identical seeds, got %d", len(rows))
	}
	if len(rows) > 0 {
		if rows[0].DepName != "hwloc" {
			t.Errorf("dep_name: got %q, want %q", rows[0].DepName, "hwloc")
		}
	}
}

// TestSlurmListDepMatrix_DistinctContentAllowed asserts that two entries with
// different content (different dep_version_min) both appear in the result.
func TestSlurmListDepMatrix_DistinctContentAllowed(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	entries := []db.SlurmDepMatrixRow{
		{
			ID:              uuid.New().String(),
			SlurmVersionMin: "24.05.0",
			SlurmVersionMax: "25.00.0",
			DepName:         "hwloc",
			DepVersionMin:   "2.9.0",
			DepVersionMax:   "3.0.0",
			Source:          "bundled",
			CreatedAt:       time.Now().Unix(),
		},
		{
			ID:              uuid.New().String(),
			SlurmVersionMin: "24.05.0",
			SlurmVersionMax: "25.00.0",
			DepName:         "pmix",
			DepVersionMin:   "4.2.0",
			DepVersionMax:   "5.0.0",
			Source:          "bundled",
			CreatedAt:       time.Now().Unix(),
		},
	}

	if err := d.SlurmSeedDepMatrix(ctx, entries); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Seed again — both should still yield exactly 2 rows.
	if err := d.SlurmSeedDepMatrix(ctx, entries); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	rows, err := d.SlurmListDepMatrix(ctx)
	if err != nil {
		t.Fatalf("SlurmListDepMatrix: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 distinct rows, got %d", len(rows))
	}
}
