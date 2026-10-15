-- First output calibration (tasks 0.4): first token latency of successful
-- streams per model and reasoning effort over the last :days days, and the
-- share that would exceed the 60/120/180 second tiers. Read-only; run with
--   PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=300s' \
--   psql -X -v days=7 -f first_token_calibration.sql

\if :{?days}
\else
\set days 7
\endif
\pset footer off
\pset null '-'

SELECT COALESCE(NULLIF(requested_model, ''), model) AS model,
       COALESCE(NULLIF(reasoning_effort, ''), '-') AS effort,
       count(*) AS n,
       round(percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) ::numeric / 1000, 1) AS p50_s,
       round(percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms) ::numeric / 1000, 1) AS p95_s,
       round(percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms) ::numeric / 1000, 1) AS p99_s,
       round(percentile_cont(0.999) WITHIN GROUP (ORDER BY first_token_ms) ::numeric / 1000, 1) AS p999_s,
       round(100.0 * count(*) FILTER (WHERE first_token_ms > 60000) / count(*), 3) AS over_60s_pct,
       round(100.0 * count(*) FILTER (WHERE first_token_ms > 120000) / count(*), 3) AS over_120s_pct,
       round(100.0 * count(*) FILTER (WHERE first_token_ms > 180000) / count(*), 3) AS over_180s_pct
FROM usage_logs
WHERE created_at >= now() - make_interval(days => :days)
  AND stream AND first_token_ms > 0 AND image_count = 0
GROUP BY 1, 2
HAVING count(*) >= 50
ORDER BY n DESC
LIMIT 60;
