package db

// lookup.go — parses and validates the migrations/lookup.yml manifest for the
// clustr.db migration chain.
//
// The lookup.yml contract:
//   - Every .sql file in migrations/ must have exactly one entry in lookup.yml.
//   - Every entry in lookup.yml must have a corresponding .sql file.
//   - Migration IDs within the chain must be unique.
//   - Requires lists must not contain cycles.
//   - A startup failure is returned for any of the above violations.
//
// The migration runner in migrate() uses loadDBLookup + validateDBLookup to
// validate the manifest before applying any migrations. If validation fails,
// Open() returns an error and the server refuses to start.

import (
	"fmt"

	"go.yaml.in/yaml/v2"
)

// DBLookupEntry is one migration entry in migrations/lookup.yml.
type DBLookupEntry struct {
	ID          int    `yaml:"id"`
	Filename    string `yaml:"filename"`
	Description string `yaml:"description"`
	AppliedTo   string `yaml:"applied_to"`
	Requires    []int  `yaml:"requires"`
}

// dbLookupFile is the top-level YAML structure.
type dbLookupFile struct {
	Migrations []DBLookupEntry `yaml:"migrations"`
}

// loadDBLookup reads and parses migrations/lookup.yml from migrationsFS.
func loadDBLookup() ([]DBLookupEntry, error) {
	data, err := migrationsFS.ReadFile("migrations/lookup.yml")
	if err != nil {
		return nil, fmt.Errorf("db: lookup: read lookup.yml: %w", err)
	}
	var f dbLookupFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("db: lookup: parse lookup.yml: %w", err)
	}
	if len(f.Migrations) == 0 {
		return nil, fmt.Errorf("db: lookup: lookup.yml has no migrations listed")
	}
	return f.Migrations, nil
}

// validateDBLookup checks that the .sql file set on disk matches the manifest
// and that IDs are unique and requires lists are acyclic.
func validateDBLookup(entries []DBLookupEntry) error {
	// Build manifest filename set and check for duplicate IDs.
	manifestFiles := make(map[string]bool, len(entries))
	seenIDs := make(map[int]bool, len(entries))
	for _, e := range entries {
		if e.Filename == "" {
			return fmt.Errorf("db: lookup: entry with id=%d has empty filename", e.ID)
		}
		if seenIDs[e.ID] {
			return fmt.Errorf("db: lookup: duplicate migration id %d", e.ID)
		}
		seenIDs[e.ID] = true
		if manifestFiles[e.Filename] {
			return fmt.Errorf("db: lookup: duplicate filename %q in lookup.yml", e.Filename)
		}
		manifestFiles[e.Filename] = true
	}

	// Build set of .sql files on disk (via the embed FS).
	diskFiles, err := dbReadDirSQLFiles()
	if err != nil {
		return fmt.Errorf("db: lookup: enumerate migrations dir: %w", err)
	}

	// Every file on disk must be in the manifest.
	for name := range diskFiles {
		if !manifestFiles[name] {
			return fmt.Errorf("db: lookup: migration file %q exists on disk but is not listed in lookup.yml — add it or remove it", name)
		}
	}

	// Every manifest entry must have a file on disk.
	for name := range manifestFiles {
		if !diskFiles[name] {
			return fmt.Errorf("db: lookup: lookup.yml references %q but no such file exists in migrations/ — remove the entry or create the file", name)
		}
	}

	// All requires IDs must be declared.
	for _, e := range entries {
		for _, reqID := range e.Requires {
			if !seenIDs[reqID] {
				return fmt.Errorf("db: lookup: migration %d (%s) requires id %d which is not declared in lookup.yml",
					e.ID, e.Filename, reqID)
			}
		}
	}

	// Cycle detection.
	return dbDetectCycles(entries, seenIDs)
}

// dbDetectCycles performs DFS-based cycle detection on the requires graph.
func dbDetectCycles(entries []DBLookupEntry, seenIDs map[int]bool) error {
	deps := make(map[int][]int, len(entries))
	for _, e := range entries {
		deps[e.ID] = e.Requires
	}

	const (
		colorWhite = 0
		colorGray  = 1
		colorBlack = 2
	)
	color := make(map[int]int, len(entries))

	var visit func(id int) error
	visit = func(id int) error {
		if color[id] == colorBlack {
			return nil
		}
		if color[id] == colorGray {
			return fmt.Errorf("db: lookup: dependency cycle detected involving migration id %d", id)
		}
		color[id] = colorGray
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = colorBlack
		return nil
	}

	for id := range seenIDs {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// dbReadDirSQLFiles returns a set of .sql filenames in the embedded migrations dir.
func dbReadDirSQLFiles() (map[string]bool, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	out := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && len(name) > 4 && name[len(name)-4:] == ".sql" {
			out[name] = true
		}
	}
	return out, nil
}
