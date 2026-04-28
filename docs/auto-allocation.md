# Auto-Allocation Policy Engine

> Introduced in v1.7.0 (Sprint H). Requires the `auto_policy_config` singleton
> to have `enabled = true` before any automation runs.

## Overview

The auto-allocation engine removes manual sysadmin steps from PI onboarding.
When a PI creates a project with `auto_compute: true`, clustr automatically:

1. Creates a NodeGroup named after the project slug
2. Assigns the PI as owner
3. Syncs an LDAP project group (if LDAP is enabled)
4. Applies a per-group LDAP access restriction (if configured)
5. Adds a Slurm partition entry to `slurm.conf`
6. Records a JSON policy snapshot for undo
7. Audits the full action chain

All steps run in a single request. Fatal failures (Slurm partition add) trigger
automatic NodeGroup rollback. Non-fatal steps (LDAP) log and continue.

## Admin Configuration

### Enable / Disable

```
GET  /api/v1/admin/auto-policy-config
PUT  /api/v1/admin/auto-policy-config
```

Config fields:

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | bool | `false` | Master switch — no automation runs if false |
| `default_node_count` | int | `0` | Node count hint stored in state (informational) |
| `default_hardware_profile` | string | `""` | Hardware profile to apply to auto-created groups |
| `default_partition_template` | string | `""` | Go template for partition name. Available vars: `{{.ProjectSlug}}` |
| `default_role` | string | `"compute"` | NodeGroup role for auto-created groups |
| `notify_admins_on_create` | bool | `false` | Send admin email when auto-allocation fires |

Example — enable with GPU-by-default:

```json
{
  "enabled": true,
  "default_role": "gpu",
  "default_partition_template": "{{.ProjectSlug}}-gpu",
  "notify_admins_on_create": true
}
```

### Partition Name Templates

The `default_partition_template` field is a Go `text/template` string. The only
available variable is `{{.ProjectSlug}}` — the project name run through
`slugify()` (lowercase, non-alphanumeric → `-`, clamped to 48 chars).

Examples:

| Template | Slug input | Result |
|---|---|---|
| `{{.ProjectSlug}}-compute` | `HPC Research Group` | `hpc-research-group-compute` |
| `shared-gpu` | (any) | `shared-gpu` |
| `{{.ProjectSlug}}` | `My Lab 2026` | `my-lab-2026` |

A PI can also supply `partition_template` directly in their project creation
request, which overrides the admin default for that project.

## PI Onboarding Wizard

On first login, PIs with no existing projects see a one-screen wizard overlay
in the PI portal. The wizard collects:

- **Project name** — used to derive the NodeGroup name and partition slug
- **Partition name template** — overrides admin default for this project
- **Initial members** — comma-separated usernames added to the group at creation
- **LDAP sync toggle** — enable/disable LDAP group sync for this project
- **Auto-compute toggle** — submit to the engine vs. create a plain project

Submitting the wizard POSTs to `POST /api/v1/projects`. If `auto_compute=true`
the engine runs immediately. The wizard dismisses permanently on success.

### Wizard State Endpoints

```
GET  /api/v1/portal/pi/onboarding-status
     → { "show_wizard": true/false, "completed": true/false }

POST /api/v1/portal/pi/onboarding-complete
     → 200 OK (marks wizard dismissed without creating a project)
```

The wizard is shown only when both conditions are true:
- `onboarding_completed = 0` for the PI user
- PI has no existing NodeGroups

## 24-Hour Undo Window

After auto-allocation, PIs have 24 hours to reverse the entire operation via
the PI portal or the API.

### Undo Endpoint

```
POST /api/v1/node-groups/{id}/undo-auto-policy
```

- Returns `200` with a summary on success
- Returns `409 Conflict` when the window is closed (finalized or >24h elapsed)
- Sends the PI a notification email on success

The undo operation:
1. Checks the window is still open
2. Parses the stored `auto_policy_state` JSON
3. Deletes the NodeGroup (cascades members, audit refs)
4. Audits the undo event
5. Notifies the PI by email (non-blocking goroutine)

Note: the Slurm partition stanza added by auto-allocation is flagged in the
notification for manual operator removal. Automated Slurm config rollback is
not performed (Slurm config changes require operator validation).

### Undo State Endpoint

```
GET /api/v1/node-groups/{id}/auto-policy-state
```

Response when window is open:

```json
{
  "undo_available": true,
  "hours_remaining": 21.5,
  "node_group_id": "...",
  "node_group_name": "mylab-compute",
  "slurm_partition_name": "mylab-compute",
  "pi_user_id": "...",
  "created_at": "2026-04-27T10:00:00Z"
}
```

Response when window is closed:

```json
{
  "undo_available": false,
  "hours_remaining": 0,
  ...
}
```

### PI Portal Undo Banner

The PI portal renders a banner on each auto-compute group card while the undo
window is open. The banner shows hours remaining and an Undo button. Clicking
Undo confirms with the user, then calls the undo endpoint and reloads the
group list.

### Background Finalizer

A background worker (`runAutoPolicyFinalizer`) ticks every hour. It:
1. Queries `ListPendingAutoComputeGroups` — groups where `auto_compute=1 AND
   auto_policy_finalized_at IS NULL`
2. For each group where `created_at` is >24h ago, calls `FinalizeAutoComputeState`
   (sets `auto_policy_finalized_at = NOW()`)
3. Audits each finalization as `auto_policy.finalized`

Once finalized, the undo window is permanently closed even if `auto_policy_state`
JSON is still present.

## Engine Package

The engine lives in `internal/allocation/auto_policy.go`. It is decoupled from
the server via function fields on `Engine`:

```go
type Engine struct {
    DB    *db.DB
    Audit *db.AuditService

    // Optional hooks — nil = skip step (non-fatal).
    SyncLDAPGroup       func(ctx context.Context, groupID string) error
    SetGroupRestriction func(ctx context.Context, groupID string) error
    AddSlurmPartition   func(ctx context.Context, groupID, partitionName string) error
}
```

This makes the engine fully testable without a live LDAP/Slurm environment —
pass nil hooks or stub functions.

### Policy State JSON

Stored in `node_groups.auto_policy_state`. Schema:

```json
{
  "v": "1",
  "node_group_id": "uuid",
  "node_group_name": "mylab-compute",
  "ldap_group_dn": "cn=clustr-project-mylab,ou=clustr-projects,dc=example,dc=com",
  "slurm_partition_name": "mylab-compute",
  "pi_user_id": "uuid",
  "initial_members": ["alice", "bob"],
  "created_at": "2026-04-27T10:00:00Z",
  "policy_snapshot": {
    "hardware_profile": "",
    "partition_template": "{{.ProjectSlug}}-compute",
    "role": "compute",
    "node_count": 0
  }
}
```

## DB Schema

Migration 072 adds to `node_groups`:

| Column | Type | Description |
|---|---|---|
| `auto_compute` | `INTEGER NOT NULL DEFAULT 0` | Set to 1 for engine-managed groups |
| `auto_policy_state` | `TEXT` | JSON snapshot (nullable) |
| `auto_policy_finalized_at` | `INTEGER` | Unix timestamp; NULL = window open |

Migration 072 adds to `users`:

| Column | Type | Description |
|---|---|---|
| `onboarding_completed` | `INTEGER NOT NULL DEFAULT 0` | Set to 1 after wizard dismiss |

Migration 073 creates `auto_policy_config` (singleton, `id='default'`).

## Notifications

Two email templates are used:

- `internal/notifications/templates/auto_allocation_created.txt` — sent to
  admin list when `notify_admins_on_create=true`
- `internal/notifications/templates/auto_allocation_undone.txt` — sent to PI
  after successful undo
