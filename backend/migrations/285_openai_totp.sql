-- Managed OpenAI login/TOTP secrets are encrypted separately from accounts.
-- Stop all workers and resolve uncertain tasks before rollback. Keep these
-- tables and the fixed encryption key until all secrets have been recovered.
CREATE TABLE IF NOT EXISTS openai_totp_credentials (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 revision BIGINT NOT NULL CHECK (revision > 0),
 email VARCHAR(254) NOT NULL,
 payload TEXT NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS openai_totp_tasks (
 id BIGSERIAL PRIMARY KEY,
 account_id BIGINT REFERENCES accounts(id) ON DELETE CASCADE,
 operation VARCHAR(24) NOT NULL CHECK(operation IN ('login','totp_rotation')),
 action VARCHAR(16) NOT NULL CHECK(action IN ('login','rotate','verify_new','verify_old')),
 state VARCHAR(16) NOT NULL CHECK(state IN ('queued','running','enrolling','prepared','activating','verifying','uncertain','succeeded','failed','ready','imported')),
 error_code VARCHAR(64) NOT NULL DEFAULT '',
 has_candidate BOOLEAN NOT NULL DEFAULT FALSE,
 payload TEXT NOT NULL,
 claim_hash VARCHAR(64) NOT NULL DEFAULT '',
 worker_id VARCHAR(128) NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ,
 started_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_openai_totp_tasks_queue ON openai_totp_tasks(id) WHERE state='queued';
CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_totp_tasks_active_account ON openai_totp_tasks(account_id)
 WHERE operation='totp_rotation' AND state IN ('queued','running','enrolling','prepared','activating','verifying','uncertain');
CREATE TABLE IF NOT EXISTS openai_totp_workers (
 worker_id VARCHAR(128) PRIMARY KEY,
 ready_until TIMESTAMPTZ NOT NULL
);

-- Ordinary account deletion/identity edits must not destroy recovery material.
-- Share the service transaction lock so queue creation and account changes are
-- serialized even when they originate from different backend replicas/APIs.
CREATE OR REPLACE FUNCTION protect_active_openai_totp_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'UPDATE' THEN
  IF OLD.platform IS NOT DISTINCT FROM NEW.platform
   AND OLD.type IS NOT DISTINCT FROM NEW.type
   AND OLD.parent_account_id IS NOT DISTINCT FROM NEW.parent_account_id
   AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at
   AND LOWER(TRIM(OLD.credentials->>'email')) IS NOT DISTINCT FROM LOWER(TRIM(NEW.credentials->>'email'))
   AND OLD.credentials->>'auth_mode' IS NOT DISTINCT FROM NEW.credentials->>'auth_mode'
  THEN RETURN NEW; END IF;
 END IF;
 PERFORM pg_advisory_xact_lock(741285037442);
 IF EXISTS(SELECT 1 FROM openai_totp_tasks WHERE account_id=OLD.id AND operation='totp_rotation'
  AND state IN ('queued','running','enrolling','prepared','activating','verifying','uncertain')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='OPENAI_TOTP_ACCOUNT_LOCKED';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS protect_active_openai_totp_account ON accounts;
CREATE TRIGGER protect_active_openai_totp_account BEFORE UPDATE OR DELETE ON accounts
 FOR EACH ROW EXECUTE FUNCTION protect_active_openai_totp_account();
