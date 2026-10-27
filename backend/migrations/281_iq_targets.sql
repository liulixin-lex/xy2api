CREATE TABLE IF NOT EXISTS account_iq_targets (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model TEXT NOT NULL CHECK (octet_length(model) BETWEEN 1 AND 256),
    enabled BOOLEAN NOT NULL DEFAULT false,
    reasoning_effort TEXT NOT NULL DEFAULT 'low',
    interval_minutes INT NOT NULL DEFAULT 5 CHECK (interval_minutes BETWEEN 1 AND 1440),
    timeout_seconds INT NOT NULL DEFAULT 120 CHECK (timeout_seconds BETWEEN 30 AND 300),
    revision TEXT NOT NULL,
    verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (verdict IN ('unknown','smart','degraded')),
    verdict_at TIMESTAMPTZ,
    gate_model TEXT,
    degraded_streak INT NOT NULL DEFAULT 0,
    failure_streak INT NOT NULL DEFAULT 0,
    paused_reason TEXT NOT NULL DEFAULT '',
    last_run_at TIMESTAMPTZ,
    last_result TEXT NOT NULL DEFAULT '',
    last_reason TEXT NOT NULL DEFAULT '',
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    not_before TIMESTAMPTZ,
    manual_requested_at TIMESTAMPTZ,
    lease_token TEXT UNIQUE,
    lease_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, model)
);
CREATE INDEX IF NOT EXISTS account_iq_targets_due ON account_iq_targets(next_run_at) WHERE enabled;
CREATE INDEX IF NOT EXISTS account_iq_targets_manual ON account_iq_targets(manual_requested_at) WHERE manual_requested_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS account_iq_targets_lease ON account_iq_targets(lease_until) WHERE lease_until IS NOT NULL;

ALTER TABLE account_iq_check_results ADD COLUMN IF NOT EXISTS outbound_model TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_results ADD COLUMN IF NOT EXISTS mode TEXT NOT NULL DEFAULT 'normal';
CREATE INDEX IF NOT EXISTS account_iq_check_results_model_idx ON account_iq_check_results(account_id, model, id DESC);
CREATE INDEX IF NOT EXISTS account_iq_check_results_started_idx ON account_iq_check_results(started_at);
CREATE INDEX IF NOT EXISTS account_iq_check_results_open_idx ON account_iq_check_results(id) WHERE finished_at IS NULL;

ALTER TABLE account_iq_check_attempts DROP CONSTRAINT IF EXISTS account_iq_check_attempts_attempt_no_check;
ALTER TABLE account_iq_check_attempts ADD CONSTRAINT account_iq_check_attempts_attempt_no_check CHECK (attempt_no BETWEEN 1 AND 5);
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS effort TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS expected TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS answer TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS reported_model TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS model_class TEXT NOT NULL DEFAULT '';
ALTER TABLE account_iq_check_attempts ADD COLUMN IF NOT EXISTS challenge JSONB;

CREATE OR REPLACE FUNCTION cleanup_account_iq_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
        DELETE FROM account_iq_check_results WHERE account_id = NEW.id;
        DELETE FROM account_iq_targets WHERE account_id = NEW.id;
        NEW.iq_check = '{"enabled":false,"interval_minutes":15,"status":"unknown"}'::jsonb;
    END IF;
    RETURN NEW;
END $$;

INSERT INTO account_iq_targets(account_id, model, enabled, reasoning_effort, interval_minutes, timeout_seconds, revision, verdict, gate_model)
SELECT a.id, m.model, COALESCE((a.iq_check->>'enabled')::boolean,false), COALESCE(NULLIF(a.iq_check->>'reasoning_effort',''),'low'),
       COALESCE(NULLIF(a.iq_check->>'interval_minutes','')::int,5), COALESCE(NULLIF(a.iq_check->>'timeout_seconds','')::int,120),
       gen_random_uuid()::text,
       CASE WHEN m.model = COALESCE(NULLIF(a.iq_check->>'model',''), 'gpt-6-astra') AND COALESCE((a.iq_check->>'enabled')::boolean,false) AND a.iq_check->>'last_valid_status' = 'degraded' THEN 'degraded' ELSE 'unknown' END,
       NULL
FROM accounts a
CROSS JOIN LATERAL (VALUES ('gpt-6-astra'), ('gpt-6.1-sol')) AS m(model)
WHERE a.deleted_at IS NULL AND a.platform='openai'
ON CONFLICT (account_id, model) DO NOTHING;
INSERT INTO account_iq_targets(account_id, model, enabled, reasoning_effort, interval_minutes, timeout_seconds, revision, verdict)
SELECT a.id, a.iq_check->>'model', COALESCE((a.iq_check->>'enabled')::boolean,false), COALESCE(NULLIF(a.iq_check->>'reasoning_effort',''),'low'),
       COALESCE(NULLIF(a.iq_check->>'interval_minutes','')::int,5), COALESCE(NULLIF(a.iq_check->>'timeout_seconds','')::int,120), gen_random_uuid()::text,
       CASE WHEN COALESCE((a.iq_check->>'enabled')::boolean,false) AND a.iq_check->>'last_valid_status'='degraded' THEN 'degraded' ELSE 'unknown' END
FROM accounts a
WHERE a.deleted_at IS NULL AND a.platform='openai' AND NULLIF(a.iq_check->>'model','') IS NOT NULL
  AND a.iq_check->>'model' NOT IN ('gpt-6-astra','gpt-6.1-sol')
ON CONFLICT (account_id, model) DO NOTHING;
