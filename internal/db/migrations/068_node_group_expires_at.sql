-- 068: Add optional allocation expiration timestamp to node_groups (Sprint F, v1.5.0).
-- expires_at is a nullable Unix timestamp. NULL means no expiration is set.
-- warning_sent_days is a JSON array of integer day thresholds for which a warning
-- email has already been sent (e.g. [30, 14]) to prevent duplicate sends.
ALTER TABLE node_groups ADD COLUMN expires_at INTEGER;
ALTER TABLE node_groups ADD COLUMN expiration_warning_sent TEXT DEFAULT '[]';
