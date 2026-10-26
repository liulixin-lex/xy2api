-- Remove the cache observability tables after retiring the cache features.
-- Migration 279 shipped in v0.4.7 and remains unchanged. Databases upgrading
-- directly from v0.4.6 create these tables in 279 and remove them here during
-- the same startup. Rollback to v0.4.7 is no longer supported.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

DROP TABLE IF EXISTS cache_trace_hourly;
DROP TABLE IF EXISTS cache_observability_events;
DROP TABLE IF EXISTS cache_northstar_hours;
DROP TABLE IF EXISTS cache_northstar_rollup_state;
DROP TABLE IF EXISTS cache_northstar_hourly;
