-- Read-only inventory for a 0.2.6 database before upgrading to share scheduling.
-- Contains counts only. The false acknowledgements must be reviewed explicitly.
BEGIN READ ONLY;
WITH raw AS (
    SELECT p.group_id, r.item, r.ord
    FROM scheduling_group_policies p
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE WHEN jsonb_typeof(p.policy->'accounts') = 'array'
             THEN p.policy->'accounts' ELSE '[]'::jsonb END
    ) WITH ORDINALITY r(item, ord)
), resolved AS (
    SELECT raw.*, a.id,
           (raw.group_id = 0 OR EXISTS (
               SELECT 1 FROM account_groups ag WHERE ag.group_id = raw.group_id AND ag.account_id = a.id
           )) AS member,
           ROW_NUMBER() OVER (PARTITION BY raw.group_id, a.id ORDER BY raw.ord) AS duplicate_rank
    FROM raw
    LEFT JOIN accounts a ON a.deleted_at IS NULL AND a.id = CASE
        WHEN jsonb_typeof(raw.item) = 'object' AND raw.item->>'account_id' ~ '^[1-9][0-9]{0,17}$'
        THEN (raw.item->>'account_id')::BIGINT END
), malformed_lists AS (
    SELECT COUNT(*) AS n FROM scheduling_group_policies
    WHERE jsonb_typeof(policy->'accounts') IS DISTINCT FROM 'array'
)
SELECT jsonb_build_object(
    'controlled_mode', COALESCE((SELECT mode FROM scheduling_system_mode WHERE singleton), 'controlled') = 'controlled',
    'legacy_rule_count', (SELECT COUNT(*) FROM raw) + (SELECT n FROM malformed_lists),
    'zero_weight_count', (SELECT COUNT(*) FROM resolved WHERE id IS NOT NULL AND member AND duplicate_rank = 1 AND item->>'traffic_weight' ~ '^0+$'),
    'skipped_rule_count', (SELECT COUNT(*) FROM resolved WHERE id IS NULL OR NOT member OR duplicate_rank > 1) + (SELECT n FROM malformed_lists),
    'cutover_acknowledged', false,
    'skipped_rules_reviewed', false,
    'legacy_clients_acknowledged', false
);
COMMIT;
