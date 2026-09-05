package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration235AddsModelScopedLongContextPricing(t *testing.T) {
	content, err := FS.ReadFile("235_group_long_context_pricing_models.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))
	require.Contains(t, sql, "long_context_pricing_scope")
	require.Contains(t, sql, "default 'all'")
	require.Contains(t, sql, "long_context_pricing_models")
	require.Contains(t, sql, "default '[]'::jsonb")
	require.Contains(t, sql, "old.long_context_pricing_models is not distinct from new.long_context_pricing_models")
}
