ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS show_exclusive_badge BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS system_prompt_config JSONB NOT NULL DEFAULT
        '{"prompt":"","scope":"all","models":[],"model_prompts":{}}'::jsonb;

-- Preserve all previous invalidation conditions and include the new policy.
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
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
       AND OLD.long_context_pricing_enabled IS NOT DISTINCT FROM NEW.long_context_pricing_enabled
       AND OLD.long_context_pricing_scope IS NOT DISTINCT FROM NEW.long_context_pricing_scope
       AND OLD.long_context_pricing_models IS NOT DISTINCT FROM NEW.long_context_pricing_models
       AND OLD.model_allowlist IS NOT DISTINCT FROM NEW.model_allowlist
       AND OLD.model_pricing IS NOT DISTINCT FROM NEW.model_pricing
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
