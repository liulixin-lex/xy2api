-- Diagnostics live and expire with the account's two retained check records.
ALTER TABLE account_iq_check_results
 ADD COLUMN diagnostic JSONB,
 ADD CONSTRAINT account_iq_check_diagnostic_bound CHECK (diagnostic IS NULL OR (jsonb_typeof(diagnostic) = 'object' AND octet_length(diagnostic::text) <= 4096));
