-- Share scheduling baseline (openspec replace-controlled-with-share-scheduling,
-- tasks 0.4-0.6). Read-only: run against a replica or inside a read-only
-- transaction, before switching any group and again during a canary.
--
--   psql "$DATABASE_URL" -v days=7 -f tools/share-scheduling/baseline.sql
--
-- Token columns follow usage_logs: input_tokens excludes cache reads and
-- cache writes, so the prompt is input + cache_creation + cache_read and the
-- uncached input is input + cache_creation.

\if :{?days}
\else
\set days 7
\endif

BEGIN TRANSACTION READ ONLY;

\echo '== 0.4 shares per group and account (requests, tokens, standard cost)'
WITH per_account AS (
    SELECT group_id, account_id,
           count(*) AS requests,
           sum(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS tokens,
           sum(total_cost) AS standard_cost
    FROM usage_logs
    WHERE created_at >= now() - make_interval(days => :days)
    GROUP BY group_id, account_id
)
SELECT group_id, account_id, requests, tokens, round(standard_cost, 4) AS standard_cost,
       round(requests::numeric / NULLIF(sum(requests) OVER w, 0), 4) AS request_share,
       round(tokens::numeric / NULLIF(sum(tokens) OVER w, 0), 4) AS token_share,
       round(standard_cost / NULLIF(sum(standard_cost) OVER w, 0), 4) AS cost_share
FROM per_account
WINDOW w AS (PARTITION BY group_id)
ORDER BY group_id NULLS FIRST, requests DESC;

\echo '== 0.4 cache read ratio and first-token percentiles by group, model and prompt size'
SELECT group_id, model,
       CASE
           WHEN prompt < 1024 THEN 'a <1k'
           WHEN prompt < 8192 THEN 'b 1k-8k'
           WHEN prompt < 32768 THEN 'c 8k-32k'
           WHEN prompt < 131072 THEN 'd 32k-128k'
           ELSE 'e >=128k'
       END AS prompt_bucket,
       count(*) AS requests,
       round(sum(cache_read_tokens)::numeric / NULLIF(sum(prompt), 0), 4) AS cache_read_ratio,
       count(first_token_ms) AS ttft_samples,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) AS ttft_p50_ms,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms) AS ttft_p95_ms,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms) AS ttft_p99_ms
FROM (
    SELECT group_id, model, cache_read_tokens, first_token_ms,
           input_tokens + cache_creation_tokens + cache_read_tokens AS prompt
    FROM usage_logs
    WHERE created_at >= now() - make_interval(days => :days)
) u
GROUP BY 1, 2, 3
ORDER BY 1 NULLS FIRST, 2, 3;

\echo '== 0.4 retired controlled scheduling state (the tables stay until the D7 export)'
SELECT (SELECT value FROM settings WHERE key = 'scheduling_system_mode') AS scheduling_system_mode,
       (SELECT count(*) FROM scheduling_group_policies) AS controlled_group_policies;

\echo '== 0.5 first-token slope per 1k uncached input tokens (the wait budget per 1k tokens)'
-- Least squares over streamed requests; the per-bucket medians show whether
-- the relation is close to linear before trusting the slope.
WITH samples AS (
    SELECT lower(substring(model FROM '^[A-Za-z]+(?:-[A-Za-z]+)?')) AS family,
           (input_tokens + cache_creation_tokens) / 1000.0 AS uncached_k,
           first_token_ms
    FROM usage_logs
    WHERE created_at >= now() - make_interval(days => :days)
      AND stream AND first_token_ms BETWEEN 1 AND 120000
)
SELECT family, count(*) AS samples,
       round(regr_slope(first_token_ms, uncached_k)::numeric, 1) AS ms_per_1k_uncached,
       round(regr_intercept(first_token_ms, uncached_k)::numeric, 0) AS intercept_ms,
       round(regr_r2(first_token_ms, uncached_k)::numeric, 3) AS r2,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE uncached_k < 1) AS p50_lt_1k,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE uncached_k >= 1 AND uncached_k < 8) AS p50_1k_8k,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE uncached_k >= 8 AND uncached_k < 32) AS p50_8k_32k,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE uncached_k >= 32) AS p50_ge_32k
FROM samples
GROUP BY family
HAVING count(*) >= 100
ORDER BY samples DESC;

\echo '== 0.6 explicit client session coverage by platform and endpoint'
-- usage_logs.session_id holds the client session header. The Anthropic key
-- source split (metadata_user_id / cacheable_content / message_content_fallback)
-- is only in the "sticky.hash_source" log entries; count their "source" field.
SELECT a.platform, COALESCE(u.inbound_endpoint, '') AS inbound_endpoint,
       count(*) AS requests,
       round(avg((COALESCE(u.session_id, '') <> '')::int), 4) AS with_client_session
FROM usage_logs u
LEFT JOIN accounts a ON a.id = u.account_id
WHERE u.created_at >= now() - make_interval(days => :days)
GROUP BY 1, 2
ORDER BY requests DESC;

ROLLBACK;
