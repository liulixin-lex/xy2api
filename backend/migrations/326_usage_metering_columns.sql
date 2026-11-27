-- Keep settlement amounts unchanged; estimate procurement from unadjusted usage.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS metering_adjustment JSONB,
    ADD COLUMN IF NOT EXISTS supplier_cost_estimate NUMERIC(20,10);
COMMENT ON COLUMN usage_logs.supplier_cost_estimate IS
    'Final observed attempt procurement estimate, account multiplier included; NULL unknown, zero confirmed free';
COMMENT ON COLUMN usage_logs.metering_adjustment IS
    'Original metering, settlement adjustments and procurement source; historical rows are not backfilled';
