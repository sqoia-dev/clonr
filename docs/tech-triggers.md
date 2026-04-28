# Tech Triggers — D27 Bucket 2 Observability

**Status:** Live as of v1.11.0 (Sprint M, 2026-04-27).

Tech Triggers are the four TECH-TRIG signals defined in `decisions.md` D27 Bucket 2. Each signal gates a major architectural sprint. This document describes what each trigger measures, what the threshold is and why, and what to do when it fires.

The admin UI surface is at **Settings > Tech Triggers** (admin role required).
The API surface is at `GET /api/v1/admin/tech-triggers`.

---

## How It Works

A background worker evaluates all four triggers every 10 minutes. On the first firing transition (not-fired → fired), the system:

1. Records a `tech_trig.fired` entry in the audit log.
2. Sends a notification email to all admin users (if SMTP is configured).
3. Sets `fired_at` in `tech_trig_state` — this timestamp is preserved until an admin resets it.

The "fired" state is sticky: it does not clear automatically if the metric drops back below the threshold. The operator must explicitly reset it (via the UI "Reset" button or `POST /api/v1/admin/tech-triggers/{name}/reset`) after the corresponding sprint is dispatched and completed.

---

## T1 — PostgreSQL Migration Trigger (`t1_postgresql`)

### What it measures

- **Metric A:** Registered node count (`SELECT COUNT(*) FROM node_configs`).
- **Metric B:** SQLite write contention rate — the rolling count of `SQLITE_BUSY` and `SQLITE_LOCKED` errors per second, computed as a 10-minute average between evaluator ticks.

### Thresholds

| Metric | Threshold | Rationale |
|---|---|---|
| Node count | >= 500 | 10x the expected single-cluster size for Persona A/B targets. At this scale, WAL-mode SQLite remains functional but single-writer serialization starts to impact concurrent deploys. |
| Contention rate | >= 5 events/sec sustained | WAL mode makes contention rare at normal scale. 5 events/sec for a full 10-minute tick (= 3,000 events) indicates genuine write saturation, not a transient admin burst. |

**Fires when:** node count >= 500 **OR** contention rate >= 5 events/sec.

### What to do when it fires

Dispatch the PostgreSQL migration sprint (cf. D28 — this is a MAJOR version bump to v2.0.0, BREAKING). The sprint includes:

1. Migrate the SQLite schema to PostgreSQL (schema export, data migration script, dual-write period).
2. Replace `modernc.org/sqlite` with `lib/pq` or `jackc/pgx`.
3. Update Docker Compose to require a PostgreSQL service or Managed Database.
4. Update all `PRAGMA`-based introspection (e.g., `MeasureLogBytes`) to PostgreSQL equivalents.
5. Update `docs/install.md` with PostgreSQL setup path.

**Decision authority:** Richard (Technical Co-founder). Node-count threshold fires automatically; contention-rate threshold fires automatically. Multi-tenant requirement (alternative trigger path) is Founder's call.

### Reset policy

Reset after the PostgreSQL migration sprint ships and the new binary is deployed. A reset while the SQLite ceiling is still in effect (metric still above threshold) is valid only if the sprint has been dispatched and is in active development.

---

## T2 — Framework Ceiling Trigger (`t2_framework`)

### What it measures

- **Metric A:** Total non-blank lines across all `*.js` files in `internal/server/ui/static/` (vendor paths excluded). Counted from the embedded FS at startup; updated every 10-minute tick.
- **Metric B (manual):** Operator-set `manual_signal` flag. Set this when a specific Alpine.js or vanilla-JS pattern requires an architectural workaround to implement correctly — e.g., deeply nested Alpine `x-data` components communicating via custom events, or a feature that requires a build step because vanilla JS cannot express it without duplication.

### Thresholds

| Metric | Threshold | Rationale |
|---|---|---|
| Frontend JS LOC | >= 5,000 | Matches D21's explicit LOC ceiling for the "vanilla JS + Alpine.js, no build step" constraint. At 5,000 LOC the maintenance surface becomes non-trivial; the build-step cost of a framework migration is increasingly justified. |
| Manual signal | = true | Qualitative: the operator hit a concrete Alpine pattern that the framework cannot express cleanly. The LOC counter alone does not capture framework-specific friction. |

**Fires when:** JS LOC >= 5,000 **OR** manual signal is set.

### What to do when it fires

Dispatch the framework migration sprint (cf. D28 — this is a MAJOR version bump to v2.0.0, BREAKING, per D10). The sprint should:

1. Select a target framework (React, Vue, SvelteKit — evaluate at dispatch time; D23 explicitly deferred this choice).
2. Introduce a build step (`vite` or `esbuild`). Update `Makefile`, Dockerfile, and CI.
3. Migrate existing Alpine.js components to the target framework incrementally.
4. Remove the embedded Alpine.js and HTMX vendor files once migrated.

**Decision authority:** Richard (Technical Co-founder). If LOC threshold fires automatically, confirm with Richard before dispatching (LOC alone may not justify the migration cost without qualitative friction). Manual signal is operator-initiated; confirm the specific pattern that triggered it.

### Setting the manual signal

From the admin UI (Settings > Tech Triggers > T2 — Framework Ceiling), click "Set Signal".
From the API: `POST /api/v1/admin/tech-triggers/t2_framework/signal` with `{"signal": true}`.
Both are audit logged.

### Reset policy

Reset after the framework migration sprint ships. Manual signal is cleared automatically on reset.

---

## T3 — Multi-Tenant Isolation Trigger (`t3_multitenant`)

### What it measures

This trigger is **purely manual**. Clustr is single-tenant today — one operator, one cluster fleet, one set of users. There is no `tenants` table and no tenant-scoping in any query.

The signal fires when an operator explicitly marks it, indicating they are planning one of:

- A hosted-clustr-as-service deployment (multiple independent tenants sharing one server).
- An internal deployment where three or more logically separate fleets need isolation (separate audit trails, separate user namespaces, separate node groups with no cross-visibility).

### Threshold

No numeric threshold. Purely a manual operator signal.

### What to do when it fires

Dispatch the multi-tenant isolation sprint (cf. D28 — this is a MAJOR version bump to v2.0.0, BREAKING). The sprint should:

1. Add a `tenant_id` column to all resource tables (nodes, node groups, images, users, audit log, etc.) — schema-wide backfill.
2. Scope all queries to `WHERE tenant_id = ?` using middleware injection.
3. Add tenant provisioning API (create, update, delete tenant; assign admin).
4. Add tenant-scoped API key validation.
5. Update Docker Compose to support multi-tenant configuration.

**Decision authority:** Founder. This is a product positioning decision as much as a technical one. Do not dispatch without explicit Founder sign-off.

### Setting the manual signal

From the admin UI (Settings > Tech Triggers > T3 — Multi-Tenant Isolation), click "Set Signal".
From the API: `POST /api/v1/admin/tech-triggers/t3_multitenant/signal` with `{"signal": true}`.
Both are audit logged.

### Reset policy

Reset if the hosted deployment decision is reversed before the sprint ships. Post-sprint, reset to clear the fired state once multi-tenant support is live.

---

## T4 — Log Archive Trigger (`t4_log_archive`)

### What it measures

Estimated total bytes consumed by audit log and node log rows:

```
estimated_bytes = (audit_log row count × 800) + (node_logs row count × 300)
```

The per-row estimates (800 bytes for audit_log, 300 bytes for node_logs) are conservative upper bounds based on the JSON + metadata payload shape. This is not an exact measurement — it uses row counts rather than SQLite's internal page accounting (which cannot isolate per-table byte usage without reading every page). At the 50 GiB threshold, the approximation error is negligible.

### Threshold

**50 GiB** of estimated log storage.

**Rationale:** The single-binary self-hosted model targets operators with fewer than 500 nodes. At 50 GiB of log data, SQLite WAL overhead and vacuum cycles become measurably expensive. 50 GiB gives operators significant headroom before performance impact is observable — at 500 nodes with daily reimages and 100 audit events per reimage, reaching 50 GiB would take approximately 5–10 years. If an operator reaches this threshold earlier, it indicates unusually high event volume (large-scale automated operations) that justifies the archive infrastructure cost.

### What to do when it fires

Dispatch the hot/cold log archive sprint. The sprint should:

1. Implement a log retention policy (configurable `CLUSTR_LOG_RETENTION_DAYS`; default 90 days).
2. Add a background log purger that archives rows older than the retention window to a cold store (Object Storage / S3-compatible bucket, compressed JSONL format).
3. Add a `GET /api/v1/admin/logs/archive` endpoint for on-demand cold-log query.
4. Update the audit log UI to show a "load from archive" option for queries beyond the retention window.
5. Update `docs/audit.md` with the retention policy and archive format.

**Decision authority:** Richard (Technical Co-founder) for the technical design; operator confirms the retention window.

### Reset policy

Reset after the log archive sprint ships and old logs have been successfully pruned or archived.

---

## API Reference

All endpoints require the `admin` role.

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/admin/tech-triggers` | Current state for all four triggers |
| GET | `/api/v1/admin/tech-triggers/history` | Past firings, resets, and manual signals (newest-first, up to 200 records) |
| POST | `/api/v1/admin/tech-triggers/{name}/reset` | Clear fired state and manual signal |
| POST | `/api/v1/admin/tech-triggers/{name}/signal` | Set or clear manual signal (T2/T3 only) |

### Trigger names

| Name | Trigger |
|---|---|
| `t1_postgresql` | T1 — PostgreSQL Migration |
| `t2_framework` | T2 — Framework Ceiling |
| `t3_multitenant` | T3 — Multi-Tenant Isolation |
| `t4_log_archive` | T4 — Log Archive Pressure |

### Example response (`GET /api/v1/admin/tech-triggers`)

```json
[
  {
    "trigger_name": "t1_postgresql",
    "description": "T1: PostgreSQL migration signal — fires when node count >= 500 OR write contention >= 5 events/sec sustained for 5 minutes",
    "current_value": {"node_count": 12, "contention_rate": 0.0},
    "threshold": {"node_count": 500, "contention_rate": 5.0},
    "fired": false,
    "fired_at": null,
    "last_evaluated_at": "2026-04-27T14:30:00Z",
    "manual_signal": false
  }
]
```

---

## Database Schema

The `tech_trig_state` table (migration `074_tech_trig_state.sql`):

```sql
CREATE TABLE tech_trig_state (
    trigger_name       TEXT    NOT NULL PRIMARY KEY,
    current_value_json TEXT    NOT NULL DEFAULT '{}',
    threshold_json     TEXT    NOT NULL DEFAULT '{}',
    fired_at           INTEGER,          -- NULL = not fired; unix timestamp when first fired
    last_evaluated_at  INTEGER,          -- NULL = never evaluated
    manual_signal      INTEGER NOT NULL DEFAULT 0  -- 1 = operator marked
);
```

History is stored in the existing `audit_log` table with `action LIKE 'tech_trig.%'`. Query with `GET /api/v1/admin/tech-triggers/history`.

---

## Prometheus Metrics

If Prometheus scraping is configured, two gauges are exported:

| Metric | Labels | Value |
|---|---|---|
| `clustr_tech_trigger` | `name` | 1 = fired, 0 = not fired |
| `clustr_tech_trigger_value` | `name` | Current primary metric value (node count, JS LOC, 0 for T3, log bytes for T4) |

The Prometheus endpoint (`/metrics`) is the same one used by existing clustr instrumentation.

---

## Threshold Rationale Summary

| Trigger | Threshold | Chosen because |
|---|---|---|
| T1 node count | 500 nodes | 10x Persona A/B target; SQLite writer serialization measurable at this scale |
| T1 contention rate | 5 events/sec | WAL makes contention rare; 5/sec × 10-min tick = 3,000 events = genuine saturation |
| T2 JS LOC | 5,000 lines | D21's explicit ceiling for the no-build-step constraint |
| T3 | Manual | Purely a product positioning decision; no numeric proxy |
| T4 log bytes | 50 GiB | Conservative upper bound for <500-node deployment; gives years of headroom |

If any threshold feels wrong for a specific deployment's scale, open an issue. Threshold adjustments are a non-breaking config change and can be made before the trigger sprint is dispatched.
