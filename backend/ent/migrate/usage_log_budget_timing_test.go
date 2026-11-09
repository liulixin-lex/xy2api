package migrate

import (
	"regexp"
	"testing"

	entschema "entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUsageLogBudgetTimingMigrationColumns(t *testing.T) {
	sql, err := migrations.FS.ReadFile("292_usage_logs_budget_timing.sql")
	require.NoError(t, err)
	columns := regexp.MustCompile(`ADD COLUMN IF NOT EXISTS (\w+) (BIGINT|TEXT|BOOLEAN) NULL`).FindAllStringSubmatch(string(sql), -1)
	require.Len(t, columns, 18)
	types := map[string]field.Type{"BIGINT": field.TypeInt64, "TEXT": field.TypeString, "BOOLEAN": field.TypeBool}
	byName := make(map[string]*entschema.Column)
	for _, col := range UsageLogsColumns {
		byName[col.Name] = col
	}
	for _, col := range columns {
		t.Run(col[1], func(t *testing.T) {
			actual := byName[col[1]]
			require.NotNil(t, actual)
			require.Equal(t, types[col[2]], actual.Type)
			require.True(t, actual.Nullable)
			require.Nil(t, actual.Default)
		})
	}
	for _, key := range []struct{ table, column string }{
		{"api_keys", "api_key_id"}, {"accounts", "account_id"}, {"groups", "group_id"},
		{"users", "user_id"}, {"user_subscriptions", "subscription_id"},
	} {
		fk := findForeignKeyBySymbol(t, UsageLogsTable, "usage_logs_"+key.table+"_usage_logs")
		require.Equal(t, []*entschema.Column{byName[key.column]}, fk.Columns)
		idx := findIndexByName(t, UsageLogsTable, "usagelog_"+key.column)
		require.Equal(t, []*entschema.Column{byName[key.column]}, idx.Columns)
	}
	for _, name := range []string{"user_id", "api_key_id", "group_id"} {
		idx := findIndexByName(t, UsageLogsTable, "usagelog_"+name+"_created_at")
		require.Equal(t, []*entschema.Column{byName[name], byName["created_at"]}, idx.Columns)
	}
}
