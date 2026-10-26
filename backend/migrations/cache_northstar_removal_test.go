package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheNorthstarRemovalDropsOnlyTheObservabilityTables(t *testing.T) {
	content, err := FS.ReadFile("280_drop_cache_northstar.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	upper := strings.ToUpper(sql)

	require.Contains(t, sql, "SET LOCAL lock_timeout = '5s';")
	require.Contains(t, sql, "SET LOCAL statement_timeout = '10min';")
	for _, table := range []string{
		"cache_trace_hourly",
		"cache_observability_events",
		"cache_northstar_hours",
		"cache_northstar_rollup_state",
		"cache_northstar_hourly",
	} {
		require.Contains(t, sql, "DROP TABLE IF EXISTS "+table+";")
	}
	require.Equal(t, 5, strings.Count(upper, "DROP "), "exactly five table removals")
	for _, other := range []string{"DELETE ", "UPDATE ", "INSERT ", "ALTER ", "TRUNCATE "} {
		require.NotContains(t, upper, other)
	}
}
