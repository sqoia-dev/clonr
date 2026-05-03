-- Migration 072: Auto-compute allocation policy engine (Sprint H, v1.7.0 / CF-29).
--
-- Adds two columns to node_groups:
--
--   auto_compute (INTEGER, default 0):
--     When set to 1, this NodeGroup was created by the auto-policy engine
--     (H1). Setting it at creation time records provenance so the undo
--     handler knows which groups are candidates for reversal.
--
--   auto_policy_state (TEXT, nullable JSON):
--     JSON blob written by AutoPolicyEngine.Run() recording exactly what
--     was created: NodeGroup IDs, LDAP group DNs, Slurm partition name,
--     PI user ID, and a pre-action snapshot of the caller's configuration.
--     The undo handler reads this blob to reverse the engine's actions.
--
--   auto_policy_finalized_at (INTEGER, nullable unix timestamp):
--     Set by the background finalizer 24 hours after auto_policy_state is
--     written. Once non-null, the undo endpoint returns 409 (window closed).
--
--   onboarding_completed (INTEGER, default 0):
--     Tracks whether the PI has completed the first-project wizard (H2).
--     Stored on the user record via a separate column added below.

-- NodeGroup policy columns.
ALTER TABLE node_groups ADD COLUMN auto_compute            INTEGER  NOT NULL DEFAULT 0;
ALTER TABLE node_groups ADD COLUMN auto_policy_state       TEXT;
ALTER TABLE node_groups ADD COLUMN auto_policy_finalized_at INTEGER;

-- Per-user wizard completion flag.
-- Stored on users so we can gate "show wizard on first login" without a
-- separate table query.
ALTER TABLE users ADD COLUMN onboarding_completed INTEGER NOT NULL DEFAULT 0;

-- Index for the finalizer background worker: only scan unfinalised auto-compute groups.
CREATE INDEX IF NOT EXISTS idx_node_groups_auto_policy_pending
    ON node_groups(auto_policy_finalized_at)
    WHERE auto_compute = 1 AND auto_policy_finalized_at IS NULL;
