package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatProfileMigration(t *testing.T) {
	content, err := FS.ReadFile("298_anthropic_compat_profile_final.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "SET extra = jsonb_set(extra, '{anthropic_compat_profile}', '\"claude_code\"'::jsonb)")
	require.Contains(t, sql, "WHERE platform = 'anthropic' AND type = 'apikey'")
	require.Contains(t, sql, "AND extra ->> 'anthropic_compat_profile' IN ('claude_code_v1', 'claude_code_v2')")
	require.NotContains(t, sql, "UPDATE accounts SET credentials")
}
