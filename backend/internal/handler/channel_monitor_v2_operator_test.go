//go:build unit

package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestChannelMonitorV2AdminViewNeedsChannelsView(t *testing.T) {
	for _, tc := range []struct {
		role  string
		scope bool
		level operatoraccess.Level
		want  bool
	}{{"admin", false, 0, true}, {"operator", false, 0, false}, {"operator", true, operatoraccess.LevelView, true}, {"operator", true, 0, false}, {"user", false, 0, false}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Set(string(middleware.ContextKeyUserRole), tc.role)
		if tc.scope {
			c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), &operatoraccess.Scope{Perms: operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleChannels: tc.level}}}))
		}
		require.Equal(t, tc.want, channelMonitorV2IsAdmin(c))
	}
}
