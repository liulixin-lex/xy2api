package dto

import (
	"encoding/json"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A usage row an availability fallback group served: users see the group
// the request was made for and nothing of the fallback; admins keep the
// serving group and the origin (fallback-billing design §2.5).
func fallbackServedUsageLog() *service.UsageLog {
	served, origin := int64(20), int64(10)
	return &service.UsageLog{
		ID: 1, UserID: 5, APIKeyID: 77, AccountID: 3,
		GroupID: &served, Group: &service.Group{ID: served, Name: "GPT-企业专线"},
		OriginGroupID: &origin, OriginGroupName: "GPT-不降智", OriginGroup: &service.Group{ID: origin, Name: "GPT-不降智"},
	}
}

func TestUserUsageLogShowsTheCallerGroup(t *testing.T) {
	out := UsageLogFromService(fallbackServedUsageLog())
	require.Equal(t, int64(10), *out.GroupID)
	require.NotNil(t, out.Group)
	require.Equal(t, "GPT-不降智", out.Group.Name)

	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "origin_group")
	require.NotContains(t, string(raw), "GPT-企业专线")
}

func TestAdminUsageLogKeepsTheFallback(t *testing.T) {
	out := UsageLogFromServiceAdmin(fallbackServedUsageLog())
	require.Equal(t, int64(20), *out.GroupID)
	require.Equal(t, "GPT-企业专线", out.Group.Name)
	require.Equal(t, int64(10), *out.OriginGroupID)
	require.Equal(t, "GPT-不降智", out.OriginGroupName)
}

func TestUserUsageLogWithoutFallbackIsUnchanged(t *testing.T) {
	log := fallbackServedUsageLog()
	log.OriginGroupID, log.OriginGroupName, log.OriginGroup = nil, "", nil
	out := UsageLogFromService(log)
	require.Equal(t, int64(20), *out.GroupID)
	require.Equal(t, "GPT-企业专线", out.Group.Name)
}
