-- Non-unique because a retried asynchronous write may already be stored.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gateway_undelivered_gateway_request_id
    ON gateway_undelivered_requests (gateway_request_id)
    WHERE gateway_request_id IS NOT NULL;
