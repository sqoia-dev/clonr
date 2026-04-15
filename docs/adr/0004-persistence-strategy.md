# ADR-0004: Persistence Strategy

**Date:** 2026-04-13
**Status:** Accepted
**Last Verified:** 2026-04-13 — applies to clonr main @ fbccab9

---

## Context

clonr uses SQLite via `modernc.org/sqlite` (pure Go, CGO_ENABLED=0). The write patterns are: node registration events (burst at deploy time), deploy event writes (~1 per node per deploy operation), image metadata updates (infrequent), sensor polling writes (regular, configurable interval). The read patterns are: MAC lookup on every PXE boot, blob path resolution on every download, group membership queries during deploy jobs.

Key constraints: the binary must remain statically compilable with CGO_ENABLED=0. The server runs on a single host. Most HPC provisioning servers are not HA — they are replaced if they fail. The database is not the source of truth for cluster state; the cluster itself is. A provisioning server restore from backup is a routine recovery path.

PostgreSQL adds: external daemon dependency, auth configuration, connection string secrets, and a deployment complexity step that conflicts with the "drop a binary and run it" install model. These costs are only justified when SQLite's write concurrency ceiling is actually hit.

SQLite WAL mode with a read connection pool of 4-8 connections handles the expected load: writes are serialized (one writer), reads are concurrent. At 500 nodes, the heaviest write burst is 500 concurrent deploy event inserts — WAL queues them with millisecond latency, not a correctness issue.

The inflection point where SQLite becomes a problem is approximately:

- Sensor polling at 1-minute intervals across 1000 nodes × 20 sensors = 20,000 writes/minute = 333 writes/second. SQLite WAL sustains ~2,000-5,000 simple writes/second on a modern SSD. This headroom covers ~5,000-15,000 nodes before write throughput becomes a constraint.
- Concurrent deploy jobs: 500 nodes deploying simultaneously = 500 MAC lookups in a burst. All reads, all concurrent. Not a problem for SQLite.

The practical constraint is not throughput — it is operational: at 500+ nodes, sites expect Prometheus metrics, structured audit logs, and potentially HA for the provisioning server. These often co-evolve with a PostgreSQL requirement from ops teams.

---

## Decision

**SQLite is the permanent default. PostgreSQL is an optional backend, supported starting in v1.1, required at nothing.**

The threshold for recommending PostgreSQL: ~500 nodes with sensor polling enabled at sub-5-minute intervals, OR any requirement for provisioning server HA (active-passive or active-active). Below that threshold, SQLite with WAL is the right answer and PostgreSQL is overhead.

To preserve the migration path without painting into a corner:

1. **`ImageStore` interface is the only persistence boundary.** All database access goes through typed query wrappers in `pkg/db/`. No raw SQL outside `pkg/db/`. No SQLite-specific types, pragmas, or extension functions in application code.

2. **No SQLite-specific query features in schema.** No FTS5, no JSON1 functions in application queries (JSON columns exist but are read/written as TEXT blobs in Go, not manipulated in SQL). This ensures any SQLite query is portable to PostgreSQL with at most a dialect substitution.

3. **Migration files are plain SQL, dialect-neutral.** Integer timestamps (not SQLite's datetime() function), TEXT for UUIDs, no SQLite-specific AUTOINCREMENT or ON CONFLICT syntax in the migration files used by the Go migration runner.

4. **`pkg/db/db.go` accepts a DSN string.** The `openDB(dsn string)` function returns a `*sql.DB`. Swapping in a PostgreSQL DSN is a configuration change, not a code change. The migration runner detects the driver and applies the appropriate dialect layer (a thin shim that rewrites `?` placeholders to `$1`-style for PostgreSQL).

5. **Purge policy for sensor readings.** The `sensor_readings` table is the one high-volume table. It gets a daily purge job (retain 30 days). This is essential for keeping SQLite file size manageable at scale. PostgreSQL partitioning is the right answer at 1000+ nodes, but that is a v1.1 concern.

---

## Consequences

- The single-binary install model is preserved. SQLite is embedded; no external services required.
- PostgreSQL support in v1.1 is an additive feature, not a migration. Sites that start on SQLite and grow past 500 nodes run `clonr db migrate --to postgres --dsn <pg-dsn>` — a one-time export/import operation.
- SQLite-specific anti-patterns (raw SQL with `?` placeholders used inconsistently, JSON manipulation in SQL, relying on ROWID) are prohibited from the codebase by code review policy, not just this ADR. This is enforced by the `pkg/db/` boundary.
- The Postgres migration path does NOT require downtime beyond the cutover window. The migration tool exports all rows to a staging Postgres instance, validates counts, then cuts over the DSN. Blob files on disk are unaffected.
