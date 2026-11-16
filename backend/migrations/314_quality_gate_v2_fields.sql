-- Nullable fields and a constant boolean default avoid rewriting historical rows.
-- Acquire every lock before any ALTER, without waiting behind live traffic.
LOCK TABLE groups, account_groups, usage_logs, gateway_unbilled_requests
    IN ACCESS EXCLUSIVE MODE NOWAIT;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS min_upstream_rate DECIMAL(10,4);

ALTER TABLE account_groups
    ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS profit_basis JSONB,
    ADD COLUMN IF NOT EXISTS dispatch_phases JSONB,
    ADD COLUMN IF NOT EXISTS first_output_ms INTEGER;

ALTER TABLE gateway_unbilled_requests
    ADD COLUMN IF NOT EXISTS profit_basis JSONB,
    ADD COLUMN IF NOT EXISTS dispatch_phases JSONB;
