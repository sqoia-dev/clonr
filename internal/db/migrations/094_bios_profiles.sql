-- #159: BIOS profiles — named reusable BIOS setting sets and per-node bindings.
--
-- bios_profiles stores vendor-specific BIOS settings as an opaque JSON object
-- keyed on vendor-defined setting names.  The settings_json column contains a
-- flat JSON object: { "setting-name": "value", ... }.  clustr does not model
-- the settings schema internally; validation at create-time is delegated to the
-- active Provider.SupportedSettings() call.
--
-- node_bios_profile binds exactly one profile to a node.  A NULL profile_id
-- row does not exist — the absence of a row means "no profile assigned."  The
-- applied_settings_hash column records sha256(profile.settings_json) at the
-- time of the most recent successful apply so drift detection can compare
-- quickly without re-fetching the profile.

CREATE TABLE bios_profiles (
    id              TEXT    PRIMARY KEY,            -- UUIDv4
    name            TEXT    NOT NULL UNIQUE,
    vendor          TEXT    NOT NULL,               -- "intel" in v1; "dell", "supermicro" later
    settings_json   TEXT    NOT NULL,               -- JSON object {name: value, ...}
    description     TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,               -- unix seconds
    updated_at      INTEGER NOT NULL                -- unix seconds
);

CREATE INDEX idx_bios_profiles_vendor ON bios_profiles(vendor);

CREATE TABLE node_bios_profile (
    node_id                 TEXT    PRIMARY KEY,    -- one profile per node
    profile_id              TEXT    NOT NULL REFERENCES bios_profiles(id),
    last_applied_at         INTEGER,                -- unix seconds; NULL until first apply
    applied_settings_hash   TEXT,                   -- sha256(profile.settings_json) at last apply
    last_apply_error        TEXT                    -- non-empty string on most recent failure; NULL on success
);
