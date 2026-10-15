-- xy2api gateway resilience (design §5): usage served by an availability
-- fallback group records the group the request was made for. Metadata-only
-- change on the large usage_logs table: nullable, no default, no foreign key.

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS origin_group_id BIGINT NULL;
