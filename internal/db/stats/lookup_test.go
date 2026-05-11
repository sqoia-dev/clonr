package stats

// lookup_test.go — Sprint 42 MIGRATE-CHAIN tests for the stats migration runner.
//
// Tests:
//   - File on disk but not in manifest → rejected.
//   - Manifest entry without file → rejected.
//   - Unsatisfied requires → rejected.
//   - Direct dependency cycle A→B→A → rejected.
//   - Transitive dependency cycle A→B→C→A → rejected.
//   - Duplicate migration IDs → rejected.
//   - Happy path: clean chain passes validation.
//
// These tests use the unexported validateWithFakeDisk helper defined below,
// which exercises the same code paths as validateLookup() but with an
// injected disk-file set instead of reading the embedded FS.

import (
	"fmt"
	"testing"
)

// happyEntries returns a minimal valid 3-entry lookup chain for testing.
func happyEntries() []LookupEntry {
	return []LookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "first", AppliedTo: "stats", Requires: []int{}},
		{ID: 2, Filename: "002_b.sql", Description: "second", AppliedTo: "stats", Requires: []int{1}},
		{ID: 3, Filename: "003_c.sql", Description: "third", AppliedTo: "stats", Requires: []int{1, 2}},
	}
}

// fakeDiskFiles builds a filename-set simulating .sql files on disk.
func fakeDiskFiles(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// validateWithFakeDisk runs the same validation logic as validateLookup but
// takes a pre-built disk-file set instead of reading the embedded FS. This
// lets us unit-test the validation and cycle-detection logic without creating
// actual SQL files.
func validateWithFakeDisk(entries []LookupEntry, disk map[string]bool) error {
	manifestFiles := make(map[string]bool, len(entries))
	seenIDs := make(map[int]bool, len(entries))
	for _, e := range entries {
		if e.Filename == "" {
			return fmt.Errorf("entry with id=%d has empty filename", e.ID)
		}
		if seenIDs[e.ID] {
			return fmt.Errorf("duplicate migration id %d", e.ID)
		}
		seenIDs[e.ID] = true
		if manifestFiles[e.Filename] {
			return fmt.Errorf("duplicate filename %q in lookup.yml", e.Filename)
		}
		manifestFiles[e.Filename] = true
	}
	for name := range disk {
		if !manifestFiles[name] {
			return fmt.Errorf("migration file %q exists on disk but not in lookup.yml", name)
		}
	}
	for name := range manifestFiles {
		if !disk[name] {
			return fmt.Errorf("lookup.yml references %q but no such file exists", name)
		}
	}
	for _, e := range entries {
		for _, reqID := range e.Requires {
			if !seenIDs[reqID] {
				return fmt.Errorf("migration %d (%s) requires id %d which is not declared", e.ID, e.Filename, reqID)
			}
		}
	}
	return detectCycles(entries, seenIDs)
}

// TestLookup_HappyPath verifies a clean 3-entry chain passes validation.
func TestLookup_HappyPath(t *testing.T) {
	entries := happyEntries()
	disk := fakeDiskFiles("001_a.sql", "002_b.sql", "003_c.sql")
	if err := validateWithFakeDisk(entries, disk); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// TestLookup_FileOnDiskNotInManifest verifies drift detection when a file
// exists on disk but is missing from lookup.yml.
func TestLookup_FileOnDiskNotInManifest(t *testing.T) {
	entries := happyEntries()
	disk := fakeDiskFiles("001_a.sql", "002_b.sql", "003_c.sql", "004_extra.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for file on disk but not in lookup")
	}
	t.Logf("correctly rejected: %v", err)
}

// TestLookup_ManifestEntryMissingFile verifies drift detection when lookup.yml
// references a file that does not exist on disk.
func TestLookup_ManifestEntryMissingFile(t *testing.T) {
	entries := happyEntries()
	// Disk is missing 003_c.sql
	disk := fakeDiskFiles("001_a.sql", "002_b.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for manifest entry without file")
	}
	t.Logf("correctly rejected: %v", err)
}

// TestLookup_UnsatisfiedRequires verifies that a requires referencing an
// undeclared ID is rejected.
func TestLookup_UnsatisfiedRequires(t *testing.T) {
	entries := []LookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "a", Requires: []int{}},
		{ID: 2, Filename: "002_b.sql", Description: "b", Requires: []int{99}}, // 99 does not exist
	}
	disk := fakeDiskFiles("001_a.sql", "002_b.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for unsatisfied requires")
	}
	t.Logf("correctly rejected: %v", err)
}

// TestLookup_DirectCycle verifies cycle detection for A→B→A.
func TestLookup_DirectCycle(t *testing.T) {
	entries := []LookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "a", Requires: []int{2}},
		{ID: 2, Filename: "002_b.sql", Description: "b", Requires: []int{1}},
	}
	disk := fakeDiskFiles("001_a.sql", "002_b.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	t.Logf("direct cycle detected: %v", err)
}

// TestLookup_TransitiveCycle verifies cycle detection for A→B→C→A.
func TestLookup_TransitiveCycle(t *testing.T) {
	entries := []LookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "a", Requires: []int{3}},
		{ID: 2, Filename: "002_b.sql", Description: "b", Requires: []int{1}},
		{ID: 3, Filename: "003_c.sql", Description: "c", Requires: []int{2}},
	}
	disk := fakeDiskFiles("001_a.sql", "002_b.sql", "003_c.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected transitive cycle detection error")
	}
	t.Logf("transitive cycle detected: %v", err)
}

// TestLookup_DuplicateID verifies that duplicate migration IDs are rejected.
func TestLookup_DuplicateID(t *testing.T) {
	entries := []LookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "a", Requires: []int{}},
		{ID: 1, Filename: "002_b.sql", Description: "b", Requires: []int{}},
	}
	disk := fakeDiskFiles("001_a.sql", "002_b.sql")
	err := validateWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
	t.Logf("duplicate ID rejected: %v", err)
}

// TestLookup_EmptyFilename verifies that an entry with an empty filename is rejected.
func TestLookup_EmptyFilename(t *testing.T) {
	entries := []LookupEntry{
		{ID: 1, Filename: "", Description: "missing name", Requires: []int{}},
	}
	err := validateWithFakeDisk(entries, fakeDiskFiles())
	if err == nil {
		t.Fatal("expected error for empty filename")
	}
	t.Logf("empty filename rejected: %v", err)
}

// TestLookup_ActualManifest verifies the real stats lookup.yml validates cleanly
// against the actual embedded migration files.
func TestLookup_ActualManifest(t *testing.T) {
	entries, err := loadLookup(migrationsFS)
	if err != nil {
		t.Fatalf("loadLookup: %v", err)
	}
	if err := validateLookup(migrationsFS, entries); err != nil {
		t.Fatalf("validateLookup: %v", err)
	}
	t.Logf("stats lookup.yml: %d entries, all valid", len(entries))
}
