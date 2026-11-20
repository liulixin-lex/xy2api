-- Run after migration 322 in the target database. This script only reads counts.
-- psql "$AUDIT_DATABASE_URL" -X -f tools/billing-integrity/openai-tier-preflight.sql
\set ON_ERROR_STOP on
BEGIN TRANSACTION READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '2s';

SELECT count(*) FILTER (WHERE openai_tier_policy_version = 1) AS legacy_groups,
       count(*) FILTER (WHERE openai_tier_policy_version = 2) AS explicit_groups,
       count(*) FILTER (WHERE free_openai_ultrafast) AS free_ultrafast_groups
FROM groups WHERE deleted_at IS NULL AND platform IN ('openai', 'composite');

SELECT count(*) FILTER (WHERE fast_multiplier IS NOT NULL) AS explicit_fast_cards,
       count(*) FILTER (WHERE ultrafast_multiplier IS NOT NULL) AS explicit_ultrafast_cards
FROM channel_model_pricing WHERE platform = 'openai' AND billing_mode = 'token';

SELECT count(*) AS group_ultrafast_cards
FROM groups g
CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(g.model_pricing) = 'array' THEN g.model_pricing ELSE '[]'::jsonb END
) AS card
WHERE g.deleted_at IS NULL AND card->>'ultrafast_multiplier' IS NOT NULL;

SELECT state, count(*) AS pending,
       count(*) FILTER (WHERE command #> '{Usage,ServiceTierDetails}' IS NOT NULL
                         AND command #> '{Usage,ServiceTierDetails}' <> 'null'::jsonb) AS with_tier_evidence
FROM usage_billing_intents WHERE state <> 'settled' OR cache_pending
GROUP BY state ORDER BY state;

COMMIT;
