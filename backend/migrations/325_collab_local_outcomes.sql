SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '5s';

-- Widening cannot invalidate existing rows; do not scan the billing table.
LOCK TABLE gateway_unbilled_requests IN ACCESS EXCLUSIVE MODE NOWAIT;

ALTER TABLE gateway_unbilled_requests
    DROP CONSTRAINT IF EXISTS gateway_unbilled_requests_result_check;

ALTER TABLE gateway_unbilled_requests
    ADD CONSTRAINT gateway_unbilled_requests_result_check
    CHECK (result BETWEEN 1 AND 6) NOT VALID;
