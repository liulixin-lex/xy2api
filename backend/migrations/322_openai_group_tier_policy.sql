SET LOCAL lock_timeout = '5s';

ALTER TABLE groups
    ADD COLUMN openai_fast_mode VARCHAR NOT NULL DEFAULT 'inherit',
    ADD COLUMN openai_ultrafast_mode VARCHAR NOT NULL DEFAULT 'inherit',
    ADD COLUMN openai_tier_policy_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN free_openai_ultrafast BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE groups
SET openai_fast_mode = 'force_on'
WHERE force_openai_fast AND platform IN ('openai', 'composite');

UPDATE groups
SET force_openai_fast = FALSE, free_openai_fast = FALSE
WHERE platform NOT IN ('openai', 'composite')
  AND (force_openai_fast OR free_openai_fast);

-- Old binaries write the legacy booleans. Keep their version-1 rows in sync.
CREATE FUNCTION sync_legacy_group_openai_tier_policy()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.openai_tier_policy_version = 1 THEN
        IF NEW.platform NOT IN ('openai', 'composite') THEN
            NEW.force_openai_fast := FALSE;
            NEW.free_openai_fast := FALSE;
        END IF;
        NEW.openai_fast_mode := CASE WHEN NEW.force_openai_fast THEN 'force_on' ELSE 'inherit' END;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER sync_legacy_group_openai_tier_policy
BEFORE INSERT OR UPDATE ON groups
FOR EACH ROW EXECUTE FUNCTION sync_legacy_group_openai_tier_policy();

ALTER TABLE groups
    ADD CONSTRAINT groups_openai_tier_mode_chk CHECK (
        openai_fast_mode IN ('inherit', 'force_on', 'force_off')
        AND openai_ultrafast_mode IN ('inherit', 'force_on', 'force_off')
        AND (openai_fast_mode <> 'force_on' OR openai_ultrafast_mode <> 'force_on')
    ),
    ADD CONSTRAINT groups_openai_tier_legacy_chk CHECK (
        openai_tier_policy_version IN (1, 2)
        AND force_openai_fast = (openai_fast_mode = 'force_on')
        AND (openai_tier_policy_version = 2 OR
            (openai_fast_mode <> 'force_off' AND openai_ultrafast_mode = 'inherit'
             AND NOT free_openai_ultrafast))
    ),
    ADD CONSTRAINT groups_openai_tier_platform_chk CHECK (
        platform IN ('openai', 'composite') OR
        (openai_fast_mode = 'inherit' AND openai_ultrafast_mode = 'inherit'
         AND NOT free_openai_fast AND NOT free_openai_ultrafast)
    );

ALTER TABLE channel_model_pricing ADD COLUMN ultrafast_multiplier NUMERIC(12,6);
ALTER TABLE channel_model_pricing ADD CONSTRAINT channel_model_pricing_ultrafast_multiplier_chk
    CHECK (ultrafast_multiplier IS NULL OR
           (ultrafast_multiplier > 0 AND ultrafast_multiplier <= 999999.999999));

ALTER TABLE usage_logs ADD COLUMN service_tier_details JSONB;

CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id BIGINT;
BEGIN
    target_group_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.is_exclusive IS NOT DISTINCT FROM NEW.is_exclusive
       AND OLD.show_exclusive_badge IS NOT DISTINCT FROM NEW.show_exclusive_badge
       AND OLD.system_prompt_config IS NOT DISTINCT FROM NEW.system_prompt_config
       AND OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation
       AND OLD.platform IS NOT DISTINCT FROM NEW.platform
       AND OLD.subscription_type IS NOT DISTINCT FROM NEW.subscription_type
       AND OLD.rate_multiplier IS NOT DISTINCT FROM NEW.rate_multiplier
       AND OLD.peak_rate_enabled IS NOT DISTINCT FROM NEW.peak_rate_enabled
       AND OLD.peak_start IS NOT DISTINCT FROM NEW.peak_start
       AND OLD.peak_end IS NOT DISTINCT FROM NEW.peak_end
       AND OLD.peak_rate_multiplier IS NOT DISTINCT FROM NEW.peak_rate_multiplier
       AND OLD.min_upstream_rate IS NOT DISTINCT FROM NEW.min_upstream_rate
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
       AND OLD.long_context_pricing_enabled IS NOT DISTINCT FROM NEW.long_context_pricing_enabled
       AND OLD.long_context_pricing_scope IS NOT DISTINCT FROM NEW.long_context_pricing_scope
       AND OLD.long_context_pricing_models IS NOT DISTINCT FROM NEW.long_context_pricing_models
       AND OLD.model_allowlist IS NOT DISTINCT FROM NEW.model_allowlist
       AND OLD.model_pricing IS NOT DISTINCT FROM NEW.model_pricing
       AND OLD.force_openai_fast IS NOT DISTINCT FROM NEW.force_openai_fast
       AND OLD.free_openai_fast IS NOT DISTINCT FROM NEW.free_openai_fast
       AND OLD.openai_fast_mode IS NOT DISTINCT FROM NEW.openai_fast_mode
       AND OLD.openai_ultrafast_mode IS NOT DISTINCT FROM NEW.openai_ultrafast_mode
       AND OLD.free_openai_ultrafast IS NOT DISTINCT FROM NEW.free_openai_ultrafast
       AND OLD.openai_tier_policy_version IS NOT DISTINCT FROM NEW.openai_tier_policy_version
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;
    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.group_id = target_group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
