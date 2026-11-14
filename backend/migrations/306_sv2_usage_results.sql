-- Nullable without defaults: historical and uncovered requests stay unknown.
-- request_id remains the billing idempotency key.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS gateway_request_id TEXT,
    ADD COLUMN IF NOT EXISTS result SMALLINT,
    ADD COLUMN IF NOT EXISTS switches SMALLINT,
    ADD COLUMN IF NOT EXISTS fallback BOOLEAN,
    ADD COLUMN IF NOT EXISTS takeover BOOLEAN;
