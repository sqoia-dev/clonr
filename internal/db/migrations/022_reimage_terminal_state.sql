-- Add terminal-state detail columns to reimage_requests.
-- exit_code, exit_name, and phase are populated on deploy-failed callbacks
-- so operators can triage failures without reading logs.
ALTER TABLE reimage_requests ADD COLUMN exit_code  INTEGER;
ALTER TABLE reimage_requests ADD COLUMN exit_name  TEXT;
ALTER TABLE reimage_requests ADD COLUMN phase      TEXT;
