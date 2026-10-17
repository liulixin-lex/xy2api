package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 277 removes the two balance alert rules 276 seeded: upstream balance
// handling no longer keeps the balance states they watched (0.4.0,
// 2026-10-17). 276 already ran on the RC and staging databases and stays
// as it is.
func TestBalanceAlertRulesRemovalDeletesOnlyTheSeededRules(t *testing.T) {
	content, err := FS.ReadFile("277_remove_balance_alert_rules.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	upper := strings.ToUpper(sql)

	require.Contains(t, sql, "DELETE FROM ops_alert_rules WHERE (name, metric_type) IN")
	require.Contains(t, sql, "('上游余额不足', 'account_balance_exhausted_count')")
	require.Contains(t, sql, "('上游余额长期未恢复', 'account_balance_unrecovered_count')")
	require.Equal(t, 1, strings.Count(upper, "DELETE "), "a single statement")
	for _, other := range []string{"UPDATE ", "INSERT ", "DROP ", "ALTER ", "TRUNCATE "} {
		require.NotContains(t, upper, other)
	}
}
