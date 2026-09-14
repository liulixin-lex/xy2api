ALTER TABLE accounts ADD COLUMN IF NOT EXISTS iq_check JSONB NOT NULL
    DEFAULT '{"enabled":false,"interval_minutes":15,"status":"unknown"}'::jsonb;

CREATE TABLE IF NOT EXISTS account_iq_check_results (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    lease_token TEXT NOT NULL UNIQUE,
    prompt_version TEXT NOT NULL DEFAULT 'candy-v1',
    model TEXT NOT NULL DEFAULT 'gpt-6-astra',
    effort TEXT NOT NULL DEFAULT 'low',
    status TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('smart','degraded','unknown')),
    answer TEXT NOT NULL DEFAULT '' CHECK (octet_length(answer) <= 65536),
    reason TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    latency_ms BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS account_iq_check_results_account_idx
    ON account_iq_check_results(account_id, id DESC);

-- Account deletion is soft in the application, so the FK alone is insufficient.
CREATE OR REPLACE FUNCTION cleanup_account_iq_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
        DELETE FROM account_iq_check_results WHERE account_id = NEW.id;
        NEW.iq_check = '{"enabled":false,"interval_minutes":15,"status":"unknown"}'::jsonb;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER account_iq_check_soft_delete BEFORE UPDATE OF deleted_at ON accounts
    FOR EACH ROW EXECUTE FUNCTION cleanup_account_iq_check();
