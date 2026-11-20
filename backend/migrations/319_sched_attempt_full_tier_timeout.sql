-- The migration runner wraps this file in a transaction. Historical samples
-- have no full-tier timeout proof.
SET LOCAL lock_timeout = '5s';
LOCK TABLE account_attempt_samples IN ACCESS EXCLUSIVE MODE;

ALTER TABLE account_attempt_samples
    ADD COLUMN IF NOT EXISTS full_tier_first_output_timeout BOOLEAN NOT NULL DEFAULT FALSE;
