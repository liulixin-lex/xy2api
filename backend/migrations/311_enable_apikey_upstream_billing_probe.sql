-- Enable the existing scheduled probe for every live API-key account once.
UPDATE accounts
SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{upstream_billing_probe_enabled}', 'true'::jsonb, true)
WHERE type = 'apikey' AND deleted_at IS NULL;
