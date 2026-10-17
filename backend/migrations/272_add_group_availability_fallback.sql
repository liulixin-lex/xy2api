-- xy2api gateway resilience (design §5): a group may name an availability
-- fallback group that serves requests when the group has no available
-- account. Independent of fallback_group_id (Claude-Code-only downgrade).
-- Nullable, no default: older releases ignore the column.

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS availability_fallback_group_id BIGINT NULL
        REFERENCES groups(id) ON DELETE SET NULL;
