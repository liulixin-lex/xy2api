-- xy2api group categories: admin-only labels for organising groups. Routing,
-- scheduling, billing, user-facing responses and the API-key auth snapshot never
-- read these tables; the groups table is not altered. Older binaries ignore them.
--
-- A group can be in several categories or none. Deleting a category removes it
-- from every group through the cascade. Soft-deleted groups keep their member
-- rows; every read joins groups.deleted_at IS NULL.
--
-- Manual rollback (never run automatically):
--   DROP TABLE group_category_members, group_categories;
CREATE TABLE IF NOT EXISTS group_categories (
    id         BIGSERIAL PRIMARY KEY,
    name       VARCHAR(64) NOT NULL CHECK (btrim(name) <> ''),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS group_category_members (
    category_id BIGINT NOT NULL REFERENCES group_categories(id) ON DELETE CASCADE,
    group_id    BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (category_id, group_id)
);

CREATE INDEX IF NOT EXISTS group_category_members_group_idx
    ON group_category_members (group_id);
