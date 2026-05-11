-- 117: deduplicate slurm_dep_matrix and add unique constraint.
--
-- Background: seedDepMatrix() generates a fresh UUID on every call and uses
-- INSERT OR IGNORE, which only guards on the primary key (id).  Each server
-- restart therefore inserts a duplicate set of rows.  This migration:
--   1. Removes the duplicates, keeping the oldest row per content tuple.
--   2. Adds a UNIQUE constraint on the content tuple so future seeds can use
--      INSERT OR IGNORE on the tuple (enforced by the new index).
--
-- SQLite does not support ALTER TABLE ADD CONSTRAINT on existing tables,
-- so we use the table-rebuild pattern: create a new table with the UNIQUE
-- constraint, copy the deduplicated rows across, drop the old table, and
-- rename the new one.

CREATE TABLE slurm_dep_matrix_new (
    id                 TEXT PRIMARY KEY,
    slurm_version_min  TEXT NOT NULL,
    slurm_version_max  TEXT NOT NULL,
    dep_name           TEXT NOT NULL,
    dep_version_min    TEXT NOT NULL,
    dep_version_max    TEXT NOT NULL,
    source             TEXT NOT NULL DEFAULT 'bundled',
    created_at         INTEGER NOT NULL,
    UNIQUE(slurm_version_min, slurm_version_max, dep_name, dep_version_min, dep_version_max, source)
);

-- Copy only the oldest row per unique content tuple (MIN(id) is arbitrary but
-- deterministic for any given snapshot; MIN(rowid) picks the first-inserted row).
INSERT INTO slurm_dep_matrix_new
    SELECT id, slurm_version_min, slurm_version_max, dep_name, dep_version_min, dep_version_max, source, created_at
    FROM slurm_dep_matrix
    WHERE rowid IN (
        SELECT MIN(rowid)
        FROM slurm_dep_matrix
        GROUP BY slurm_version_min, slurm_version_max, dep_name, dep_version_min, dep_version_max, source
    );

DROP TABLE slurm_dep_matrix;

ALTER TABLE slurm_dep_matrix_new RENAME TO slurm_dep_matrix;
