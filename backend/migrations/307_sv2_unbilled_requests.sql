-- Requests without an arranged billing task never create placeholder usage rows.
CREATE TABLE IF NOT EXISTS gateway_unbilled_requests (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT,
    account_id BIGINT,
    platform TEXT NOT NULL,
    model TEXT NOT NULL,
    stream BOOLEAN NOT NULL,
    request_type SMALLINT,
    result SMALLINT NOT NULL CHECK (result BETWEEN 1 AND 4),
    error_class TEXT NOT NULL DEFAULT '',
    http_status INTEGER,
    switches SMALLINT NOT NULL DEFAULT 0,
    fallback BOOLEAN NOT NULL DEFAULT FALSE,
    takeover BOOLEAN NOT NULL DEFAULT FALSE,
    first_output_ms INTEGER,
    duration_ms INTEGER NOT NULL,
    input_tokens INTEGER,
    cache_read_tokens INTEGER,
    cache_creation_tokens INTEGER
);
CREATE INDEX IF NOT EXISTS idx_gateway_unbilled_created ON gateway_unbilled_requests (created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_unbilled_group_created ON gateway_unbilled_requests (group_id, created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_unbilled_account_created ON gateway_unbilled_requests (account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_unbilled_user_created ON gateway_unbilled_requests (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_unbilled_api_key_created ON gateway_unbilled_requests (api_key_id, created_at);
