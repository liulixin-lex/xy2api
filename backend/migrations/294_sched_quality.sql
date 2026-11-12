CREATE TABLE IF NOT EXISTS account_attempt_samples (
  attempt_id         UUID        PRIMARY KEY,
  started_at         TIMESTAMPTZ NOT NULL,
  finished_at        TIMESTAMPTZ NOT NULL,
  account_id         BIGINT      NOT NULL,
  group_id           BIGINT      NOT NULL DEFAULT 0,
  model              TEXT        NOT NULL,
  request_class      SMALLINT    NOT NULL,
  stream             BOOLEAN     NOT NULL,
  warm               BOOLEAN     NOT NULL,
  ttfo_ms            INTEGER,
  censored_ms        INTEGER,
  outcome            SMALLINT    NOT NULL,
  detail             TEXT        NOT NULL DEFAULT '',
  prompt_tokens      INTEGER,
  prev_prompt_tokens INTEGER,
  cache_read_tokens  INTEGER,
  session_key        TEXT        NOT NULL DEFAULT '',
  request_id         TEXT        NOT NULL DEFAULT '',
  inserted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_attempt_samples_account_time ON account_attempt_samples (account_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_attempt_samples_inserted ON account_attempt_samples (inserted_at);
CREATE INDEX IF NOT EXISTS idx_attempt_samples_started ON account_attempt_samples (started_at);

CREATE TABLE IF NOT EXISTS account_quality_states (
  account_id BIGINT      NOT NULL,
  model      TEXT        NOT NULL,
  group_id   BIGINT      NOT NULL,
  state      SMALLINT    NOT NULL,            -- 0 normal, 1 cooling
  level      SMALLINT    NOT NULL DEFAULT 0,  -- index of the next cooldown step, 0..6
  reason     TEXT        NOT NULL DEFAULT '', -- cooling: speed | error | cache | acute_timeout | test_speed | test_error | test_cache; normal: '' | unverified
  since      TIMESTAMPTZ NOT NULL,            -- start of the current state / current cooling round; evidence and tests before it are ignored
  until      TIMESTAMPTZ,                     -- cooling only
  PRIMARY KEY (account_id, model, group_id)
);

CREATE TABLE IF NOT EXISTS account_quality_overrides (
  account_id  BIGINT      NOT NULL,
  group_id    BIGINT      NOT NULL DEFAULT 0,   -- 0 = all groups of the account
  mode        SMALLINT    NOT NULL,             -- 1 exempt, 2 demote
  expires_at  TIMESTAMPTZ,
  reason      TEXT        NOT NULL DEFAULT '',
  operator_id BIGINT      NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (account_id, group_id)
);

CREATE TABLE IF NOT EXISTS account_quality_tests (
  id            UUID        PRIMARY KEY,
  account_id    BIGINT      NOT NULL,
  model         TEXT        NOT NULL,             -- evidence key
  trigger       SMALLINT    NOT NULL,             -- 0 auto (cooldown expiry), 1 admin
  operator_id   BIGINT      NOT NULL DEFAULT 0,
  status        SMALLINT    NOT NULL,             -- 0 running, 1 done, 2 aborted
  abort_reason  TEXT        NOT NULL DEFAULT '',  -- not_schedulable | busy | timeout | error (only when no turn was recorded)
  started_at    TIMESTAMPTZ NOT NULL,
  finished_at   TIMESTAMPTZ,
  turns         JSONB       NOT NULL DEFAULT '[]',-- one object per turn, see TestTurn in section 12
  input_tokens  BIGINT      NOT NULL DEFAULT 0,
  cached_tokens BIGINT      NOT NULL DEFAULT 0,
  output_tokens BIGINT      NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_quality_tests_running ON account_quality_tests (account_id, model) WHERE status = 0;
CREATE INDEX IF NOT EXISTS idx_quality_tests_account_time ON account_quality_tests (account_id, started_at DESC);
