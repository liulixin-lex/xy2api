-- New accounts only: existing intervals and budgets are deliberately untouched.
ALTER TABLE accounts ALTER COLUMN iq_check SET DEFAULT '{"enabled":false,"interval_minutes":5,"daily_request_limit":576,"timeout_seconds":120,"status":"unknown"}'::jsonb;
CREATE TABLE account_iq_check_attempts (
 result_id BIGINT NOT NULL REFERENCES account_iq_check_results(id) ON DELETE CASCADE,
 attempt_no INTEGER NOT NULL CHECK(attempt_no BETWEEN 1 AND 2),
 lease_token TEXT NOT NULL UNIQUE,
 started_at TIMESTAMPTZ NOT NULL,
 finished_at TIMESTAMPTZ,
 status TEXT NOT NULL DEFAULT 'unknown' CHECK(status IN ('smart','degraded','unknown')),
 reason TEXT NOT NULL DEFAULT '',
 latency_ms BIGINT NOT NULL DEFAULT 0,
 diagnostic JSONB CHECK(diagnostic IS NULL OR octet_length(diagnostic::text)<=4096),
 PRIMARY KEY(result_id,attempt_no)
);
CREATE TABLE account_iq_check_metrics_hourly (
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 bucket TIMESTAMPTZ NOT NULL,
 event TEXT NOT NULL,
 reason TEXT NOT NULL,
 count BIGINT NOT NULL DEFAULT 0,
 latency_ms BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY(account_id,bucket,event,reason)
);
CREATE INDEX account_iq_check_metrics_expiry ON account_iq_check_metrics_hourly(bucket);
-- Recover only trustworthy retained assessments from the same configuration.
UPDATE accounts a SET iq_check=a.iq_check || jsonb_build_object(
 'status',COALESCE((SELECT r.status FROM account_iq_check_results r WHERE r.account_id=a.id AND r.config_revision=a.iq_check->>'revision' AND r.status IN ('smart','degraded') AND r.finished_at IS NOT NULL ORDER BY r.id DESC LIMIT 1),'unknown'),
 'reason',COALESCE((SELECT r.reason FROM account_iq_check_results r WHERE r.account_id=a.id AND r.config_revision=a.iq_check->>'revision' AND r.status IN ('smart','degraded') AND r.finished_at IS NOT NULL ORDER BY r.id DESC LIMIT 1),'no_valid_history'),
 'last_valid_at',(SELECT r.finished_at FROM account_iq_check_results r WHERE r.account_id=a.id AND r.config_revision=a.iq_check->>'revision' AND r.status IN ('smart','degraded') AND r.finished_at IS NOT NULL ORDER BY r.id DESC LIMIT 1),
 'last_run_status',a.iq_check->>'status','last_run_reason',a.iq_check->>'reason')
WHERE a.deleted_at IS NULL;
CREATE OR REPLACE FUNCTION xy_iq_apply_settings(original JSONB, patch JSONB, identity_changed BOOLEAN, at_time TIMESTAMPTZ)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
 prior JSONB := '{"enabled":false,"interval_minutes":15,"model":"gpt-6-astra","reasoning_effort":"low","output_mode":"compat","status":"unknown","scheduling_mode":"fixed","max_interval_minutes":60,"timeout_seconds":120}'::jsonb || COALESCE(original,'{}'::jsonb);
 result JSONB;
 reset_result BOOLEAN;
 next_time TIMESTAMPTZ;
 daily_limit INT;
BEGIN
 result := prior || COALESCE(patch,'{}'::jsonb);
 reset_result := identity_changed OR (prior->'enabled' IS DISTINCT FROM result->'enabled')
  OR (prior->'model' IS DISTINCT FROM result->'model')
  OR (prior->'reasoning_effort' IS DISTINCT FROM result->'reasoning_effort')
  OR (prior->'output_mode' IS DISTINCT FROM result->'output_mode')
  OR (prior->'timeout_seconds' IS DISTINCT FROM result->'timeout_seconds');
 IF reset_result THEN
  result := result || jsonb_build_object('status','unknown','reason',CASE WHEN result->>'enabled'='true' THEN 'configuration_changed' ELSE '' END,
   'last_valid_at',NULL,'last_run_status','','last_run_reason','','round_id','','round_deadline',NULL,'retry_at',NULL,'attempt_count',0,'busy_deferrals',0,'smart_streak',0,'failure_streak',0,'protocol_failures',0,'execution_state','idle','execution_reason','','task_id','',
   'revision',md5(random()::text || clock_timestamp()::text),'next_run_at',NULL);
 END IF;
 IF result->>'enabled'='true' AND (reset_result OR (COALESCE(result->>'execution_state','') <> 'paused' AND
  (prior->'interval_minutes' IS DISTINCT FROM result->'interval_minutes' OR prior->'scheduling_mode' IS DISTINCT FROM result->'scheduling_mode'
   OR prior->'max_interval_minutes' IS DISTINCT FROM result->'max_interval_minutes' OR prior->'daily_request_limit' IS DISTINCT FROM result->'daily_request_limit'
   OR prior->'quota_group' IS DISTINCT FROM result->'quota_group') AND COALESCE((result->>'lease_until')::timestamptz,'-infinity')<=at_time)) THEN
  next_time:=GREATEST(at_time,COALESCE((result->>'last_run_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'last_attempt_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'not_before')::timestamptz,at_time));
  daily_limit:=COALESCE(NULLIF((result->>'daily_request_limit')::int,0),(1440+(result->>'interval_minutes')::int-1)/(result->>'interval_minutes')::int);
  IF result->>'budget_day'=to_char(at_time AT TIME ZONE 'UTC','YYYY-MM-DD') AND COALESCE((result->>'budget_used')::int,0)>=daily_limit THEN
   next_time:=GREATEST(next_time,(date_trunc('day',at_time AT TIME ZONE 'UTC')+interval '1 day') AT TIME ZONE 'UTC');
  END IF;
  result:=result||jsonb_build_object('next_run_at',next_time,'execution_state',CASE WHEN next_time>at_time THEN 'deferred' ELSE 'pending' END,
   'execution_reason',CASE WHEN next_time>at_time THEN 'minimum_interval' ELSE '' END,'task_id',md5(random()::text||clock_timestamp()::text));
 END IF;
 IF NOT reset_result AND prior->>'retry_at' IS NOT NULL THEN result:=result||jsonb_build_object('task_id',prior->'task_id','next_run_at',prior->'next_run_at','execution_state',prior->'execution_state','execution_reason',prior->'execution_reason'); END IF;
 RETURN result;
END $$;

-- Both bulk and single-account configuration updates close abandoned waiting rounds.
CREATE FUNCTION xy_iq_cancel_waiting_round() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.iq_check->>'round_id' IS NOT NULL AND OLD.iq_check->>'round_id' <> ''
    AND OLD.iq_check->>'started_at' IS NULL
    AND OLD.iq_check->>'revision' IS DISTINCT FROM NEW.iq_check->>'revision' THEN
  UPDATE account_iq_check_results SET finished_at=clock_timestamp(), reason='cancelled_by_account_change'
  WHERE lease_token=OLD.iq_check->>'round_id' AND finished_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER iq_cancel_waiting_round AFTER UPDATE OF iq_check ON accounts FOR EACH ROW EXECUTE FUNCTION xy_iq_cancel_waiting_round();
