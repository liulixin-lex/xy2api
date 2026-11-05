-- Fixed column allowlist for the pre-created xy2api_mcp_reader role.
-- Run against the application database after its normal migrations.
-- Strict startup verification also requires the explicit PUBLIC function
-- privilege review in mcp-reader-function-privileges.sql; read the runbook first.
-- This grants diagnostic reads only; role creation, login policy and network
-- access remain explicit deployment steps. No role/password is created here.

GRANT USAGE ON SCHEMA public TO xy2api_mcp_reader;

GRANT SELECT (id, username, role, status, balance, concurrency, created_at, deleted_at)
    ON TABLE public.users TO xy2api_mcp_reader;

GRANT SELECT (id, name, platform, type, status, concurrency, created_at, deleted_at)
    ON TABLE public.accounts TO xy2api_mcp_reader;

GRANT SELECT (id, name, platform, status, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, created_at, deleted_at)
    ON TABLE public.groups TO xy2api_mcp_reader;

GRANT SELECT (id, user_id, deleted_at)
    ON TABLE public.api_keys TO xy2api_mcp_reader;

GRANT SELECT (account_id, group_id)
    ON TABLE public.account_groups TO xy2api_mcp_reader;

GRANT SELECT (group_id, category_id)
    ON TABLE public.group_category_members TO xy2api_mcp_reader;

GRANT SELECT (user_id, group_id)
    ON TABLE public.user_allowed_groups TO xy2api_mcp_reader;

GRANT SELECT (id, name, status, created_at)
    ON TABLE public.channels TO xy2api_mcp_reader;

GRANT SELECT (channel_id, group_id)
    ON TABLE public.channel_groups TO xy2api_mcp_reader;

GRANT SELECT (id, user_id, api_key_id, account_id, group_id, origin_group_id, request_id, model, requested_model, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, actual_cost, duration_ms, first_token_ms, end_to_end_first_output_ms, stream, session_id, created_at)
    ON TABLE public.usage_logs TO xy2api_mcp_reader;

GRANT SELECT (id, user_id, api_key_id, account_id, group_id, request_id, client_request_id, platform, model, requested_model, error_phase, error_type, error_owner, status_code, severity, duration_ms, time_to_first_token_ms, stream, upstream_status_code, is_business_limited, disconnect_stage, upstream_errors, created_at)
    ON TABLE public.ops_error_logs TO xy2api_mcp_reader;

GRANT SELECT (id, user_id, group_id, starts_at, expires_at, status, daily_window_start, weekly_window_start, monthly_window_start, daily_usage_usd, weekly_usage_usd, monthly_usage_usd, created_at, deleted_at)
    ON TABLE public.user_subscriptions TO xy2api_mcp_reader;

GRANT SELECT (id, user_id, platform, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, daily_usage_usd, weekly_usage_usd, monthly_usage_usd, daily_window_start, weekly_window_start, monthly_window_start, created_at, updated_at, deleted_at)
    ON TABLE public.user_platform_quotas TO xy2api_mcp_reader;

GRANT SELECT (id, created_at, level, component, request_id, client_request_id)
    ON TABLE public.ops_system_logs TO xy2api_mcp_reader;

GRANT SELECT (id, created_at, rule_id, severity, status, fired_at, resolved_at, metric_value, threshold_value, dimensions)
    ON TABLE public.ops_alert_events TO xy2api_mcp_reader;

GRANT SELECT (id, metric_type)
    ON TABLE public.ops_alert_rules TO xy2api_mcp_reader;

GRANT SELECT (id, created_at, actor_user_id, action, status_code, latency_ms, request_id)
    ON TABLE public.audit_logs TO xy2api_mcp_reader;

GRANT SELECT (id, created_at, request_id, user_id, api_key_id, group_id, provider, model, stage, decision, risk_level, action, latency_ms, policy_version, config_version)
    ON TABLE public.prompt_audit_events TO xy2api_mcp_reader;

GRANT SELECT (id, bucket_start, reject_reason, route_family, protocol, request_count)
    ON TABLE public.ops_ingress_reject_aggregates TO xy2api_mcp_reader;

GRANT SELECT (bucket_start, platform, group_id, model, success_requests, error_requests, upstream_affected_requests, upstream_attempt_count, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, ttft_count, ttft_sum_ms, duration_count, duration_sum_ms, computed_at)
    ON TABLE public.channel_monitor_v2_metrics_1m TO xy2api_mcp_reader;

GRANT SELECT (bucket_start, platform, group_id, model, error_category, taxonomy_version, error_requests)
    ON TABLE public.channel_monitor_v2_error_metrics_1m TO xy2api_mcp_reader;

GRANT SELECT (id, enabled)
    ON TABLE public.channel_monitor_v2_config TO xy2api_mcp_reader;

GRANT SELECT (id, data_through, usage_coverage_start, error_coverage_start)
    ON TABLE public.channel_monitor_v2_watermarks TO xy2api_mcp_reader;

GRANT SELECT (bucket_date, group_id, actual_cost, computed_at)
    ON TABLE public.usage_group_daily_rollups TO xy2api_mcp_reader;

GRANT SELECT (id, closed_before, retained_from, timezone_name)
    ON TABLE public.usage_group_rollup_state TO xy2api_mcp_reader;
