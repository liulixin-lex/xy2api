-- Persist the original billing decision before the money transaction. No cascading
-- foreign keys: removing an authentication/resource row must not erase a debt.
CREATE TABLE IF NOT EXISTS usage_billing_intents (
    request_id TEXT NOT NULL,
    api_key_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    command JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'settled', 'review')),
    cache_pending BOOLEAN NOT NULL DEFAULT TRUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '30 seconds',
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, api_key_id)
);
CREATE INDEX IF NOT EXISTS idx_usage_billing_intents_recovery
    ON usage_billing_intents (next_attempt_at)
    WHERE state = 'pending' OR (state = 'settled' AND cache_pending);
CREATE INDEX IF NOT EXISTS idx_usage_billing_intents_unsettled
    ON usage_billing_intents (state, created_at) WHERE state <> 'settled';
COMMENT ON TABLE usage_billing_intents IS
    'Frozen billing commands and recovery state; never infer historical settlement from an absent intent.';
