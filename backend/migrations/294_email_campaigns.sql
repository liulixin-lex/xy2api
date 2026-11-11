-- Marketing mail is durable and independent of transactional notification opt-outs.
CREATE TABLE IF NOT EXISTS email_campaigns (
    id BIGSERIAL PRIMARY KEY,
    subject TEXT NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
    body_markdown TEXT NOT NULL CHECK (octet_length(body_markdown) BETWEEN 1 AND 100000),
    audience JSONB NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN ('sending','paused','completed','canceled')),
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    pause_reason VARCHAR(16) NOT NULL DEFAULT '' CHECK (pause_reason IN ('','manual','failures','auth')),
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS email_campaigns_one_active ON email_campaigns ((true)) WHERE status IN ('sending','paused');
CREATE TABLE IF NOT EXISTS email_campaign_recipients (
    campaign_id BIGINT NOT NULL REFERENCES email_campaigns(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    email TEXT NOT NULL,
    email_norm TEXT NOT NULL CHECK (email_norm = lower(btrim(email))),
    status VARCHAR(16) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed','skipped')),
    claim_version BIGINT NOT NULL DEFAULT 0,
    outcome_uncertain BOOLEAN NOT NULL DEFAULT false,
    claimed_at TIMESTAMPTZ,
    sent_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (campaign_id, user_id),
    UNIQUE (campaign_id, email_norm)
);
-- Keep snapshots even if a user is physically removed; dispatch rechecks users.
CREATE INDEX IF NOT EXISTS email_campaign_recipients_status ON email_campaign_recipients (campaign_id,status);
CREATE INDEX IF NOT EXISTS email_campaign_recipients_pending ON email_campaign_recipients (campaign_id) WHERE status='pending';
CREATE INDEX IF NOT EXISTS email_campaign_recipients_sending ON email_campaign_recipients (claimed_at) WHERE status='sending';
CREATE TABLE IF NOT EXISTS email_marketing_suppressions (
    id BIGSERIAL PRIMARY KEY,
    email_norm TEXT NOT NULL UNIQUE CHECK (email_norm = lower(btrim(email_norm))),
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    source VARCHAR(16) NOT NULL CHECK (source IN ('admin','unsubscribe')),
    note TEXT NOT NULL DEFAULT '',
    campaign_id BIGINT REFERENCES email_campaigns(id) ON DELETE SET NULL,
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS email_marketing_suppressions_user ON email_marketing_suppressions (user_id) WHERE user_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS email_send_log (
    id BIGSERIAL PRIMARY KEY,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    campaign_id BIGINT REFERENCES email_campaigns(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS email_send_log_time ON email_send_log (sent_at);
