-- Dedicated OAuth control plane. Diagnostic queries use a separate login.
CREATE TABLE mcp_oauth_clients (
 id TEXT PRIMARY KEY,
 name VARCHAR(80) NOT NULL,
 redirect_uris JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW()+INTERVAL '90 days'
);
CREATE TABLE mcp_oauth_pending (
 id UUID PRIMARY KEY,
 form JSONB NOT NULL,
 user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL,
 consumed_at TIMESTAMPTZ
);
CREATE INDEX mcp_oauth_pending_expiry_idx ON mcp_oauth_pending(expires_at);
CREATE TABLE mcp_oauth_grants (
 id UUID PRIMARY KEY,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(id),
 tools JSONB NOT NULL,
 contract_version TEXT NOT NULL,
 credential_version BIGINT NOT NULL,
 issuer TEXT NOT NULL,
 resource TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL,
 revoked_at TIMESTAMPTZ
);
CREATE INDEX mcp_oauth_grants_user_idx ON mcp_oauth_grants(user_id,created_at DESC);
CREATE TABLE mcp_oauth_sessions (
 kind TEXT NOT NULL CHECK(kind IN ('code','pkce','access','refresh')),
 signature TEXT NOT NULL,
 request_id UUID NOT NULL REFERENCES mcp_oauth_grants(id) ON DELETE CASCADE,
 data JSONB NOT NULL,
 active BOOLEAN NOT NULL DEFAULT TRUE,
 expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(kind,signature)
);
CREATE INDEX mcp_oauth_sessions_request_idx ON mcp_oauth_sessions(request_id,kind);
CREATE INDEX mcp_oauth_sessions_expiry_idx ON mcp_oauth_sessions(expires_at);
CREATE TABLE mcp_access_audit (
 id BIGSERIAL PRIMARY KEY,
 occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 query_id UUID NOT NULL,
 user_id BIGINT NOT NULL,
 client_id TEXT NOT NULL,
 grant_id UUID NOT NULL,
 tool VARCHAR(100) NOT NULL,
 decision VARCHAR(20) NOT NULL,
 error_code VARCHAR(40) NOT NULL DEFAULT '',
 duration_ms BIGINT NOT NULL DEFAULT 0,
 rows_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX mcp_access_audit_time_idx ON mcp_access_audit(occurred_at DESC);
