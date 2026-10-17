-- Gateway resilience metrics (design §14) as JSON lines, for
-- metrics_report.py collect. Read-only; run with
--   PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=120s' \
--   psql -X -At -v ON_ERROR_STOP=1 -v hours=48 -f metrics.sql
-- Every line is one JSON object of aggregates (no user identifiers or
-- amounts); baseline.sql prints the same numbers as tables for reading.

\if :{?hours}
\else
\set hours 48
\endif
\set QUIET on
\pset footer off
SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY;
SET statement_timeout = '300s';

WITH routing AS (
  SELECT group_id, created_at
  FROM ops_error_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
    AND error_phase = 'routing' AND status_code = 503
), minutes AS (
  SELECT DISTINCT group_id, date_trunc('minute', created_at) AS minute FROM routing WHERE group_id IS NOT NULL
), islands AS (
  SELECT group_id, minute - make_interval(mins => (row_number() OVER (PARTITION BY group_id ORDER BY minute))::int) AS island
  FROM minutes
), streak_ev AS (
  SELECT api_key_id, account_id, created_at, false AS ok
  FROM ops_error_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
    AND account_id IS NOT NULL AND api_key_id IS NOT NULL AND error_phase IN ('upstream', 'stream', 'network')
  UNION ALL
  SELECT api_key_id, account_id, created_at, true
  FROM usage_logs
  WHERE created_at >= now() - make_interval(hours => :hours)
), streak_seq AS (
  SELECT api_key_id, account_id, ok,
         sum(CASE WHEN ok THEN 1 ELSE 0 END) OVER (PARTITION BY api_key_id, account_id ORDER BY created_at) AS run_id
  FROM streak_ev
), streak_runs AS (
  SELECT count(*) AS failures FROM streak_seq WHERE NOT ok GROUP BY api_key_id, account_id, run_id
), platforms AS (
  SELECT a.platform,
         count(*) AS requests,
         round(100.0 * sum(l.cache_read_tokens) / nullif(sum(l.input_tokens + l.cache_read_tokens + l.cache_creation_tokens), 0), 2) AS cache_read_pct,
         percentile_cont(0.5) WITHIN GROUP (ORDER BY l.first_token_ms) FILTER (WHERE l.stream AND l.first_token_ms > 0) AS ttft_p50_ms,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY l.first_token_ms) FILTER (WHERE l.stream AND l.first_token_ms > 0) AS ttft_p95_ms
  FROM usage_logs l
  JOIN accounts a ON a.id = l.account_id
  WHERE l.created_at >= now() - make_interval(hours => :hours)
  GROUP BY 1
)
SELECT json_build_object(
  'hours', :hours,
  'until', now(),
  'requests', (SELECT count(*) FROM usage_logs WHERE created_at >= now() - make_interval(hours => :hours)),
  'error_rows', (SELECT count(*) FROM ops_error_logs WHERE created_at >= now() - make_interval(hours => :hours)),
  'no_account_503', (SELECT count(*) FROM routing),
  'no_account_longest_minutes', COALESCE((SELECT max(run) FROM (SELECT count(*) AS run FROM islands GROUP BY group_id, island) r), 0),
  'balance_disabled_events', (SELECT count(*) FROM ops_system_logs
    WHERE created_at >= now() - make_interval(hours => :hours)
      AND message = 'account_disabled_auth_error'
      AND (extra::text ILIKE '%balance%' OR extra::text ILIKE '%余额%' OR extra::text ILIKE '%quota%' OR extra::text ILIKE '%credit%')),
  'balance_error_accounts_now', (SELECT count(*) FROM accounts
    WHERE deleted_at IS NULL AND status = 'error'
      AND (error_message ILIKE '%balance%' OR error_message ILIKE '%余额%' OR error_message ILIKE '%quota%' OR error_message ILIKE '%credit%')),
  'insufficient_shown_positive_balance', (SELECT count(*) FROM ops_error_logs e JOIN users u ON u.id = e.user_id
    WHERE e.created_at >= now() - make_interval(hours => :hours) AND u.balance > 0
      AND (e.error_message ILIKE '%insufficient%balance%' OR e.error_message ILIKE '%余额不足%'
           OR e.error_type IN ('billing_error', 'insufficient_balance'))),
  'key_account_streak_p99', (SELECT percentile_disc(0.99) WITHIN GROUP (ORDER BY failures) FROM streak_runs),
  'platforms', COALESCE((SELECT json_object_agg(platform, json_build_object(
      'requests', requests, 'cache_read_pct', cache_read_pct, 'ttft_p50_ms', ttft_p50_ms, 'ttft_p95_ms', ttft_p95_ms))
    FROM platforms), '{}'::json)
);

SELECT to_regclass('public.gateway_undelivered_requests') IS NOT NULL AS has_undelivered_table \gset
\if :has_undelivered_table
SELECT json_build_object(
  'undelivered', (SELECT count(*) FROM gateway_undelivered_requests WHERE created_at >= now() - make_interval(hours => :hours)),
  'undelivered_billed', (SELECT count(*) FROM gateway_undelivered_requests WHERE created_at >= now() - make_interval(hours => :hours) AND billed),
  'served_by_fallback_group', (SELECT count(*) FROM usage_logs WHERE created_at >= now() - make_interval(hours => :hours) AND origin_group_id IS NOT NULL),
  'balance_paused', (SELECT count(*) FROM accounts WHERE deleted_at IS NULL
      AND temp_unschedulable_reason LIKE 'balance:%' AND temp_unschedulable_until > now())
);
\endif
