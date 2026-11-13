CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attempt_samples_request_id ON account_attempt_samples (request_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attempt_samples_session_previous ON account_attempt_samples (group_id, session_key, model, started_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attempt_samples_finished ON account_attempt_samples (finished_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_gateway_request_created ON usage_logs (gateway_request_id, created_at) WHERE gateway_request_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_group_created_at_not_null ON usage_logs (group_id, created_at) WHERE group_id IS NOT NULL;
