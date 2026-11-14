-- Enable the existing scheduled probe for every live API-key account once.
UPDATE accounts
SET extra = jsonb_set(
    CASE WHEN jsonb_typeof(extra) = 'object' THEN extra ELSE '{}'::jsonb END,
    '{upstream_billing_probe_enabled}', 'true'::jsonb, true
)
WHERE type = 'apikey'
  AND deleted_at IS NULL
  AND extra->'upstream_billing_probe_enabled' IS DISTINCT FROM 'true'::jsonb;
