package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupModelAllowlistAuthCacheMigration(t *testing.T) {
	content, err := FS.ReadFile("238_group_model_allowlist_auth_cache_invalidation.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(strings.ToLower(string(content))), " ")
	require.Contains(t, sql, "create or replace function enqueue_group_auth_cache_invalidation()")
	require.Contains(t, sql, "old.model_allowlist is not distinct from new.model_allowlist")
	require.Contains(t, sql, "insert into auth_cache_invalidation_outbox")
}
