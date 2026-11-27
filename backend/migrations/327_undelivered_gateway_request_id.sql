-- Retain the gateway identity even when the client leaves during upload.
-- Historical client correlation IDs cannot identify a gateway request.
ALTER TABLE gateway_undelivered_requests
    ADD COLUMN IF NOT EXISTS gateway_request_id VARCHAR(128);
