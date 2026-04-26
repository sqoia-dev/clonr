# RBAC — Role-Based Access Control

clustr ships with a 3-tier role model designed for small HPC teams where
most day-to-day work (deploying nodes, managing power) is delegated to
operators who are scoped to a subset of the cluster.

---

## The Three Roles

| Role | Capabilities |
|------|-------------|
| **admin** | Full access to everything: all nodes, all images, all groups, all users, API keys, audit log, LDAP config, Slurm config. Only admins can create/disable users or rotate API keys. |
| **operator** | Can perform state-changing operations (reimage, power on/off/cycle/reset, PXE boot) on nodes that belong to a NodeGroup they are a member of. Cannot create nodes, delete nodes, manage users, or see other groups' nodes beyond read access. |
| **readonly** | Can view all nodes, images, groups, and logs. Cannot perform any mutations. |

---

## Group-Scoped Operator

The key design: operators are not cluster-wide. They are scoped to one or more
**NodeGroups**. This is tracked in the `user_group_memberships` table:

```sql
CREATE TABLE user_group_memberships (
    user_id  TEXT NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES node_groups(id)  ON DELETE CASCADE,
    role     TEXT NOT NULL CHECK(role IN ('operator')),
    PRIMARY KEY (user_id, group_id)
);
```

An operator can only mutate (reimage, power actions) a node when that node
is assigned to a group the operator is a member of. A node with no group
cannot be mutated by any operator — only an admin can act on ungrouped nodes.

### Why this model

HPC clusters often have dedicated teams per sub-cluster: a GPU team, a storage
team, a compute team. Group-scoped operators let each team manage their own
nodes without needing cluster-wide admin access. Admins retain full control and
can reassign nodes between groups at any time.

---

## Permission Matrix

| Action | admin | operator (in-group) | operator (out-of-group) | readonly |
|--------|-------|---------------------|------------------------|---------|
| List nodes / images / groups | Yes | Yes | Yes | Yes |
| View node / image detail | Yes | Yes | Yes | Yes |
| Create node | Yes | No | No | No |
| Update node (config, hostname, tags) | Yes | No | No | No |
| Delete node | Yes | No | No | No |
| Power on/off/cycle/reset | Yes | Yes | No | No |
| PXE boot / disk boot | Yes | Yes | No | No |
| Trigger reimage | Yes | Yes | No | No |
| Group reimage | Yes | Yes (own group) | No | No |
| Create / delete image | Yes | No | No | No |
| Manage users / roles | Yes | No | No | No |
| Manage group memberships | Yes | No | No | No |
| View audit log | Yes | No | No | No |
| Manage LDAP config | Yes | No | No | No |
| Manage Slurm config | Yes | No | No | No |
| Create / revoke API keys | Yes | No | No | No |

---

## Session vs API Key Auth

Both session cookies and API keys are valid auth mechanisms. The role is
derived from the authenticated identity:

- **Session cookie** (browser): role is stored in the `users` table
  (`users.role`). Group memberships are looked up per request for operators.
- **API key**: the key carries a `scope` field. Scope `admin` maps to admin
  role for API purposes. Keys scoped to `node` or `readonly` are restricted
  accordingly. There is no operator-scoped API key today — operator access
  requires an interactive session.

The middleware resolves the effective role on every request:
1. Check `ctxKeyScope` (API key scope) first.
2. Fall back to `ctxKeyUserRole` (session role).
3. Missing both → 401 Unauthorized.

---

## Bootstrap Admin

When clustr-serverd starts for the first time with no users, the bootstrap
flow creates an admin user. The recommended approach uses the built-in
`apikey bootstrap` command:

```bash
clustr-serverd apikey bootstrap
```

This prints a one-time admin API key. Use it to create the first user account
via the API or web UI, then revoke the bootstrap key.

Alternatively, if `CLUSTR_AUTH_DEV_MODE=1` is set (loopback addresses only),
authentication is bypassed for local development. **Never use dev mode in
production** — the server will refuse to start if `CLUSTR_AUTH_DEV_MODE=1`
is set with a non-loopback listen address.

---

## Migration Story

**Single-tenant deployments (no RBAC needed):** Nothing to change. Admin API
keys issued before Sprint 3 still work. All existing deployments run as admin
scope and bypass group access checks entirely. No database migration is
required beyond running the server (migrations 043 and 044 apply automatically
on next startup).

**Adding your first operator:** 
1. Create a NodeGroup for the nodes you want the operator to manage.
2. Assign nodes to that group (set `group_id` on each `NodeConfig`).
3. Create a user with role `operator`.
4. Go to Settings → Users → click "Edit" in the Group Memberships column
   for that user. Check the appropriate groups. Save.
5. The operator can now log in and reimage/power nodes in their group.

**Expanding to multi-team:**
Repeat step 1–5 for each team. Each team gets its own NodeGroup and its own
operator accounts. Admins can reassign nodes between groups at any time by
changing the node's `group_id`.

---

## Audit Trail

All state-changing actions by admin and operator users are recorded to the
`audit_log` table (migration 044). The audit log captures:

- `actor_id` / `actor_label` — who performed the action (user ID or API key)
- `action` — what happened (e.g., `node.reimage`, `user.create`)
- `resource_type` / `resource_id` — what was affected
- `old_value` / `new_value` — JSON snapshots of before/after state (for updates)
- `ip_addr` — source IP of the request
- `created_at` — Unix timestamp

Query the audit log via `GET /api/v1/audit` (admin only). Retention is
controlled by `CLUSTR_AUDIT_RETENTION` (default: 90 days). A background purger
runs hourly to delete old records.

---

## Architecture Reference

See `architecture/` for the full system design. The RBAC implementation
follows decision D12 in `decisions.md`.
