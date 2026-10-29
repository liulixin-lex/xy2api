-- Nullable observability only; no backfill or table rewrite. ALTER takes ACCESS EXCLUSIVE.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS end_to_end_first_output_ms BIGINT NULL;
ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS end_to_end_first_output_ms BIGINT NULL,
    ADD COLUMN IF NOT EXISTS recovery_outcome VARCHAR(32) NULL;
