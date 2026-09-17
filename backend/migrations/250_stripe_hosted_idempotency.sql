ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(128);
CREATE UNIQUE INDEX IF NOT EXISTS paymentorder_user_id_idempotency_key ON payment_orders (user_id, idempotency_key);
