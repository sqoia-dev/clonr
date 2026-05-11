package db

// lookup_test.go — Sprint 42 MIGRATE-CHAIN tests for the clustr.db migration runner.
//
// Tests the validateDBLookup logic using a fake-disk helper.

import (
	"fmt"
	"testing"
)

// validateDBLookupWithFakeDisk runs the same validation as validateDBLookup but
// takes a pre-built disk-file set instead of reading the embedded FS.
func validateDBLookupWithFakeDisk(entries []DBLookupEntry, disk map[string]bool) error {
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
	return dbDetectCycles(entries, seenIDs)
}

func fakeDBDisk(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func happyDBEntries() []DBLookupEntry {
	return []DBLookupEntry{
		{ID: 1, Filename: "001_a.sql", Description: "first", Requires: []int{}},
		{ID: 2, Filename: "002_b.sql", Description: "second", Requires: []int{1}},
		{ID: 3, Filename: "003_c.sql", Description: "third", Requires: []int{1, 2}},
	}
}

func TestDBLookup_HappyPath(t *testing.T) {
	entries := happyDBEntries()
	disk := fakeDBDisk("001_a.sql", "002_b.sql", "003_c.sql")
	if err := validateDBLookupWithFakeDisk(entries, disk); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestDBLookup_FileOnDiskNotInManifest(t *testing.T) {
	entries := happyDBEntries()
	disk := fakeDBDisk("001_a.sql", "002_b.sql", "003_c.sql", "004_extra.sql")
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for file on disk but not in lookup")
	}
	t.Logf("correctly rejected: %v", err)
}

func TestDBLookup_ManifestEntryMissingFile(t *testing.T) {
	entries := happyDBEntries()
	disk := fakeDBDisk("001_a.sql", "002_b.sql") // missing 003
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for manifest entry without file")
	}
	t.Logf("correctly rejected: %v", err)
}

func TestDBLookup_UnsatisfiedRequires(t *testing.T) {
	entries := []DBLookupEntry{
		{ID: 1, Filename: "001_a.sql", Requires: []int{}},
		{ID: 2, Filename: "002_b.sql", Requires: []int{99}}, // 99 not declared
	}
	disk := fakeDBDisk("001_a.sql", "002_b.sql")
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for unsatisfied requires")
	}
	t.Logf("correctly rejected: %v", err)
}

func TestDBLookup_DirectCycle(t *testing.T) {
	entries := []DBLookupEntry{
		{ID: 1, Filename: "001_a.sql", Requires: []int{2}},
		{ID: 2, Filename: "002_b.sql", Requires: []int{1}},
	}
	disk := fakeDBDisk("001_a.sql", "002_b.sql")
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
	t.Logf("direct cycle detected: %v", err)
}

func TestDBLookup_TransitiveCycle(t *testing.T) {
	entries := []DBLookupEntry{
		{ID: 1, Filename: "001_a.sql", Requires: []int{3}},
		{ID: 2, Filename: "002_b.sql", Requires: []int{1}},
		{ID: 3, Filename: "003_c.sql", Requires: []int{2}},
	}
	disk := fakeDBDisk("001_a.sql", "002_b.sql", "003_c.sql")
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected transitive cycle detection error")
	}
	t.Logf("transitive cycle detected: %v", err)
}

func TestDBLookup_DuplicateID(t *testing.T) {
	entries := []DBLookupEntry{
		{ID: 1, Filename: "001_a.sql", Requires: []int{}},
		{ID: 1, Filename: "002_b.sql", Requires: []int{}},
	}
	disk := fakeDBDisk("001_a.sql", "002_b.sql")
	err := validateDBLookupWithFakeDisk(entries, disk)
	if err == nil {
		t.Fatal("expected error for duplicate ID")
	}
	t.Logf("duplicate ID rejected: %v", err)
}

// TestDBLookup_ActualManifest verifies the real lookup.yml validates cleanly
// against the actual embedded migration files.
func TestDBLookup_ActualManifest(t *testing.T) {
	entries, err := loadDBLookup()
	if err != nil {
		t.Fatalf("loadDBLookup: %v", err)
	}
	if err := validateDBLookup(entries); err != nil {
		t.Fatalf("validateDBLookup: %v", err)
	}
	t.Logf("actual lookup.yml: %d entries, all valid", len(entries))
}
