-- Share scheduling: one versioned policy per group. Group 0 governs ungrouped
-- traffic (all accounts in simple mode). Every group starts on the original
-- Sub2API scheduler; nothing routes differently until an administrator saves
-- scheduler 'shadow' or 'share'.
CREATE TABLE IF NOT EXISTS share_scheduling_policies (
    group_id      BIGINT PRIMARY KEY CHECK (group_id >= 0),
    version       BIGINT NOT NULL CHECK (version > 0),
    scheduler     VARCHAR(16) NOT NULL DEFAULT 'sub2api' CHECK (scheduler IN ('sub2api', 'shadow', 'share')),
    policy        JSONB NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
    imported_from VARCHAR(64),
    updated_by    BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_share_scheduling_policies_scheduler
    ON share_scheduling_policies (scheduler) WHERE scheduler <> 'sub2api';

-- Import the retired group account policies as inactive drafts. Priority
-- becomes the tier (falling back to the account priority), traffic weight the
-- share weight; weight 0 meant "no traffic" and becomes a drained account.
-- Malformed rules and accounts that left the group are skipped instead of
-- failing the upgrade.
DO $$
BEGIN
    IF to_regclass('scheduling_group_policies') IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO share_scheduling_policies (group_id, version, scheduler, policy, imported_from)
    SELECT
        p.group_id,
        1,
        'sub2api',
        jsonb_build_object(
            'schema', 1,
            'scheduler', 'sub2api',
            'accounts', COALESCE(rules.accounts, '[]'::jsonb)
        ),
        'scheduling_group_policies:v' || p.version
    FROM scheduling_group_policies p
    LEFT JOIN LATERAL (
        SELECT jsonb_agg(item ORDER BY ord) AS accounts
        FROM (
            SELECT DISTINCT ON (a.id)
                r.ord,
                jsonb_build_object(
                    'account_id', a.id,
                    'tier', CASE
                        WHEN r.rule->>'priority' ~ '^-?[0-9]{1,10}$'
                            THEN GREATEST(LEAST((r.rule->>'priority')::BIGINT, 2147483647), -2147483648)
                        ELSE a.priority
                    END,
                    'weight', CASE
                        WHEN r.rule->>'traffic_weight' ~ '^[0-9]{1,7}$'
                            THEN GREATEST(LEAST((r.rule->>'traffic_weight')::BIGINT, 1000000), 1)
                        ELSE 1
                    END
                )
                || CASE
                    WHEN r.rule->>'traffic_weight' ~ '^0+$' THEN '{"drain": true}'::jsonb
                    ELSE '{}'::jsonb
                END AS item
            FROM jsonb_array_elements(
                     CASE WHEN jsonb_typeof(p.policy->'accounts') = 'array' THEN p.policy->'accounts' ELSE '[]'::jsonb END
                 ) WITH ORDINALITY AS r(rule, ord)
            JOIN accounts a
              ON a.id = CASE
                     WHEN jsonb_typeof(r.rule) = 'object' AND r.rule->>'account_id' ~ '^[1-9][0-9]{0,17}$'
                         THEN (r.rule->>'account_id')::BIGINT
                 END
             AND a.deleted_at IS NULL
            WHERE p.group_id = 0
               OR EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id = a.id AND ag.group_id = p.group_id)
            ORDER BY a.id, r.ord
        ) deduplicated
    ) rules ON TRUE
    WHERE p.group_id >= 0
    ON CONFLICT (group_id) DO NOTHING;
END
$$;
