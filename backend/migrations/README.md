# Database Migrations

## Overview

This directory contains SQL migration files for database schema changes. The migration files and `checksums.json` are embedded into the server binary (`migrations.go`). The migration system uses SHA256 checksums to ensure migration immutability and consistency across environments.

Migrations are forward-only: every schema change, including a rollback of an earlier change, is a new migration file.

## Migration File Naming

Format: `NNN_description.sql`
- `NNN`: Sequential number (e.g., 001, 002, 003)
- `description`: Brief description in snake_case

Example: `024_add_gemini_tier_id.sql`

Migrations run in the lexicographic order of their full file names. Some historical numbers are shared by more than one file (for example `006_add_users_allowed_groups_compat.sql` and `006_fix_invalid_subscription_expires_at.sql`). For a new migration, use a number greater than every existing one.

### `_notx.sql` 命名与执行语义（并发索引专用）

当迁移包含 `CREATE INDEX CONCURRENTLY` 或 `DROP INDEX CONCURRENTLY` 时，必须使用 `_notx.sql` 后缀，例如：

- `062_add_scheduler_and_usage_composite_indexes_notx.sql`
- `148_add_ops_error_logs_user_time_index_notx.sql`

运行规则：

1. `*.sql`（不带 `_notx`）按事务执行。文件中不能出现 `CONCURRENTLY` 字样（注释中也不行），否则启动时校验失败。
2. `*_notx.sql` 按非事务执行，不会包裹在 `BEGIN/COMMIT` 中。
3. `*_notx.sql` 仅允许并发索引语句，不允许混入事务控制语句或其他 DDL/DML。文件中不能出现 `BEGIN`、`COMMIT`、`ROLLBACK` 字样（注释中也不行）。

幂等要求（必须）：

- 创建索引：`CREATE INDEX CONCURRENTLY IF NOT EXISTS ...`
- 删除索引：`DROP INDEX CONCURRENTLY IF EXISTS ...`

这样可以保证灾备重放、重复执行时不会因对象已存在/不存在而失败。

## Migration File Structure

This project uses a custom migration runner (`internal/repository/migrations_runner.go`) that executes the full SQL file content as-is.

- Regular migrations (`*.sql`): the whole file is executed in a single transaction.
- Non-transactional migrations (`*_notx.sql`): split on `;` and executed statement by statement without a transaction (for `CONCURRENTLY`).

```sql
-- Forward-only migration
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS example_column VARCHAR(100);
```

> Do **not** place executable "Down" SQL in the same file. The runner does not parse goose Up/Down sections and will execute all SQL statements in the file.

## Important Rules

### Immutability Principle

**Once a migration is applied to ANY environment (dev, staging, production), it MUST NOT be modified.**

Why?
- Each migration has a SHA256 checksum stored in the `schema_migrations` table
- Modifying an applied migration causes checksum mismatch errors, and the server refuses to start
- Different environments would have inconsistent database states
- Breaks audit trail and reproducibility

### `checksums.json` Is Append-Only

`checksums.json` pins the checksum of every published migration. `TestPublishedMigrationChecksumsAreImmutable` (`branding_compatibility_test.go`, no build tag, so it runs under any `go test`) fails when:
- a `*.sql` file has no entry, or the number of entries differs from the number of files;
- a file's checksum differs from its entry.

When you add a migration, append exactly one entry for it to the `migrations` object. Never change or remove existing entries, and do not change `schema_version` or `algorithm`.

The checksum is the SHA-256 of the file content after trimming leading and trailing whitespace (`sha256(strings.TrimSpace(utf8))`), as lowercase hex. It is the same value the runner stores in `schema_migrations`. From the `backend/` directory:

```bash
python3 -c "import hashlib,sys;print(hashlib.sha256(open(sys.argv[1],encoding='utf-8').read().strip().encode()).hexdigest())" migrations/NNN_your_change.sql
```

### Correct Workflow

1. **Create new migration**
   ```bash
   # Create new file with a number greater than every existing one
   touch migrations/NNN_your_change.sql
   ```

2. **Write forward-only migration SQL**
   - Put only the intended schema change in the file
   - If rollback is needed, create a new migration file to revert

3. **Add its checksum to `checksums.json`**
   - Compute it as shown above and append `"NNN_your_change.sql": "<sha256>"`

4. **Test locally**
   ```bash
   # Check the checksum manifest.
   go test ./migrations/
   # Run the migration and repository integration tests (requires Docker).
   make test-integration
   ```

   The embedded runner is forward-only. If a schema rollback is required,
   create a new migration that reverses the previous change; never add Down
   SQL to an already-published file.

5. **Commit and deploy**
   ```bash
   git add migrations/NNN_your_change.sql migrations/checksums.json
   git commit -m "feat(db): add your change"
   ```

### What NOT to Do

- Modify an already-applied migration file
- Delete migration files
- Change migration file names
- Reorder migration numbers
- Change or remove existing entries in `checksums.json`

### If You Accidentally Modified an Applied Migration

**Error message:**
```
migration 024_add_gemini_tier_id.sql checksum mismatch (db=abc123... file=def456...)
```

**Solution:**
```bash
# 1. Find the original version
git log --oneline -- migrations/024_add_gemini_tier_id.sql

# 2. Revert to the commit when it was first applied
git checkout <commit-hash> -- migrations/024_add_gemini_tier_id.sql

# 3. Create a NEW migration for your changes
touch migrations/NNN_your_new_change.sql
```

## Migration System Details

- **Checksum Algorithm**: SHA256 of trimmed file content
- **Tracking Table**: `schema_migrations` (filename, checksum, applied_at)
- **Checksum Manifest**: `checksums.json` (append-only, checked by tests)
- **Runner**: `internal/repository/migrations_runner.go`
- **Auto-run**: Migrations run automatically on service startup, under a PostgreSQL advisory lock so that only one instance applies them at a time

## Best Practices

1. **Keep migrations small and focused**
   - One logical change per migration
   - Easier to review, and easier to reverse with a follow-up migration

2. **Make migrations idempotent**
   - Use `IF NOT EXISTS` / `IF EXISTS` so a replay does not fail

3. **Do not add your own transaction control**
   - Regular migrations already run inside a single transaction; do not add `BEGIN` / `COMMIT`

4. **Add comments**
   - Explain WHY the change is needed
   - Document any special considerations

5. **Test in development first**
   - Apply migration locally
   - Verify data integrity

## Example Migration

```sql
-- Add tier_id field to Gemini OAuth accounts for quota tracking
UPDATE accounts
SET credentials = jsonb_set(
    credentials,
    '{tier_id}',
    '"LEGACY"',
    true
)
WHERE platform = 'gemini'
  AND type = 'oauth'
  AND credentials->>'tier_id' IS NULL;
```

## Troubleshooting

### Checksum Mismatch
See "If You Accidentally Modified an Applied Migration" above.

### Migration Failed
```bash
# Check migration status
psql -d <database> -c "SELECT * FROM schema_migrations ORDER BY applied_at DESC;"
```

A failed regular migration is rolled back as a whole and is not recorded, so the server retries it on the next start. A failed `*_notx.sql` migration may have applied some of its statements before the failure; it is not recorded either, and because its statements must be idempotent the retry is safe. Fix the cause (for example conflicting data), or, if the file has not been published yet, correct it before release. Once a migration is published, put further changes in a new migration.

## References

- Migration runner: `internal/repository/migrations_runner.go`
- PostgreSQL docs: https://www.postgresql.org/docs/
