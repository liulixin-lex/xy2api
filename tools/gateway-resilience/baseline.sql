-- Gateway resilience baseline (design §14). Read-only; run with
--   PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=120s' \
--   psql -X -v hours=48 -f baseline.sql
-- Every section prints aggregates only (no user identifiers or amounts).
-- Metrics that need proxy or application logs are produced by
-- caddy_cuts.py; see README.md.

\if :{?hours}
\else
\set hours 48
\endif
\pset footer off
\pset null '-'

\echo '== window'
SELECT now() - make_interval(hours => :hours) AS since, now() AS until, :hours AS hours;

\echo '== volume: successful requests and recorded errors'
SELECT
  (SELECT count(*) FROM usage_logs WHERE created_at >= now() - make_interval(hours => :hours)) AS usage_rows,
  (SELECT count(*) FROM ops_error_logs WHERE created_at >= now() - make_interval(hours => :hours)) AS error_rows;

\echo '== errors by phase and status (top 20)'
SELECT error_phase, status_code, count(*) AS n
FROM ops_error_logs
WHERE created_at >= now() - make_interval(hours => :hours)
GROUP BY 1, 2 ORDER BY n DESC LIMIT 20;

\echo '== no-account 503 (routing phase): total and groups affected'
SELECT count(*) AS no_account_503, count(DISTINCT group_id) AS groups
FROM ops_error_logs
WHERE created_at >= now() - make_interval(hours => :hours)
  AND error_phase = 'routing' AND status_code = 503;

\echo '== longest run of consecutive minutes with no-account 503 per group (top 10; target: none over 5)'
WITH m AS (
  SELECT DISTINCT group_id, date_trunc('minute', created_at) AS minute
  FROM ops_error_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
    AND error_phase = 'routing' AND status_code = 503 AND group_id IS NOT NULL
), islands AS (
  SELECT group_id, minute,
         minute - make_interval(mins => (row_number() OVER (PARTITION BY group_id ORDER BY minute))::int) AS island
  FROM m
)
SELECT 'group#' || dense_rank() OVER (ORDER BY max(run) DESC) AS grp, max(run) AS longest_minutes
FROM (SELECT group_id, island, count(*) AS run FROM islands GROUP BY 1, 2) r
GROUP BY group_id ORDER BY longest_minutes DESC LIMIT 10;

\echo '== accounts disabled for balance reasons (log events and current state)'
SELECT
  (SELECT count(*) FROM ops_system_logs
    WHERE created_at >= now() - make_interval(hours => :hours)
      AND message = 'account_disabled_auth_error'
      AND (extra::text ILIKE '%balance%' OR extra::text ILIKE '%余额%' OR extra::text ILIKE '%quota%' OR extra::text ILIKE '%credit%')) AS disabled_events,
  (SELECT count(*) FROM accounts
    WHERE deleted_at IS NULL AND status = 'error'
      AND (error_message ILIKE '%balance%' OR error_message ILIKE '%余额%' OR error_message ILIKE '%quota%' OR error_message ILIKE '%credit%')) AS accounts_in_error_now;

\echo '== "insufficient balance" shown to users whose balance is positive now (approximation)'
SELECT count(*) AS shown, count(*) FILTER (WHERE u.balance > 0) AS with_positive_balance_now
FROM ops_error_logs e
JOIN users u ON u.id = e.user_id
WHERE e.created_at >= now() - make_interval(hours => :hours)
  AND (e.error_message ILIKE '%insufficient%balance%' OR e.error_message ILIKE '%余额不足%'
       OR e.error_type IN ('billing_error', 'insufficient_balance'));

\echo '== consecutive failures of one API key on one account (session proxy): P50 / P99 / max'
WITH ev AS (
  SELECT api_key_id, account_id, created_at, false AS ok
  FROM ops_error_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
    AND account_id IS NOT NULL AND api_key_id IS NOT NULL AND error_phase IN ('upstream', 'stream', 'network')
  UNION ALL
  SELECT api_key_id, account_id, created_at, true
  FROM usage_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
), seq AS (
  SELECT api_key_id, account_id, ok,
         sum(CASE WHEN ok THEN 1 ELSE 0 END) OVER (PARTITION BY api_key_id, account_id ORDER BY created_at) AS run_id
  FROM ev
), runs AS (
  SELECT count(*) AS failures FROM seq WHERE NOT ok GROUP BY api_key_id, account_id, run_id
)
SELECT count(*) AS runs,
       percentile_disc(0.5) WITHIN GROUP (ORDER BY failures) AS p50,
       percentile_disc(0.99) WITHIN GROUP (ORDER BY failures) AS p99,
       max(failures) AS max
FROM runs;

\echo '== cache read share and first token latency of streams by platform'
SELECT a.platform,
       count(*) AS requests,
       round(100.0 * sum(l.cache_read_tokens) / nullif(sum(l.input_tokens + l.cache_read_tokens + l.cache_creation_tokens), 0), 2) AS cache_read_pct,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY l.first_token_ms) FILTER (WHERE l.stream AND l.first_token_ms > 0) AS ttft_p50_ms,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY l.first_token_ms) FILTER (WHERE l.stream AND l.first_token_ms > 0) AS ttft_p95_ms
FROM usage_logs l
JOIN accounts a ON a.id = l.account_id
WHERE l.created_at >= now() - make_interval(hours => :hours)
GROUP BY 1 ORDER BY requests DESC;

\echo '== 0.4.0 only (skipped on older schemas): undelivered requests, fallback usage, abuse billing'
SELECT to_regclass('public.gateway_undelivered_requests') IS NOT NULL AS has_undelivered_table \gset
\if :has_undelivered_table
SELECT disconnect_stage, count(*) AS n, count(*) FILTER (WHERE billed) AS billed
FROM gateway_undelivered_requests
WHERE created_at >= now() - make_interval(hours => :hours)
GROUP BY 1 ORDER BY n DESC;
SELECT count(*) FILTER (WHERE origin_group_id IS NOT NULL) AS served_by_fallback_group,
       count(*) FILTER (WHERE billing_mode = 'undelivered_abuse') AS undelivered_abuse_billed
FROM usage_logs
WHERE created_at >= now() - make_interval(hours => :hours);
SELECT COALESCE(extra->'resilience_balance'->>'state', 'OK') AS balance_state, count(*) AS accounts
FROM accounts WHERE deleted_at IS NULL GROUP BY 1 ORDER BY 2 DESC;
\endif
