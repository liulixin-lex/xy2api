-- Nullable observability only; no backfill or table rewrite. ALTER takes ACCESS EXCLUSIVE.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS attempt_first_output_ms BIGINT NULL;
