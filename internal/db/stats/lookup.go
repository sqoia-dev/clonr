package stats

// lookup.go — parses and validates the migrations/lookup.yml manifest for the
// stats migration chain.
//
// The lookup.yml contract:
//   - Every .sql file in migrations/ must have exactly one entry in lookup.yml.
//   - Every entry in lookup.yml must have a corresponding .sql file.
//   - Migration IDs within the stats chain must be unique.
//   - Requires lists must not contain cycles.
//   - A startup failure is returned for any of the above violations.

import (
	"embed"
	"fmt"

	"go.yaml.in/yaml/v2"
)

// LookupEntry is one migration entry in lookup.yml.
type LookupEntry struct {
	ID          int    `yaml:"id"`
	Filename    string `yaml:"filename"`
	Description string `yaml:"description"`
	AppliedTo   string `yaml:"applied_to"`
	Requires    []int  `yaml:"requires"`
}

// lookupFile is the top-level structure of lookup.yml.
type lookupFile struct {
	Migrations []LookupEntry `yaml:"migrations"`
}

// loadLookup reads and parses migrations/lookup.yml from the given embed.FS.
// Returns the ordered slice of entries exactly as written in the file.
func loadLookup(fsys embed.FS) ([]LookupEntry, error) {
	data, err := fsys.ReadFile("migrations/lookup.yml")
	if err != nil {
		return nil, fmt.Errorf("lookup: read lookup.yml: %w", err)
	}
	var f lookupFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("lookup: parse lookup.yml: %w", err)
	}
	if len(f.Migrations) == 0 {
		return nil, fmt.Errorf("lookup: lookup.yml has no migrations listed")
	}
	return f.Migrations, nil
}

// validateLookup checks that the set of .sql files on disk matches the set
// of filenames declared in the manifest, and that IDs are unique and requires
// lists are acyclic.
//
// Returns an error describing the first violation found; the runner treats
// any error from this function as a fatal startup failure.
func validateLookup(fsys embed.FS, entries []LookupEntry) error {
	// Build manifest filename set and check for duplicate IDs.
	manifestFiles := make(map[string]bool, len(entries))
	seenIDs := make(map[int]bool, len(entries))
	for _, e := range entries {
		if e.Filename == "" {
			return fmt.Errorf("lookup: entry with id=%d has empty filename", e.ID)
		}
		if seenIDs[e.ID] {
			return fmt.Errorf("lookup: duplicate migration id %d", e.ID)
		}
		seenIDs[e.ID] = true
		if manifestFiles[e.Filename] {
			return fmt.Errorf("lookup: duplicate filename %q in lookup.yml", e.Filename)
		}
		manifestFiles[e.Filename] = true
	}

	// Build set of .sql files actually present on disk.
	diskFiles, err := readDirSQLFiles(fsys, "migrations")
	if err != nil {
		return fmt.Errorf("lookup: enumerate migrations dir: %w", err)
	}

	// Every file on disk must be in the manifest.
	for name := range diskFiles {
		if !manifestFiles[name] {
			return fmt.Errorf("lookup: migration file %q exists on disk but is not listed in lookup.yml — add it or remove it", name)
		}
	}

	// Every manifest entry must have a file on disk.
	for name := range manifestFiles {
		if !diskFiles[name] {
			return fmt.Errorf("lookup: lookup.yml references %q but no such file exists in migrations/ — remove the entry or create the file", name)
		}
	}

	// Check that all requires IDs exist in the manifest.
	for _, e := range entries {
		for _, reqID := range e.Requires {
			if !seenIDs[reqID] {
				return fmt.Errorf("lookup: migration %d (%s) requires id %d which is not declared in lookup.yml",
					e.ID, e.Filename, reqID)
			}
		}
	}

	// Cycle detection using DFS on the requires graph.
	if err := detectCycles(entries, seenIDs); err != nil {
		return err
	}

	return nil
}

// detectCycles performs a DFS-based topological sort over the requires graph
// to detect dependency cycles.
func detectCycles(entries []LookupEntry, seenIDs map[int]bool) error {
	// Build adjacency list: id → []requires_id
	deps := make(map[int][]int, len(entries))
	for _, e := range entries {
		deps[e.ID] = e.Requires
	}

	const (
		colorWhite = 0 // unvisited
		colorGray  = 1 // in current DFS path (potential cycle)
		colorBlack = 2 // fully visited (no cycle)
	)
	color := make(map[int]int, len(entries))

	var visit func(id int) error
	visit = func(id int) error {
		if color[id] == colorBlack {
			return nil
		}
		if color[id] == colorGray {
			return fmt.Errorf("lookup: dependency cycle detected involving migration id %d", id)
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
