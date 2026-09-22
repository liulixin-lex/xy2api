-- Durable usage export control plane; historical usage rows are unchanged.
CREATE TABLE usage_export_tasks (
 id text PRIMARY KEY,
 owner_id bigint NOT NULL REFERENCES users(id),
 scope text NOT NULL CHECK (scope IN ('user','admin')),
 fingerprint text NOT NULL,
 status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed','canceled','expired','deleted')),
 phase text NOT NULL DEFAULT 'queued',
 generation bigint NOT NULL DEFAULT 0,
 processed_rows bigint NOT NULL DEFAULT 0,
 total_rows bigint,
 snapshot_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 heartbeat_at timestamptz,
 finished_at timestamptz,
 expires_at timestamptz,
 size_bytes bigint NOT NULL DEFAULT 0,
 reserved_bytes bigint NOT NULL DEFAULT 0,
 sha256 text NOT NULL DEFAULT '',
 error_code text NOT NULL DEFAULT '',
 storage_key text NOT NULL DEFAULT '',
 cleaned boolean NOT NULL DEFAULT false,
 data jsonb NOT NULL
);
CREATE UNIQUE INDEX usage_export_one_active ON usage_export_tasks(owner_id) WHERE status IN ('queued','running');
CREATE INDEX usage_export_queue ON usage_export_tasks(status,created_at);
CREATE INDEX usage_export_owner ON usage_export_tasks(owner_id,scope,created_at DESC);
CREATE TABLE usage_export_keys (
 owner_id bigint NOT NULL,
 scope text NOT NULL,
 key text NOT NULL,
 fingerprint text NOT NULL,
 task_id text NOT NULL REFERENCES usage_export_tasks(id) ON DELETE CASCADE,
 PRIMARY KEY(owner_id,scope,key)
);
CREATE TABLE usage_export_tickets (
 hash text PRIMARY KEY,
 task_id text NOT NULL REFERENCES usage_export_tasks(id) ON DELETE CASCADE,
 identity jsonb NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE TABLE usage_export_rate (
 owner_id bigint NOT NULL,
 scope text NOT NULL,
 window_at timestamptz NOT NULL,
 count integer NOT NULL,
 PRIMARY KEY(owner_id,scope)
);
