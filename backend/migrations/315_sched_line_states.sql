-- Preserve the legacy tables so a previous binary can still be rolled back.
CREATE TABLE IF NOT EXISTS sched_line_states (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    line TEXT NOT NULL CHECK (line IN ('ttfo','error','cache')),
    identity SMALLINT NOT NULL DEFAULT 0 CHECK (identity IN (0,1,2)),
    trial BOOLEAN NOT NULL DEFAULT FALSE,
    level SMALLINT NOT NULL DEFAULT 0 CHECK (level BETWEEN 0 AND 4),
    admissions INTEGER NOT NULL DEFAULT 0,
    pending_at TIMESTAMPTZ,
    epoch TIMESTAMPTZ,
    entry TIMESTAMPTZ,
    until TIMESTAMPTZ,
    trial_until TIMESTAMPTZ,
    good_since TIMESTAMPTZ,
    last_rear TIMESTAMPTZ,
    evaluated_at TIMESTAMPTZ,
    basis JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id,model,group_id,line)
);
ALTER TABLE sched_decisions ADD COLUMN IF NOT EXISTS line TEXT NOT NULL DEFAULT '';
-- Preserve all four actions from migration 300 while the old ledger ages out.
ALTER TABLE sched_decisions DROP CONSTRAINT IF EXISTS sched_decisions_action_check;
ALTER TABLE sched_decisions ADD CONSTRAINT sched_decisions_action_check CHECK
    (action IN ('enter','renew','release','reset',
                'trial','trial_pass','trial_fail','trial_timeout','relax','clear'));
