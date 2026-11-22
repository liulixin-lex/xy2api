SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '5s';

-- Optional observation has no default, backfill, index or validation constraint.
-- Acquire both locks immediately so a busy table cannot queue billing writes.
LOCK TABLE usage_logs, gateway_unbilled_requests IN ACCESS EXCLUSIVE MODE NOWAIT;

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS collab_observation JSONB;
ALTER TABLE gateway_unbilled_requests ADD COLUMN IF NOT EXISTS collab_observation JSONB;
