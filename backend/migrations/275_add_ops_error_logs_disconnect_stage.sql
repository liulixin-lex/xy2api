-- xy2api gateway resilience (design §8.2): client disconnect diagnostics in
-- ops error logs. Nullable columns, metadata-only change.

ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS disconnect_stage VARCHAR(32) NULL,
    ADD COLUMN IF NOT EXISTS delivered_bytes BIGINT NULL;
