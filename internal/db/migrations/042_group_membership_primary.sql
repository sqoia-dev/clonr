-- 042_group_membership_primary.sql (S2-5)
-- Adds is_primary flag to node_group_memberships.
-- A node's primary group controls disk layout inheritance, network profile
-- assignment, and group reimage targeting.
--
-- The partial unique index ensures each node has at most one primary group.
-- node_configs.group_id is retained for backward compatibility and is the
-- fast-path denormalized column; EffectiveLayout() will prefer the is_primary
-- row from node_group_memberships when available.

ALTER TABLE node_group_memberships ADD COLUMN is_primary INTEGER NOT NULL DEFAULT 0;

-- Partial unique index: at most one primary row per node.
CREATE UNIQUE INDEX IF NOT EXISTS idx_primary_group_membership
    ON node_group_memberships(node_id)
    WHERE is_primary = 1;

-- Back-fill: for every node that has exactly one membership, mark it as primary.
-- Nodes with multiple memberships keep is_primary=0 until an admin designates one.
UPDATE node_group_memberships
SET is_primary = 1
WHERE (node_id, group_id) IN (
    SELECT node_id, group_id
    FROM node_group_memberships
    GROUP BY node_id
    HAVING COUNT(*) = 1
);

-- For nodes with multiple memberships, promote the group that matches
-- the node_configs.group_id fast-path column (the historically primary group).
UPDATE node_group_memberships
SET is_primary = 1
WHERE is_primary = 0
  AND (node_id, group_id) IN (
      SELECT m.node_id, m.group_id
      FROM node_group_memberships m
      JOIN node_configs nc ON nc.id = m.node_id
      WHERE nc.group_id = m.group_id
        AND (SELECT COUNT(*) FROM node_group_memberships m2 WHERE m2.node_id = m.node_id AND m2.is_primary = 1) = 0
  );
