package migrations

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestSchedLineStatesMigration(t *testing.T) {
	data, err := FS.ReadFile("315_sched_line_states.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(data)), " ")
	require.NotContains(t, strings.ToUpper(sql), "DROP TABLE")
	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "LOCK TABLE sched_decisions IN ACCESS EXCLUSIVE MODE;")
	require.NotContains(t, sql, "NOWAIT")
	require.Less(t, strings.Index(sql, "CREATE TABLE IF NOT EXISTS sched_line_states"), strings.Index(sql, "LOCK TABLE sched_decisions"))
	require.Less(t, strings.Index(sql, "LOCK TABLE sched_decisions"), strings.Index(sql, "ALTER TABLE sched_decisions"))
	require.Contains(t, sql, "PRIMARY KEY (account_id,model,group_id,line)")
	require.Contains(t, sql, "identity IN (0,1,2)")
	for _, field := range []string{"pending_at", "epoch", "entry", "until", "trial_until", "good_since", "last_rear", "evaluated_at"} {
		require.Contains(t, sql, field+" TIMESTAMPTZ")
	}
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS line TEXT NOT NULL DEFAULT ''")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS sched_decisions_action_check")
	for _, action := range []string{"enter", "renew", "release", "reset", "trial", "trial_pass", "trial_fail", "trial_timeout", "relax", "clear"} {
		require.Contains(t, sql, "'"+action+"'")
	}
}
