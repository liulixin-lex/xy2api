-- Unknown gateway facts stay NULL; existing rows are not backfilled.

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS request_scope TEXT,
    ADD COLUMN IF NOT EXISTS usage_observed BOOLEAN,
    ADD COLUMN IF NOT EXISTS usage_source TEXT,
    ADD COLUMN IF NOT EXISTS terminal_event_seen BOOLEAN,
    ADD COLUMN IF NOT EXISTS terminal_event_valid BOOLEAN,
    ADD COLUMN IF NOT EXISTS response_body_type TEXT,
    ADD COLUMN IF NOT EXISTS degradations JSONB,
    ADD COLUMN IF NOT EXISTS pricing_source JSONB;

ALTER TABLE gateway_unbilled_requests
    ADD COLUMN IF NOT EXISTS request_scope TEXT,
    ADD COLUMN IF NOT EXISTS usage_observed BOOLEAN,
    ADD COLUMN IF NOT EXISTS usage_source TEXT,
    ADD COLUMN IF NOT EXISTS terminal_event_seen BOOLEAN,
    ADD COLUMN IF NOT EXISTS terminal_event_valid BOOLEAN,
    ADD COLUMN IF NOT EXISTS response_body_type TEXT,
    ADD COLUMN IF NOT EXISTS degradations JSONB;

ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS degradations JSONB;
