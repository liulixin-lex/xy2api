-- Separate state and lock namespace keeps the preceding release independent.
CREATE TABLE IF NOT EXISTS sched_states (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    failing BOOLEAN NOT NULL DEFAULT FALSE,
    dims TEXT[] NOT NULL DEFAULT '{}',
    level SMALLINT NOT NULL DEFAULT 0 CHECK (level BETWEEN 0 AND 5),
    since TIMESTAMPTZ,
    until TIMESTAMPTZ,
    evidence_since TIMESTAMPTZ,
    basis JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, model, group_id)
);
CREATE TABLE IF NOT EXISTS sched_decisions (
    id BIGSERIAL PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL,
    rule_version TEXT NOT NULL,
    account_id BIGINT NOT NULL,
    model TEXT NOT NULL,
    group_id BIGINT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('enter', 'renew', 'release', 'reset')),
    dims TEXT[] NOT NULL DEFAULT '{}',
    level SMALLINT NOT NULL,
    until TIMESTAMPTZ,
    basis JSONB NOT NULL DEFAULT '{}',
    attempt_ids TEXT[] NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS sched_decisions_at_idx ON sched_decisions(at);
CREATE INDEX IF NOT EXISTS sched_decisions_account_at_idx ON sched_decisions(account_id, at DESC);
