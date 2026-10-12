-- Keep migration 270 immutable. Legacy zero-weight rules meant no traffic,
-- including existing sessions; drain alone cannot preserve that contract.
-- Correct only untouched imported drafts, never administrator-saved policies.
DO $$
BEGIN
    IF to_regclass('scheduling_group_policies') IS NULL THEN RETURN; END IF;

    WITH corrected AS (
        SELECT p.group_id,
               jsonb_agg(
                   CASE WHEN zero_ids.account_id IS NOT NULL
                        THEN (r.rule - 'drain' - 'drain_until' - 'drain_fallback')
                             || '{"disabled":true}'::jsonb
                        ELSE r.rule END ORDER BY r.ord
               ) AS accounts
        FROM share_scheduling_policies p
        JOIN scheduling_group_policies legacy ON legacy.group_id = p.group_id
        CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(p.policy->'accounts') = 'array' THEN p.policy->'accounts' ELSE '[]'::jsonb END) WITH ORDINALITY r(rule, ord)
        LEFT JOIN LATERAL (
            SELECT first_rule.account_id
            FROM (
                SELECT DISTINCT ON (item->>'account_id') item->>'account_id' AS account_id, item
                FROM jsonb_array_elements(
                    CASE WHEN jsonb_typeof(legacy.policy->'accounts') = 'array'
                         THEN legacy.policy->'accounts' ELSE '[]'::jsonb END
                ) WITH ORDINALITY source(item, ord)
                WHERE jsonb_typeof(item) = 'object'
                ORDER BY item->>'account_id', ord
            ) first_rule
            WHERE first_rule.account_id = r.rule->>'account_id'
              AND first_rule.item->>'traffic_weight' ~ '^0+$'
        ) zero_ids ON TRUE
        WHERE p.version = 1 AND p.updated_by IS NULL AND p.scheduler = 'sub2api'
          AND p.imported_from = 'scheduling_group_policies:v' || legacy.version
        GROUP BY p.group_id
    )
    UPDATE share_scheduling_policies p
    SET policy = jsonb_set(p.policy, '{accounts}', corrected.accounts),
        version = p.version + 1, updated_at = NOW()
    FROM corrected
    WHERE corrected.group_id = p.group_id AND corrected.accounts <> p.policy->'accounts';
END
$$;
