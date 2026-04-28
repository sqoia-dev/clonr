# RBAC — Role-Based Access Control

clustr ships with a 5-role model designed for HPC teams. The role hierarchy
determines which UI surface a user lands on after login and what API operations
are available to them.

**Role hierarchy (most to least privileged):**

```
admin > operator > pi > readonly > viewer
```

---

## The Five Roles

| Role | Primary surface | Capabilities |
|------|-----------------|-------------|
| **admin** | `/` (full admin UI) | Full access to everything: all nodes, all images, all groups, all users, API keys, audit log, LDAP config, Slurm config. Only admins can create/disable users, rotate API keys, assign PI ownership, or approve PI member requests. |
| **operator** | `/` (admin UI, scoped) | Can perform state-changing operations (reimage, power on/off/cycle/reset, PXE boot) on nodes that belong to a NodeGroup they are a member of. Cannot create nodes, delete nodes, manage users, or see other groups' nodes beyond read access. |
| **pi** | `/portal/pi/` | Views all NodeGroups they own. Self-service member management (add/remove LDAP usernames subject to auto-approve or admin-approval). Read-only utilization stats (node count, deployed count, last deploy, failed deploys). Can submit node expansion requests. Cannot touch node config, images, or Slurm. |
| **readonly** | `/` (admin UI, read-only) | Can view all nodes, images, groups, and logs. Cannot perform any mutations. Typically used for on-call staff or observability integrations. |
| **viewer** | `/portal/` (researcher portal) | Read-only access to the researcher portal only. Sees their own group membership, partition status, and quota. Cannot reach any admin or PI surfaces. |

---

## Group-Scoped Operator

The key design for operators: they are not cluster-wide. They are scoped to one or more
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

## NodeGroup PI Ownership

A PI is assigned to a NodeGroup by an admin:

```http
PUT /api/v1/node-groups/{id}/pi
Content-Type: application/json

{"pi_user_id": "<user-id>"}
```

Rules:
- One NodeGroup has at most one PI (`pi_user_id` nullable FK on `node_groups`).
- One PI can own multiple NodeGroups.
- PI cannot transfer ownership to another user — admin-only.
- Setting `pi_user_id` to `""` clears the PI assignment.

---

## Permission Matrix

| Action | admin | operator (in-group) | operator (out-of-group) | pi (owned group) | pi (non-owned) | readonly | viewer |
|--------|-------|---------------------|------------------------|------------------|----------------|---------|--------|
| List nodes / images / groups | Yes | Yes | Yes | No | No | Yes | No |
| View node / image detail | Yes | Yes | Yes | No | No | Yes | No |
| Create node | Yes | No | No | No | No | No | No |
| Update node (config, hostname, tags) | Yes | No | No | No | No | No | No |
| Delete node | Yes | No | No | No | No | No | No |
| Power on/off/cycle/reset | Yes | Yes | No | No | No | No | No |
| PXE boot / disk boot | Yes | Yes | No | No | No | No | No |
| Trigger reimage | Yes | Yes | No | No | No | No | No |
| Group reimage | Yes | Yes (own group) | No | No | No | No | No |
| Create / delete image | Yes | No | No | No | No | No | No |
| Manage users / roles | Yes | No | No | No | No | No | No |
| Manage group memberships | Yes | No | No | No | No | No | No |
| Assign PI to group | Yes | No | No | No | No | No | No |
| View owned groups | Yes | No | No | Yes | No | No | No |
| View group utilization | Yes | No | No | Yes | No | No | No |
| Add/remove group members | Yes | No | No | Yes (auto or pending) | No | No | No |
| Submit expansion request | Yes | No | No | Yes | No | No | No |
| View audit log | Yes | No | No | No | No | No | No |
| Manage LDAP config | Yes | No | No | No | No | No | No |
| Manage Slurm config | Yes | No | No | No | No | No | No |
| Create / revoke API keys | Yes | No | No | No | No | No | No |
| View researcher portal | Yes | No | No | Yes | Yes | No | Yes |

---

## Session vs API Key Auth

Both session cookies and API keys are valid auth mechanisms. The role is
derived from the authenticated identity:

- **Session cookie** (browser): role is stored in the `users` table
  (`users.role`). Group memberships are looked up per request for operators.
  PI ownership is checked per request for PI portal routes.
- **API key**: the key carries a `scope` field. Scope `admin` maps to admin
  role for API purposes. Keys scoped to `node` or `readonly` are restricted
  accordingly. There is no operator-scoped or pi-scoped API key today —
  those roles require an interactive session.

The middleware resolves the effective role on every request:
1. Check `ctxKeyScope` (API key scope) first.
2. Fall back to `ctxKeyUserRole` (session role).
3. Missing both → 401 Unauthorized.

### Scope sentinel values

| Role | `api.KeyScope` value |
|------|---------------------|
| admin | `api.KeyScopeAdmin` |
| operator | `api.KeyScopeOperator` |
| pi | `api.KeyScope("pi")` |
| readonly | `api.KeyScope("readonly")` |
| viewer | `api.KeyScope("viewer")` |

---

## PI Member Request Workflow

When a PI adds a member via the PI portal, two paths exist:

**Auto-approve mode** (`pi_auto_approve = 1` in `portal_config`, or `CLUSTR_PI_AUTO_APPROVE=true` env):
1. PI submits username via `POST /api/v1/portal/pi/groups/{id}/members`.
2. Server immediately calls `ldap.AddUserToGroup`.
3. Request row is created with `status = 'approved'`.
4. No admin action required.

**Manual-approve mode** (default):
1. PI submits username.
2. Request row is created with `status = 'pending'`.
3. Admin sees pending request in `GET /api/v1/admin/pi/member-requests`.
4. Admin approves or denies via `POST /api/v1/admin/pi/member-requests/{id}/resolve`.
5. On approval, `ldap.AddUserToGroup` is called.

Switch modes without restart: `POST /api/v1/admin/pi/auto-approve` or directly update `portal_config.pi_auto_approve`.

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

## Adding Users

### Admin or operator:
1. Settings → Users → Create User.
2. Set role to `admin`, `operator`, or `readonly`.
3. For `operator`: after creating, click "Edit" in the Group Memberships column and assign groups.

### PI:
1. Settings → Users → Create User. Set role to `pi`.
2. Go to the group you want to assign: `PUT /api/v1/node-groups/{id}/pi` with `{"pi_user_id":"<uid>"}`.
3. The PI can now log in at `/portal/pi/`.
4. To enable auto-approve: set `pi_auto_approve = 1` in `portal_config` or set `CLUSTR_PI_AUTO_APPROVE=true`.

### Viewer (researcher):
1. Settings → Users → Create User. Set role to `viewer`.
2. The viewer can log in at `/portal/` immediately — no group assignment needed (LDAP UID is read from the session).

---

## Audit Trail

All state-changing actions by admin, operator, and pi users are recorded to the
`audit_log` table (migration 044). The audit log captures:

- `actor_id` / `actor_label` — who performed the action (user ID or API key)
- `action` — what happened (e.g., `node.reimage`, `user.create`, `pi.member.add`)
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
follows decision D12 in `decisions.md`. PI governance (role, portal, member
management) is documented in `docs/pi-portal.md`.
