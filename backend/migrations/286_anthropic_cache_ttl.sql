-- Preserve existing keys' client TTL choices; new keys follow the administrator.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS anthropic_cache_ttl_mode varchar(7) NOT NULL DEFAULT 'off';
ALTER TABLE api_keys ALTER COLUMN anthropic_cache_ttl_mode SET DEFAULT 'inherit';
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'api_keys'::regclass AND conname = 'api_keys_anthropic_cache_ttl_mode_check') THEN
        ALTER TABLE api_keys ADD CONSTRAINT api_keys_anthropic_cache_ttl_mode_check CHECK (anthropic_cache_ttl_mode IN ('inherit', 'off', '5m', '1h'));
    END IF;
END $$;

-- Historical and non-Anthropic requests remain NULL; no backfill or index.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS anthropic_cache_ttl_policy varchar(3);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS anthropic_cache_ttl_applied boolean;
