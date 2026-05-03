-- 045_webhooks.sql — S4-2: webhook subscriptions and delivery log.
--
-- webhook_subscriptions: operator-configured outbound webhook endpoints.
--   events: JSON array of subscribed event types (deploy.complete, deploy.failed,
--           verify_boot.timeout, image.ready).
--   created_at, updated_at: Unix timestamps.
--
-- webhook_deliveries: per-attempt delivery log for audit/retry observability.
--   event: the event type that triggered this delivery.
--   payload_json: the JSON payload sent.
--   status: 'success' | 'failed'.
--   http_status: HTTP response code from the target URL (0 if connection refused).
--   attempt: which attempt number (1 = first, 2 = first retry, 3 = second retry).
--   delivered_at: Unix timestamp.

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id          TEXT    PRIMARY KEY,
    url         TEXT    NOT NULL,
    events      TEXT    NOT NULL DEFAULT '[]',  -- JSON array
    secret      TEXT    NOT NULL DEFAULT '',     -- HMAC secret for signature (optional)
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id           TEXT    PRIMARY KEY,
    webhook_id   TEXT    NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event        TEXT    NOT NULL,
    payload_json TEXT    NOT NULL,
    status       TEXT    NOT NULL,  -- 'success' | 'failed'
    http_status  INTEGER NOT NULL DEFAULT 0,
    attempt      INTEGER NOT NULL DEFAULT 1,
    error_msg    TEXT    NOT NULL DEFAULT '',
    delivered_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook_id
    ON webhook_deliveries(webhook_id);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_event
    ON webhook_deliveries(event);
