-- Migration 074: TECH-TRIG monitoring state table (Sprint M, v1.11.0).
--
-- Implements the D27 Bucket 2 observability layer. Each of the four technical
-- trigger signals defined in decisions.md D27 gets one row in this table.
-- A background worker evaluates each trigger on a 10-minute tick and writes
-- the result here. Admin UI reads this table via GET /api/v1/admin/tech-triggers.
--
-- Trigger names (canonical, matches the handler constants):
--   t1_postgresql  — SQLite cluster scale / write contention threshold
--   t2_framework   — Frontend LOC ceiling / operator-marked framework friction
--   t3_multitenant — Second-tenant arrival (manual signal)
--   t4_log_archive — Audit/log storage pressure threshold
--
-- Schema:
--   trigger_name       TEXT PK  — canonical name (t1_postgresql, etc.)
--   current_value_json TEXT     — JSON: {"node_count":N,"contention_rate":F,...}
--   threshold_json     TEXT     — JSON: same shape; thresholds for each metric
--   fired_at           INTEGER  — unix timestamp when trigger first fired; NULL = not fired
--   last_evaluated_at  INTEGER  — unix timestamp of last background evaluation
--   manual_signal      INTEGER  — operator-set boolean override (T2 friction, T3)
--
-- The "fired" state is: fired_at IS NOT NULL.
-- Reset (admin action) sets fired_at = NULL, manual_signal = 0.

CREATE TABLE IF NOT EXISTS tech_trig_state (
    trigger_name       TEXT    NOT NULL PRIMARY KEY,
    current_value_json TEXT    NOT NULL DEFAULT '{}',
    threshold_json     TEXT    NOT NULL DEFAULT '{}',
    fired_at           INTEGER,               -- NULL = not fired
    last_evaluated_at  INTEGER,               -- NULL = never evaluated
    manual_signal      INTEGER NOT NULL DEFAULT 0  -- 1 = operator marked
);

-- Seed one row per trigger with sensible defaults.
-- Thresholds are documented in docs/tech-triggers.md:
--   T1: node_count >= 500, contention_rate >= 5 events/sec sustained 5 min
--   T2: js_loc >= 5000
--   T3: purely manual signal
--   T4: log_bytes >= 53687091200 (50 GiB)

INSERT OR IGNORE INTO tech_trig_state (trigger_name, current_value_json, threshold_json) VALUES
    ('t1_postgresql',  '{"node_count":0,"contention_rate":0.0}',    '{"node_count":500,"contention_rate":5.0}'),
    ('t2_framework',   '{"js_loc":0}',                              '{"js_loc":5000}'),
    ('t3_multitenant', '{}',                                        '{}'),
    ('t4_log_archive', '{"log_bytes":0}',                           '{"log_bytes":53687091200}');
