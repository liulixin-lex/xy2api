-- Store one unversioned compatibility profile without changing account identity.
UPDATE accounts
SET extra = jsonb_set(extra, '{anthropic_compat_profile}', '"claude_code"'::jsonb)
WHERE platform = 'anthropic'
  AND type = 'apikey'
  AND extra ->> 'anthropic_compat_profile' IN ('claude_code_v1', 'claude_code_v2');
