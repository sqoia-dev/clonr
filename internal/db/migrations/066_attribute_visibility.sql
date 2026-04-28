-- Migration 066: Per-attribute visibility policy (Sprint E, E3, CF-39).
--
-- Defines which roles can see which named attributes on projects (NodeGroups).
-- Default visibility per attribute is set here; admin can override via API.
--
-- Visibility levels:
--   admin_only   — only admin role sees this attribute
--   pi           — pi role and above (pi, admin)
--   member       — member of the group (viewer+) and above
--   public       — all authenticated users (director, etc.)
--
-- The initial set of defaults corresponds to D26 in docs/decisions.md.

CREATE TABLE IF NOT EXISTS project_attribute_visibility (
    project_id      TEXT NOT NULL REFERENCES node_groups(id) ON DELETE CASCADE,
    attribute_name  TEXT NOT NULL,
    visibility      TEXT NOT NULL DEFAULT 'pi'
                    CHECK(visibility IN ('admin_only','pi','member','public')),
    updated_at      INTEGER NOT NULL,
    updated_by      TEXT REFERENCES users(id),
    PRIMARY KEY (project_id, attribute_name)
);

CREATE INDEX IF NOT EXISTS idx_pav_project ON project_attribute_visibility(project_id);

-- Global default visibility table (no project_id — applies when no project-specific
-- override exists). Used by the API layer to determine default visibility.
CREATE TABLE IF NOT EXISTS attribute_visibility_defaults (
    attribute_name  TEXT PRIMARY KEY,
    visibility      TEXT NOT NULL DEFAULT 'pi'
                    CHECK(visibility IN ('admin_only','pi','member','public')),
    description     TEXT NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL DEFAULT 0
);

-- D26 defaults: see docs/decisions.md D26 for rationale.
-- grant_amount is pi-only (financial sensitivity).
-- field_of_science is public (reporting value, no sensitivity).
-- node_count is member-visible (useful context for researchers).
-- bmc_credentials is admin_only (security-critical).
-- pi_name is public (accountability + transparency).
-- description is public (explains the group's purpose).
-- grant_number is pi-only (funding sensitivity).
-- funding_agency is public (useful for reporting, no sensitivity).
INSERT OR IGNORE INTO attribute_visibility_defaults (attribute_name, visibility, description) VALUES
('grant_amount',        'pi',          'Grant dollar amount — financial detail visible to PI and admin only'),
('grant_number',        'pi',          'Grant award number — funding detail visible to PI and admin only'),
('funding_agency',      'public',      'Grant funding agency — useful for reporting, no sensitivity'),
('field_of_science',    'public',      'NSF Field of Science classification — used for utilization breakdown by director'),
('node_count',          'member',      'Number of nodes in the group — visible to group members'),
('pi_name',             'public',      'PI name — public accountability'),
('description',         'public',      'NodeGroup description — explains research purpose'),
('bmc_credentials',     'admin_only',  'BMC/IPMI credentials — security-sensitive, admin only'),
('publication_doi',     'public',      'Publication DOI — academic record, fully public'),
('publication_title',   'public',      'Publication title — academic record, fully public'),
('publication_authors', 'public',      'Publication authors — academic record, fully public'),
('utilization_stats',   'member',      'Utilization statistics — visible to group members and above'),
('slurm_partition',     'member',      'Slurm partition name — operational info for group members'),
('node_hardware',       'pi',          'Node hardware profile details — visible to PI and admin');
