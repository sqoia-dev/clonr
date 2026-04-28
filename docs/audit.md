# Audit Log

clustr maintains a comprehensive audit log of all administrative and user actions.
The log is stored in the `audit_log` SQLite table and exposed via the REST API.

## Table Schema

```sql
CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    actor_id      TEXT NOT NULL,
    actor_label   TEXT NOT NULL,
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    old_value     TEXT,          -- JSON blob (nullable)
    new_value     TEXT,          -- JSON blob (nullable)
    ip_addr       TEXT,
    created_at    INTEGER NOT NULL  -- Unix timestamp
);
```

## Querying the Audit Log

**Endpoint:** `GET /api/v1/audit`  
**Auth:** Admin-only.

### Query Parameters

| Parameter | Type | Description |
|---|---|---|
| `since` | RFC3339 | Return events at or after this time |
| `until` | RFC3339 | Return events at or before this time |
| `actor` | string | Filter by `actor_id` |
| `action` | string | Filter by action string (exact match) |
| `resource_type` | string | Filter by resource type |
| `limit` | integer | Max records per page (1–500, default 100) |
| `offset` | integer | Pagination offset |

### Response

```json
{
  "records": [...],
  "total": 1234,
  "limit": 100,
  "offset": 0
}
```

Each record has:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique audit record ID (e.g. `aud-1234567890`) |
| `actor_id` | string | Internal ID of the actor |
| `actor_label` | string | Human-readable actor: `user:<id>` or `key:<label>` |
| `action` | string | Event type (see Action Reference below) |
| `resource_type` | string | Category of affected resource |
| `resource_id` | string | ID of the affected resource |
| `old_value` | object | JSON of resource state before the action (nullable) |
| `new_value` | object | JSON of resource state after the action (nullable) |
| `ip_addr` | string | Remote IP of the actor request |
| `created_at` | string | RFC3339 UTC timestamp |

## SIEM Export (v1.5.0)

**Endpoint:** `GET /api/v1/audit/export`  
**Auth:** Admin-only.  
**Rate limit:** 1 request per minute per actor.

Returns the audit log as JSONL (newline-delimited JSON / NDJSON). Each line is one
JSON object using the same schema as the query API. Records are returned in
ascending `created_at` order (oldest first) for SIEM consumers.

**Content-Type:** `application/x-ndjson`

### Query Parameters

Same `since`, `until`, `actor`, `action`, `resource_type` parameters as the query
endpoint. No `limit` / `offset` — the export is unbounded (use `since`/`until` to
bound the window).

### Example

```bash
# Export all events in the last 30 days to a file:
curl -H "Authorization: Bearer $API_KEY" \
  "https://clustr.example.com/api/v1/audit/export?since=$(date -d '30 days ago' -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o audit-export.jsonl
```

### JSONL Line Schema

Each line is a JSON object with these stable fields (v1 — no breaking changes
without a version bump per ADR-0028):

```jsonc
{
  "id":            "aud-1716000000000000000",
  "created_at":    "2026-05-18T09:00:00Z",
  "actor_id":      "usr-abc123",
  "actor_label":   "user:jdoe",
  "action":        "node.update",
  "resource_type": "node",
  "resource_id":   "node-xyz",
  "ip_addr":       "192.168.1.10",
  "old_value":     { ... },   // omitted if not applicable
  "new_value":     { ... }    // omitted if not applicable
}
```

### SIEM Integration Example (Splunk)

Configure a Splunk HEC input to receive JSONL. Then schedule a cron job:

```bash
#!/bin/bash
# Run daily; send yesterday's events to Splunk.
SINCE=$(date -d 'yesterday' -u +%Y-%m-%dT00:00:00Z)
UNTIL=$(date -d 'yesterday 23:59:59' -u +%Y-%m-%dT23:59:59Z)
curl -s "https://clustr.internal/api/v1/audit/export?since=${SINCE}&until=${UNTIL}" \
  | while IFS= read -r line; do
      curl -s -H "Authorization: Splunk $SPLUNK_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"event\":${line}}" \
        "https://splunk.internal:8088/services/collector/event"
    done
```

## Log Retention

Default retention: 90 days. Configurable via the `CLUSTR_AUDIT_RETENTION` environment
variable (duration string, e.g. `180d`). The audit purger runs hourly.

## Action Reference

| Action | Description |
|---|---|
| `node.create` | Node registered |
| `node.update` | Node config updated |
| `node.delete` | Node deleted |
| `node.reimage` | Node reimage triggered |
| `image.create` | Base image created |
| `image.delete` | Base image deleted |
| `image.archive` | Base image archived |
| `image.status_change` | Image status changed |
| `node_group.create` | Node group created |
| `node_group.update` | Node group updated |
| `node_group.delete` | Node group deleted |
| `node_group.reimage` | Group reimage triggered |
| `node_group.member_add` | Node added to group |
| `node_group.member_remove` | Node removed from group |
| `node_group.expiration_set` | Allocation expiration date set (v1.5.0) |
| `node_group.expiration_cleared` | Allocation expiration date cleared (v1.5.0) |
| `node_group.expiration_warning` | Expiration warning email sent (v1.5.0) |
| `user.create` | User created |
| `user.update` | User updated |
| `user.delete` | User deleted |
| `user.reset_password` | Password reset |
| `user.group_memberships_update` | User group memberships changed |
| `api_key.create` | API key created |
| `api_key.revoke` | API key revoked |
| `api_key.rotate` | API key rotated |
| `ldap_config.update` | LDAP config changed |
| `slurm_config.update` | Slurm config changed |
| `slurm.install.failed` | Slurm install failed during deploy |
| `notification.sent` | Email notification sent |
| `notification.failed` | Email notification failed |
| `notification.skipped` | Email notification skipped (SMTP not configured) |
| `broadcast.sent` | Broadcast email sent |
| `broadcast.skipped` | Broadcast skipped |
| `smtp_config.update` | SMTP config updated |
| `smtp_config.test_send` | SMTP test send triggered |
| `grant.create` | Research grant created |
| `grant.update` | Research grant updated |
| `grant.delete` | Research grant deleted |
| `publication.create` | Publication created |
| `publication.update` | Publication updated |
| `publication.delete` | Publication deleted |
| `review_cycle.create` | Annual review cycle created |
| `review_response.submit` | Annual review response submitted |
