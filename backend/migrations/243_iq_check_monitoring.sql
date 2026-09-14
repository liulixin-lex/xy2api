-- Aggregate counters only: no account credentials, prompts, or response history.
CREATE TABLE iq_check_quota_groups (
 id varchar(64) PRIMARY KEY,
 budget_day varchar(10) NOT NULL,
 used integer NOT NULL DEFAULT 0 CHECK (used >= 0),
 paused_reason varchar(64) NOT NULL DEFAULT '',
 not_before timestamptz,
 next_start_at timestamptz
);

-- Recognize an already-started attempt created by the previous worker version.
UPDATE accounts a SET iq_check=a.iq_check || jsonb_build_object('started_at',r.started_at,'last_attempt_at',r.started_at,'execution_state','running')
FROM account_iq_check_results r
WHERE r.account_id=a.id AND r.lease_token=a.iq_check->>'lease_token' AND r.finished_at IS NULL;

-- Additive replacement of the settings function keeps bulk and individual semantics aligned.
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
   'smart_streak',0,'failure_streak',0,'protocol_failures',0,'execution_state','idle','execution_reason','','task_id','',
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
 RETURN result;
END $$;
