-- Cache observation is disabled until an administrator enables northstar.
-- Raw prompts, request identifiers, API keys and session values are never stored.
CREATE TABLE IF NOT EXISTS cache_northstar_hourly (
 policy_version VARCHAR(100) NOT NULL,
 bucket_start TIMESTAMPTZ NOT NULL,
 platform VARCHAR(32) NOT NULL,
 group_id BIGINT NOT NULL,
 group_label TEXT NOT NULL DEFAULT '',
 account_id BIGINT NOT NULL,
 account_label TEXT NOT NULL DEFAULT '',
 upstream_host TEXT NOT NULL,
 client_family VARCHAR(64) NOT NULL,
 protocol VARCHAR(32) NOT NULL,
 model_key VARCHAR(100) NOT NULL,
 threshold_band SMALLINT NOT NULL CHECK (threshold_band IN (0,512,1024,2048,4096,6144)),
 has_media BOOLEAN NOT NULL,
 requests BIGINT NOT NULL DEFAULT 0,
 requests_hit BIGINT NOT NULL DEFAULT 0,
 p_tokens BIGINT NOT NULL DEFAULT 0,
 read_tokens BIGINT NOT NULL DEFAULT 0,
 read_maybe_rewritten BIGINT NOT NULL DEFAULT 0,
 o_recorded_tokens BIGINT NOT NULL DEFAULT 0,
 input_tokens_uncached BIGINT NOT NULL DEFAULT 0,
 creation_tokens BIGINT NOT NULL DEFAULT 0,
 input_cost_micros BIGINT NOT NULL DEFAULT 0,
 read_cost_micros BIGINT NOT NULL DEFAULT 0,
 creation_cost_micros BIGINT NOT NULL DEFAULT 0,
 read_x_charged_mult_micro BIGINT NOT NULL DEFAULT 0,
 o_x_charged_mult_micro BIGINT NOT NULL DEFAULT 0,
 read_x_upstream_mult_micro BIGINT NOT NULL DEFAULT 0,
 o_x_upstream_mult_micro BIGINT NOT NULL DEFAULT 0,
 upstream_covered_p_tokens BIGINT NOT NULL DEFAULT 0,
 component_mismatch_requests BIGINT NOT NULL DEFAULT 0,
 computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (policy_version,bucket_start,platform,group_id,model_key,threshold_band,has_media,account_id,client_family,protocol)
);

-- Cross-version retention has no policy_version predicate.
CREATE INDEX IF NOT EXISTS cache_northstar_hourly_bucket_start_idx
 ON cache_northstar_hourly(bucket_start);

CREATE TABLE IF NOT EXISTS cache_northstar_rollup_state (
 id SMALLINT PRIMARY KEY CHECK (id=1),
 policy_version VARCHAR(100) NOT NULL,
 policy_spec JSONB NOT NULL,
 policy_changed_at TIMESTAMPTZ NOT NULL,
 aggregated_until TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00',
 backfilled_from TIMESTAMPTZ NOT NULL DEFAULT TIMESTAMPTZ '1970-01-01 00:00:00+00',
 reconcile_day TIMESTAMPTZ,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Hour completion markers distinguish a genuine empty bucket from missing data.
CREATE TABLE IF NOT EXISTS cache_northstar_hours (
 policy_version VARCHAR(100) NOT NULL,
 bucket_start TIMESTAMPTZ NOT NULL,
 media_requests BIGINT NOT NULL DEFAULT 0,
 zero_prompt_requests BIGINT NOT NULL DEFAULT 0,
 probe_group_requests BIGINT NOT NULL DEFAULT 0,
 platform_counts JSONB NOT NULL DEFAULT '{}',
 computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (policy_version,bucket_start)
);

CREATE TABLE IF NOT EXISTS cache_observability_events (
 id BIGSERIAL PRIMARY KEY,
 at TIMESTAMPTZ NOT NULL,
 kind VARCHAR(20) NOT NULL,
 label TEXT NOT NULL,
 version VARCHAR(100),
 cold_until TIMESTAMPTZ,
 policy_key VARCHAR(32),
 instance_id VARCHAR(100),
 UNIQUE (at,kind,label)
);
CREATE INDEX IF NOT EXISTS cache_observability_events_at_idx ON cache_observability_events(at);


CREATE TABLE IF NOT EXISTS cache_trace_hourly (
 bucket_start TIMESTAMPTZ NOT NULL,
 process_id VARCHAR(64) NOT NULL,
 platform VARCHAR(32) NOT NULL,
 transport VARCHAR(32) NOT NULL,
 relay_family VARCHAR(32) NOT NULL,
 pck_class_out VARCHAR(32) NOT NULL,
 loss_class VARCHAR(64) NOT NULL,
 model_gen VARCHAR(32) NOT NULL,
 fork_kind VARCHAR(32) NOT NULL,
 requests BIGINT NOT NULL DEFAULT 0,
 p_tokens BIGINT NOT NULL DEFAULT 0,
 read_tokens BIGINT NOT NULL DEFAULT 0,
 opportunity_tokens BIGINT NOT NULL DEFAULT 0,
 computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (bucket_start,process_id,platform,transport,relay_family,pck_class_out,loss_class,model_gen,fork_kind)
);
