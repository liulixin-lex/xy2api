package migrate

import (
	"strings"
	"testing"

	"entgo.io/ent/schema/field"
	"github.com/liulixin-lex/xy2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestCollabObservationNullableAdditiveSchema(t *testing.T) {
	raw, err := migrations.FS.ReadFile("324_collab_observation.sql")
	require.NoError(t, err)
	sql := string(raw)
	require.Contains(t, sql, "IN ACCESS EXCLUSIVE MODE NOWAIT")
	require.Contains(t, sql, "SET LOCAL lock_timeout = '1s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '5s';")
	for _, table := range []string{"usage_logs", "gateway_unbilled_requests"} {
		require.Contains(t, sql, "ALTER TABLE "+table+" ADD COLUMN IF NOT EXISTS collab_observation JSONB;")
	}
	for _, unsafe := range []string{" DEFAULT ", " NOT NULL", "CREATE INDEX", "UPDATE "} {
		require.NotContains(t, sql, unsafe)
	}
	found := false
	for _, column := range UsageLogsColumns {
		if column.Name == "collab_observation" {
			found = true
			require.True(t, column.Nullable)
			require.Equal(t, field.TypeJSON, column.Type)
			require.Nil(t, column.Default)
			require.Equal(t, "jsonb", column.SchemaType["postgres"])
		}
	}
	require.True(t, found)
	require.Equal(t, 2, strings.Count(sql, "ADD COLUMN"))
}
