-- STATE diagnostics contain identifiers and counters only; never credentials,
-- ticket blobs, prompts, response bodies or arbitrary upstream error messages.
CREATE TABLE IF NOT EXISTS codex_ticket_events (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL UNIQUE,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL,
    at TIMESTAMPTZ NOT NULL,
    stage VARCHAR(32) NOT NULL,
    outcome VARCHAR(64) NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS codex_ticket_events_account_cursor ON codex_ticket_events(account_id,id DESC);
CREATE INDEX IF NOT EXISTS codex_ticket_events_expiry ON codex_ticket_events(at);
CREATE TABLE IF NOT EXISTS codex_ticket_hourly (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL,
    hour TIMESTAMPTZ NOT NULL,
    stage VARCHAR(32) NOT NULL,
    outcome VARCHAR(64) NOT NULL,
    count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY(account_id,model,hour,stage,outcome)
);
CREATE INDEX IF NOT EXISTS codex_ticket_hourly_expiry ON codex_ticket_hourly(hour);
