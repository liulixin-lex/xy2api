-- Nullable observation only; no default, index, or backfill.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS resilience_timing JSONB NULL;
