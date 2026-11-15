ALTER TABLE account_attempt_samples
    ADD COLUMN IF NOT EXISTS input_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS cache_creation_tokens INTEGER;
