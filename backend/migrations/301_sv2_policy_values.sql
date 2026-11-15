-- Preserve legacy JSON keys for the preceding release. Only v4 is read by v2.
WITH policy AS (
    SELECT key, value::jsonb AS body
    FROM settings
    WHERE key = 'group_quality_gate_policies'
), entries AS (
    SELECT p.key, e.key AS group_id, e.value AS entry,
           CASE WHEN jsonb_typeof(e.value->'v4') = 'object' THEN e.value->'v4'
                WHEN jsonb_typeof(e.value->'v3') = 'object' THEN jsonb_build_object(
                    'enabled', COALESCE(e.value->'v3'->'enabled', 'false'::jsonb),
                    'ttft_seconds', COALESCE(e.value->'v3'->'max_ttft_p90_seconds', '15'::jsonb),
                    'max_error_rate', COALESCE(e.value->'v3'->'max_error_rate', '0.10'::jsonb),
                    'min_cache_ratio', COALESCE(e.value->'v3'->'min_cache_keep_rate', '0.50'::jsonb),
                    'takeover_idle_seconds', 900)
                ELSE jsonb_build_object(
                    'enabled', COALESCE(e.value->'enabled', 'false'::jsonb),
                    'ttft_seconds', 15, 'max_error_rate', 0.10,
                    'min_cache_ratio', 0.50, 'takeover_idle_seconds', 900)
           END AS v4
    FROM policy p CROSS JOIN LATERAL jsonb_each(COALESCE(p.body->'groups', '{}'::jsonb)) e
), converted AS (
    SELECT key, jsonb_object_agg(group_id,
        entry || jsonb_build_object('v4',
            jsonb_set(v4, '{max_error_rate}',
                to_jsonb(GREATEST(COALESCE((v4->>'max_error_rate')::numeric, 0.05), 0.05)))
        )) AS groups
    FROM entries GROUP BY key
)
UPDATE settings s
SET value = jsonb_set(
        jsonb_set(p.body, '{groups}', COALESCE(c.groups, '{}'::jsonb)),
        '{version}', to_jsonb(COALESCE((p.body->>'version')::bigint, 0) + 1))::text,
    updated_at = NOW()
FROM policy p LEFT JOIN converted c ON c.key = p.key
WHERE s.key = p.key;
