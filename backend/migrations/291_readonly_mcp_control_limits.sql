ALTER TABLE mcp_oauth_clients
    ADD COLUMN created_ip_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_oauth_pending
    ADD COLUMN client_id TEXT REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE,
    ADD COLUMN ip_key_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE mcp_oauth_clients
    ALTER COLUMN expires_at SET DEFAULT NOW() + INTERVAL '1 day';
UPDATE mcp_oauth_clients c
SET expires_at = LEAST(c.expires_at, c.created_at + INTERVAL '1 day')
WHERE NOT EXISTS (
    SELECT 1 FROM mcp_oauth_grants g
    WHERE g.client_id = c.id AND g.revoked_at IS NULL AND g.expires_at > NOW()
);

CREATE INDEX mcp_oauth_clients_ip_expiry_idx
    ON mcp_oauth_clients(created_ip_hash, expires_at);
CREATE INDEX mcp_oauth_pending_ip_expiry_idx
    ON mcp_oauth_pending(ip_key_hash, expires_at);
CREATE INDEX mcp_oauth_pending_client_expiry_idx
    ON mcp_oauth_pending(client_id, expires_at);
