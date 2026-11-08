-- Nullable observation only; no default, index, or backfill.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE usage_logs
 ADD COLUMN IF NOT EXISTS body_bytes BIGINT NULL,
 ADD COLUMN IF NOT EXISTS first_read_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS body_eof_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS anchor_basis TEXT NULL,
 ADD COLUMN IF NOT EXISTS anchor_eligible BOOLEAN NULL,
 ADD COLUMN IF NOT EXISTS body_receipt_repeat BIGINT NULL,
 ADD COLUMN IF NOT EXISTS pre_dispatch_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS keepalive_commit_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS wire_status BIGINT NULL,
 ADD COLUMN IF NOT EXISTS dispatch_count BIGINT NULL,
 ADD COLUMN IF NOT EXISTS min_dispatch_remaining_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS final_deadline_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS client_family TEXT NULL,
 ADD COLUMN IF NOT EXISTS client_cancel_ms BIGINT NULL,
 ADD COLUMN IF NOT EXISTS cancel_stage TEXT NULL,
 ADD COLUMN IF NOT EXISTS cdn_observed TEXT NULL,
 ADD COLUMN IF NOT EXISTS budget_start_mode TEXT NULL,
 ADD COLUMN IF NOT EXISTS budget_t0_unix_ms BIGINT NULL;
