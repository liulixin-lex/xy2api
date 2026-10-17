//go:build unit

package repository

import (
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollectUsageLogIDsIncludesOriginGroups(t *testing.T) {
	served, origin := int64(8), int64(3)
	ids := collectUsageLogIDs([]service.UsageLog{
		{UserID: 1, APIKeyID: 2, AccountID: 4, GroupID: &served, OriginGroupID: &origin},
		{UserID: 1, APIKeyID: 2, AccountID: 4, GroupID: &origin},
	})
	require.ElementsMatch(t, []int64{3, 8}, ids.groupIDs)
}
