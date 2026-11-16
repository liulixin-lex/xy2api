-- Existing evidence keeps version 1; only the corrected recorder writes version 2.
-- Fail before ALTER can queue behind live traffic.
LOCK TABLE account_attempt_samples IN ACCESS EXCLUSIVE MODE NOWAIT;

ALTER TABLE account_attempt_samples
    ADD COLUMN IF NOT EXISTS capture_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS first_event_ms INTEGER,
    ADD COLUMN IF NOT EXISTS first_answer_ms INTEGER,
    ADD COLUMN IF NOT EXISTS error_class SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS http_status SMALLINT,
    ADD COLUMN IF NOT EXISTS status_origin SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS output_bytes INTEGER;
