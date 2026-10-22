package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 分类存在独立的新表里：不改 groups 表，不加触发器，删除分类或分组都由外键级联清理成员行。
func TestGroupCategoriesMigrationAddsIsolatedTables(t *testing.T) {
	content, err := FS.ReadFile("278_group_categories.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_categories (")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_category_members (")
	require.Contains(t, sql, "REFERENCES group_categories(id) ON DELETE CASCADE")
	require.Contains(t, sql, "REFERENCES groups(id) ON DELETE CASCADE")
	require.Contains(t, sql, "PRIMARY KEY (category_id, group_id)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS group_category_members_group_idx ON group_category_members (group_id)")

	upper := strings.ToUpper(sql)
	require.NotContains(t, upper, "ALTER TABLE GROUPS")
	require.NotContains(t, upper, "TRIGGER")
	require.NotContains(t, upper, "INSERT INTO", "no seeded categories")
}
