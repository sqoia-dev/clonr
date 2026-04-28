# PI Portal — How-To Guide

The PI (Principal Investigator) portal is the self-service surface for
research group leads. It lets PIs manage the membership of their compute
group and view utilization stats — without needing admin involvement for
day-to-day tasks.

**URL:** `/portal/pi/`
**Role required:** `pi` (or `admin` for testing)

---

## What PIs can do

- View all NodeGroups they own.
- Expand the member list for any owned group.
- Add a member (by LDAP username) — immediately or via admin approval, depending on mode.
- Remove a member from a group.
- View read-only utilization stats: total nodes, deployed nodes, last deploy timestamp, failed deploys in the last 30 days, and active member count.
- Submit a node expansion request (asking for more compute capacity) with a justification.

## What PIs cannot do

- Create or delete nodes.
- Modify node configuration or images.
- See groups they do not own.
- Manage users or API keys.
- Access any admin surface (`/`, `/admin/`).
- Transfer group ownership to another PI — only admins can change `pi_user_id`.

---

## Setting up a PI user (admin steps)

### 1. Create the PI user account

In the web UI: **Settings → Users → Create User**. Set role to `pi`.

Or via API:

```http
POST /api/v1/admin/users
X-API-Key: <admin-key>
Content-Type: application/json

{
  "username": "jdoe",
  "password": "TemporaryPassword1!",
  "role": "pi"
}
```

The user will be prompted to change their password on first login.

### 2. Assign the PI to a NodeGroup

```http
PUT /api/v1/node-groups/{group-id}/pi
X-API-Key: <admin-key>
Content-Type: application/json

{
  "pi_user_id": "<user-id-from-step-1>"
}
```

To clear a PI assignment, send `{"pi_user_id": ""}`.

The PI can now log in. After authentication, they are redirected to `/portal/pi/`.

---

## Member management modes

There are two modes for the "add member" operation. Which mode is active
determines whether PI-submitted member requests are auto-applied or held for
admin review.

### Auto-approve mode

When auto-approve is on, a PI-submitted add-member request immediately calls
`ldap.AddUserToGroup` and the member appears in the group without any admin
action.

**Enable:**
```bash
# Set env var (requires restart)
CLUSTR_PI_AUTO_APPROVE=true clustr-serverd

# Or update the DB flag at runtime (no restart needed)
curl -X POST http://localhost:7001/api/v1/admin/pi/auto-approve \
  -H "X-API-Key: <admin-key>" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}'
```

**Use when:** Your institution trusts PIs to manage their own group membership
without review. Faster onboarding for large groups.

### Manual-approve mode (default)

When auto-approve is off, a PI-submitted add-member request creates a
`pending` row in `pi_member_requests`. An admin must review and approve or deny
the request before the LDAP change is made.

**Admin review endpoints:**

```http
# List pending requests
GET /api/v1/admin/pi/member-requests?status=pending

# Approve a request
POST /api/v1/admin/pi/member-requests/{id}/resolve
{"action": "approved"}

# Deny a request
POST /api/v1/admin/pi/member-requests/{id}/resolve
{"action": "denied"}
```

**Use when:** Your institution requires oversight of who gets LDAP group
membership (e.g., security policy, license compliance, sponsored access).

---

## Expansion requests

When a PI needs more compute nodes than their current NodeGroup contains, they
submit an expansion request from the PI portal ("Request more nodes" button
on any group card). The request includes a free-text justification.

**Admin review:**

```http
# List pending expansion requests
GET /api/v1/admin/pi/expansion-requests?status=pending

# Acknowledge (admin will handle it manually)
POST /api/v1/admin/pi/expansion-requests/{id}/resolve
{"action": "acknowledged"}

# Dismiss (with optional note surfaced back to PI)
POST /api/v1/admin/pi/expansion-requests/{id}/resolve
{"action": "dismissed"}
```

Expansion requests do **not** automatically add nodes. The admin reviews
them and takes manual action (reassigning nodes, provisioning hardware, etc.).
This is the lightweight precursor to a full allocation-change-request workflow
(planned for v1.4).

---

## Utilization view

The utilization tab on the PI portal shows aggregated stats for each owned
group. Stats are computed by pure SQL aggregation over existing tables — no
separate rollup schema.

| Field | Source | Notes |
|-------|--------|-------|
| `node_count` | `node_group_memberships` | Total nodes in group |
| `deployed_count` | `node_configs.deploy_completed_preboot_at IS NOT NULL` | Nodes that have successfully completed a deploy |
| `undeployed_count` | Derived | `node_count - deployed_count` |
| `last_deploy_at` | `MAX(node_configs.deploy_completed_preboot_at)` | Timestamp of most recent successful deploy |
| `failed_deploys_30d` | `audit_log` where `action = 'node.reimage'` and status `failed`/`verify_timeout` | Requires audit log entries; 0 if no failures or no audit data |
| `member_count` | `pi_member_requests` with `status = 'approved'` | Approximation — tracks approved requests, not live LDAP group size |
| `partition_state` | Not yet available | LDAP/Slurm partition state is live-fetched by the server process, not persisted in DB; surfaced as "unavailable" |

The utilization card auto-refreshes every 60 seconds via HTMX
(`hx-trigger="load, every 60s"`).

---

## Data gaps and labeling

The utilization view deliberately labels missing data rather than hiding it:

- **`partition_state = ""`** → displayed as "Unavailable" (requires live Slurm
  connection; PI portal makes no assumption about Slurm connectivity).
- **`member_count`** → labeled as "Approximate" in the UI tooltip, because it
  reflects approved clustr requests, not the live LDAP directory state. A PI
  who added members outside clustr will see a lower count.
- **`failed_deploys_30d = 0`** with no deploy history → displayed as "0" with
  a tooltip noting the audit window is 30 days.

This is consistent with Decision D25 (no custom metrics schema until a
customer specifies them; surface gaps explicitly rather than hiding them).

---

## LDAP integration notes

The PI portal's add/remove member operations call:

- `ldap.Manager.AddUserToGroup(ctx, uid, groupCN)` — delegates to `ditClient.AddGroupMember`
- `ldap.Manager.RemoveUserFromGroup(ctx, uid, groupCN)` — delegates to `ditClient.RemoveGroupMember`

The `groupCN` is the NodeGroup's `name` field. Ensure your LDAP DIT structure
uses NodeGroup names as group CNs, or adjust the LDAP configuration to match.

If LDAP is not configured, the add/remove calls return an error and the request
remains `pending` (in manual mode) or the auto-approve fails with a 500 (which
is surfaced to the PI as "LDAP is not configured — contact admin").

---

## API reference

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/v1/portal/pi/groups` | pi / admin | List owned NodeGroups with summary |
| GET | `/api/v1/portal/pi/groups/{id}/utilization` | pi / admin | Utilization stats for one group |
| GET | `/api/v1/portal/pi/groups/{id}/members` | pi / admin | List members of one group |
| POST | `/api/v1/portal/pi/groups/{id}/members` | pi / admin | Add member (auto-approve or pending) |
| DELETE | `/api/v1/portal/pi/groups/{id}/members/{username}` | pi / admin | Remove member |
| POST | `/api/v1/portal/pi/groups/{id}/expansion-requests` | pi / admin | Submit expansion request |
| GET | `/api/v1/admin/pi/member-requests` | admin | List all PI member requests |
| POST | `/api/v1/admin/pi/member-requests/{id}/resolve` | admin | Approve or deny a member request |
| GET | `/api/v1/admin/pi/expansion-requests` | admin | List all expansion requests |
| POST | `/api/v1/admin/pi/expansion-requests/{id}/resolve` | admin | Acknowledge or dismiss expansion request |
| PUT | `/api/v1/node-groups/{id}/pi` | admin | Assign or clear PI for a group |
