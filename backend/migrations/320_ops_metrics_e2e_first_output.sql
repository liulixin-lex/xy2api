-- End-to-end first output is measured independently of the final attempt.
-- Historical buckets retain NULL percentiles and a zero sample count.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_metrics_hourly
    ADD COLUMN IF NOT EXISTS e2e_first_output_p50_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p90_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p95_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p99_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_avg_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS e2e_first_output_max_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_sample_count BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ops_metrics_daily
    ADD COLUMN IF NOT EXISTS e2e_first_output_p50_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p90_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p95_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_p99_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_avg_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS e2e_first_output_max_ms INT,
    ADD COLUMN IF NOT EXISTS e2e_first_output_sample_count BIGINT NOT NULL DEFAULT 0;
