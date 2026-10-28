-- xy2api operator role: per-user admin console permissions. A row is read only
-- while its user has role 'operator'; rows of other roles are ignored. The
-- users table is not altered: 'operator' is a new value of the existing
-- VARCHAR(20) role column, which has no CHECK constraint. Older binaries
-- ignore this table.
--
-- Users are soft-deleted, so ON DELETE CASCADE fires only on a hard delete. A
-- soft-deleted operator cannot sign in, so a leftover row is inert.
--
-- Manual rollback (never run automatically). Run it as one transaction, before
-- starting the older binary. Operators are demoted first because older
-- binaries do not know the role. The schema_migrations row is deleted so that
-- a later upgrade runs this file again and recreates the table:
--   BEGIN;
--   UPDATE users SET role = 'user' WHERE role = 'operator';
--   DROP TABLE IF EXISTS admin_operator_permissions;
--   DELETE FROM schema_migrations WHERE filename = '282_admin_operator_permissions.sql';
--   COMMIT;
CREATE TABLE IF NOT EXISTS admin_operator_permissions (
    user_id     BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    permissions JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_by  BIGINT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
