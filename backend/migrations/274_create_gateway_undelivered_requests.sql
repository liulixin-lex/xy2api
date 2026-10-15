-- xy2api gateway resilience (design §8): requests whose client disconnected
-- before any semantic output. They are not billed (unless the per-key abuse
-- guard applies) and are visible to administrators only. Rows are removed by
-- the ops retention cleanup.

CREATE TABLE IF NOT EXISTS gateway_undelivered_requests (
    id                      BIGSERIAL PRIMARY KEY,
    request_id              VARCHAR(128) NOT NULL DEFAULT '',
    client_request_id       VARCHAR(128) NOT NULL DEFAULT '',
    user_id                 BIGINT NULL,
    api_key_id              BIGINT NULL,
    group_id                BIGINT NULL,
    account_id              BIGINT NULL,
    platform                VARCHAR(32) NOT NULL DEFAULT '',
    model                   VARCHAR(255) NOT NULL DEFAULT '',
    inbound_endpoint        VARCHAR(128) NOT NULL DEFAULT '',
    disconnect_stage        VARCHAR(32) NOT NULL DEFAULT '',
    duration_ms             BIGINT NOT NULL DEFAULT 0,
    upstream_usage_json     JSONB NULL,
    estimated_upstream_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    billed                  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_gateway_undelivered_created_at
    ON gateway_undelivered_requests (created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_undelivered_api_key_created_at
    ON gateway_undelivered_requests (api_key_id, created_at);
CREATE INDEX IF NOT EXISTS idx_gateway_undelivered_user_created_at
    ON gateway_undelivered_requests (user_id, created_at);
