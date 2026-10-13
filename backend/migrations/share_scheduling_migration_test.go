package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShareSchedulingMigrationKeepsRoutingOnSub2API(t *testing.T) {
	content, err := FS.ReadFile("270_share_scheduling.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS share_scheduling_policies")
	require.Contains(t, sql, "CHECK (scheduler IN ('sub2api', 'shadow', 'share'))")
	require.Contains(t, sql, "IF to_regclass('scheduling_group_policies') IS NULL THEN RETURN;")
	require.Contains(t, sql, "'scheduler', 'sub2api'", "imported drafts stay on the original scheduler")
	require.Contains(t, sql, "ON CONFLICT (group_id) DO NOTHING")
	require.NotContains(t, sql, "DROP TABLE", "retired scheduling tables stay one release for rollback")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE SCHEDULING_", "retired scheduling data is read-only")
}
