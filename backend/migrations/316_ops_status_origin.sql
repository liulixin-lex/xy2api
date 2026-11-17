-- Bound the hot-table lock wait so autovacuum can yield without an unbounded
-- queue of blocked gateway writes. Historical provenance remains NULL.
SET LOCAL lock_timeout = '5s';
LOCK TABLE ops_error_logs IN ACCESS EXCLUSIVE MODE;

ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS status_origin SMALLINT,
    ADD COLUMN IF NOT EXISTS client_status INTEGER;
