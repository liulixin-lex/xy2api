-- Keep billing/attempt duration unchanged; administrator request rows use this paired with end-to-end first output.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS end_to_end_duration_ms BIGINT NULL;
