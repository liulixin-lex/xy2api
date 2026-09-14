-- Preserve migration 240 and all historical results. New workers write explicit snapshots.
ALTER TABLE account_iq_check_results
 ADD COLUMN normalized_answer TEXT,
 ADD COLUMN answer_format TEXT NOT NULL DEFAULT '',
 ADD COLUMN output_mode TEXT NOT NULL DEFAULT 'compat',
 ADD COLUMN format_compliant BOOLEAN,
 ADD COLUMN grader_version TEXT NOT NULL DEFAULT 'candy-grader-v1',
 ADD COLUMN protocol TEXT NOT NULL DEFAULT '',
 ADD COLUMN reported_model TEXT,
 ADD COLUMN config_revision TEXT NOT NULL DEFAULT '';

-- Shared by individual, bulk and credential edits; omitted fields preserve configuration.
CREATE FUNCTION xy_iq_apply_settings(original JSONB, patch JSONB, identity_changed BOOLEAN, at_time TIMESTAMPTZ)
RETURNS JSONB LANGUAGE plpgsql AS $$
DECLARE
 prior JSONB := '{"enabled":false,"interval_minutes":15,"model":"gpt-6-astra","reasoning_effort":"low","output_mode":"compat","status":"unknown"}'::jsonb || COALESCE(original,'{}'::jsonb);
 result JSONB;
 reset_result BOOLEAN;
BEGIN
 result := prior || COALESCE(patch,'{}'::jsonb);
 reset_result := identity_changed OR (prior->'enabled' IS DISTINCT FROM result->'enabled')
  OR (prior->'model' IS DISTINCT FROM result->'model')
  OR (prior->'reasoning_effort' IS DISTINCT FROM result->'reasoning_effort')
  OR (prior->'output_mode' IS DISTINCT FROM result->'output_mode');
 IF reset_result THEN
  result := result || jsonb_build_object('status','unknown','reason',CASE WHEN result->>'enabled'='true' THEN 'configuration_changed' ELSE '' END,
   'revision',md5(random()::text || clock_timestamp()::text),
   'next_run_at',CASE WHEN result->>'enabled'='true' THEN to_jsonb(at_time) ELSE 'null'::jsonb END);
 ELSIF prior->'interval_minutes' IS DISTINCT FROM result->'interval_minutes'
   AND result->>'enabled'='true'
   AND COALESCE((result->>'lease_until')::timestamptz,'-infinity') <= at_time THEN
  result := result || jsonb_build_object('next_run_at',GREATEST(at_time,
   COALESCE((result->>'last_run_at')::timestamptz + make_interval(mins => (result->>'interval_minutes')::int),at_time)));
 END IF;
 RETURN result;
END $$;
