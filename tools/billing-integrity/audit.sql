-- Read-only audit, never a backcharge script.
-- psql "$AUDIT_DATABASE_URL" -X -v ON_ERROR_STOP=1 \
--   -v since='2026-10-03T00:00:00+08:00' -v until='2026-10-08T00:00:00+08:00' \
--   -f tools/billing-integrity/audit.sql
-- Supply an explicitly bounded interval and an approved read-only credential.
-- Output includes internal IDs; keep it in a restricted operations directory.
\set ON_ERROR_STOP on
BEGIN TRANSACTION READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '2s';

SELECT state, COUNT(*) AS events,
       MIN(created_at) AS oldest_created_at,
       COUNT(*) FILTER (WHERE cache_pending) AS cache_invalidations_pending
FROM usage_billing_intents
WHERE state <> 'settled' OR cache_pending
GROUP BY state ORDER BY state;

-- Candidates ONLY: total_cost * multiplier is deliberately not computed here.
-- Zero actual_cost can be legitimate (FreeFast, explicit free prices, gifts,
-- simple mode, historical corrections). Evidence-based review must exclude it.
SELECT u.id AS usage_id, u.request_id, u.user_id, u.api_key_id, u.account_id,
       u.created_at, u.model, u.billing_type, u.subscription_id,
       u.total_cost, u.actual_cost, u.rate_multiplier, u.service_tier,
       u.input_tokens, u.output_tokens, u.cache_creation_tokens, u.cache_read_tokens,
       u.image_count, u.video_count,
       i.state AS intent_state, i.last_error_code,
       'candidate_requires_pricing_and_free_usage_review' AS disposition
FROM usage_logs u
LEFT JOIN usage_billing_intents i ON i.request_id = u.request_id AND i.api_key_id = u.api_key_id
WHERE u.created_at >= :'since'::timestamptz AND u.created_at < :'until'::timestamptz
  AND u.actual_cost = 0 AND u.total_cost > 0 AND u.rate_multiplier > 0
  AND u.subscription_id IS NULL AND u.billing_type = 0
  AND (u.input_tokens > 0 OR u.output_tokens > 0 OR u.cache_creation_tokens > 0
       OR u.cache_read_tokens > 0 OR u.image_count > 0 OR u.video_count > 0)
  AND NOT EXISTS (SELECT 1 FROM usage_billing_dedup d WHERE d.request_id = u.request_id AND d.api_key_id = u.api_key_id)
  AND NOT EXISTS (SELECT 1 FROM usage_billing_dedup_archive d WHERE d.request_id = u.request_id AND d.api_key_id = u.api_key_id)
ORDER BY u.created_at, u.id;

-- A new settled intent must have both its log and committed dedup evidence.
-- Intent-less historical rows are not classified as failed by this query.
SELECT i.request_id, i.api_key_id, i.user_id, i.created_at, i.state,
       (u.id IS NULL) AS missing_usage,
       NOT EXISTS (SELECT 1 FROM usage_billing_dedup d WHERE d.request_id = i.request_id AND d.api_key_id = i.api_key_id)
       AND NOT EXISTS (SELECT 1 FROM usage_billing_dedup_archive d WHERE d.request_id = i.request_id AND d.api_key_id = i.api_key_id)
       AS missing_settlement_evidence
FROM usage_billing_intents i
LEFT JOIN usage_logs u ON u.request_id = i.request_id AND u.api_key_id = i.api_key_id
WHERE i.created_at >= :'since'::timestamptz AND i.created_at < :'until'::timestamptz
  AND i.state = 'settled'
  AND (u.id IS NULL OR (
       NOT EXISTS (SELECT 1 FROM usage_billing_dedup d WHERE d.request_id = i.request_id AND d.api_key_id = i.api_key_id)
       AND NOT EXISTS (SELECT 1 FROM usage_billing_dedup_archive d WHERE d.request_id = i.request_id AND d.api_key_id = i.api_key_id)))
ORDER BY i.created_at;
COMMIT;
