-- Fixed-interval IQ monitoring: retired policies must not survive in stored state.
-- Stop old workers before upgrading; a binary-only rollback cannot restore this table.
ALTER TABLE accounts ALTER COLUMN iq_check SET DEFAULT '{"enabled":false,"interval_minutes":5,"timeout_seconds":120,"status":"unknown"}'::jsonb;
CREATE OR REPLACE FUNCTION xy_iq_apply_settings(original JSONB, patch JSONB, identity_changed BOOLEAN, at_time TIMESTAMPTZ)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
 prior JSONB := '{"enabled":false,"interval_minutes":5,"model":"gpt-6-astra","reasoning_effort":"low","output_mode":"compat","status":"unknown","timeout_seconds":120}'::jsonb || COALESCE(original,'{}'::jsonb);
 result JSONB;
 reset_result BOOLEAN;
 next_time TIMESTAMPTZ;
BEGIN
 result := (prior || COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(COALESCE(patch,'{}'::jsonb)) WHERE key IN ('enabled','interval_minutes','timeout_seconds','model','reasoning_effort','output_mode')),'{}'::jsonb)) - ARRAY['scheduling_mode','max_interval_minutes','daily_request_limit','quota_group','budget_day','budget_used','budget_remaining','budget_warning','smart_streak']::text[];
 reset_result := identity_changed OR (prior->'enabled' IS DISTINCT FROM result->'enabled')
  OR (prior->'model' IS DISTINCT FROM result->'model')
  OR (prior->'reasoning_effort' IS DISTINCT FROM result->'reasoning_effort')
  OR (prior->'output_mode' IS DISTINCT FROM result->'output_mode')
  OR (prior->'timeout_seconds' IS DISTINCT FROM result->'timeout_seconds');
 IF reset_result THEN
  result := result || jsonb_build_object('status','unknown','reason',CASE WHEN result->>'enabled'='true' THEN 'configuration_changed' ELSE '' END,
   'last_valid_at',NULL,'last_run_status','','last_run_reason','','round_id','','round_deadline',NULL,'retry_at',NULL,'attempt_count',0,'busy_deferrals',0,'failure_streak',0,'protocol_failures',0,'execution_state','idle','execution_reason','','task_id','',
   'revision',md5(random()::text || clock_timestamp()::text),'next_run_at',NULL);
 END IF;
 IF result->>'enabled'='true' AND (reset_result OR (COALESCE(result->>'execution_state','') <> 'paused' AND
  (prior->'interval_minutes' IS DISTINCT FROM result->'interval_minutes') AND COALESCE((result->>'lease_until')::timestamptz,'-infinity')<=at_time)) THEN
  next_time:=GREATEST(at_time,COALESCE((result->>'last_run_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'last_attempt_at')::timestamptz+make_interval(mins=>(result->>'interval_minutes')::int),at_time),COALESCE((result->>'not_before')::timestamptz,at_time));
  result:=result||jsonb_build_object('next_run_at',next_time,'execution_state',CASE WHEN next_time>at_time THEN 'deferred' ELSE 'pending' END,
   'execution_reason',CASE WHEN next_time>at_time THEN 'minimum_interval' ELSE '' END,'task_id',md5(random()::text||clock_timestamp()::text));
 END IF;
 IF NOT reset_result AND prior->>'retry_at' IS NOT NULL THEN result:=result||jsonb_build_object('task_id',prior->'task_id','next_run_at',prior->'next_run_at','execution_state',prior->'execution_state','execution_reason',prior->'execution_reason'); END IF;
 RETURN result;
END $$;


-- Requeue only retired-policy waits and generic HTTP 403 pauses, preserving assessments.
-- Keep not_before because old shared and account-specific cooldowns are indistinguishable.
WITH candidates AS (
 SELECT id, GREATEST(clock_timestamp(),
  COALESCE((iq_check->>'last_run_at')::timestamptz + make_interval(mins=>COALESCE(NULLIF((iq_check->>'interval_minutes')::int,0),5)),clock_timestamp()),
  COALESCE((iq_check->>'last_attempt_at')::timestamptz + make_interval(mins=>COALESCE(NULLIF((iq_check->>'interval_minutes')::int,0),5)),clock_timestamp()),
  COALESCE((iq_check->>'not_before')::timestamptz,clock_timestamp()),
  COALESCE(overload_until,clock_timestamp()),COALESCE(rate_limit_reset_at,clock_timestamp()),COALESCE(temp_unschedulable_until,clock_timestamp())) AS next_time
 FROM accounts
 WHERE deleted_at IS NULL AND platform='openai' AND iq_check->>'enabled'='true'
 AND COALESCE((iq_check->>'lease_until')::timestamptz,'-infinity')<=clock_timestamp()
 AND COALESCE(iq_check->>'retry_at','')='' AND COALESCE(iq_check->>'started_at','')=''
 AND ((iq_check->>'execution_state'='paused' AND iq_check->>'execution_reason'='http_403')
  OR (iq_check->>'execution_state' IN ('deferred','pending','paused') AND iq_check->>'execution_reason' IN
   ('daily_budget','group_daily_budget','group_cooldown','quota_group_unconfigured','group_paused'))
  OR (iq_check->>'scheduling_mode'='adaptive' AND iq_check->>'execution_state'='idle'
   AND COALESCE((iq_check->>'failure_streak')::int,0)=0 AND COALESCE((iq_check->>'protocol_failures')::int,0)=0))
)
UPDATE accounts a SET iq_check=a.iq_check || jsonb_build_object(
 'next_run_at',c.next_time,'execution_state','pending','execution_reason','','failure_streak',0,
 'task_id',md5(random()::text||clock_timestamp()::text),'lease_token','','lease_until',NULL)
FROM candidates c WHERE a.id=c.id;

UPDATE accounts SET iq_check=iq_check - ARRAY['scheduling_mode','max_interval_minutes','daily_request_limit','quota_group','budget_day','budget_used','budget_remaining','budget_warning','smart_streak']::text[];

DROP TABLE iq_check_quota_groups;
