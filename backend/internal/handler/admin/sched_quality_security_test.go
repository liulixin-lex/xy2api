//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func schedAdminSecurityOperator(perms operatoraccess.Permissions) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_role", service.RoleOperator)
		c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), &operatoraccess.Scope{UserID: 7, Perms: perms}))
		c.Next()
	}
}

func TestSchedQualityOverrideReasonAddressPermissions(t *testing.T) {
	entry, found := operatoraccess.Lookup(operatoraccess.Key("GET", "/api/v1/admin/sched-quality/groups/:id/board"))
	require.True(t, found)
	require.Equal(t, operatoraccess.ModuleGroups, entry.Module)
	require.Equal(t, operatoraccess.LevelView, entry.Level)
	require.Empty(t, entry.Redact)
	for _, tc := range []struct {
		name  string
		perms operatoraccess.Permissions
		paths []string
		want  string
	}{
		{"groups_only", operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleGroups: operatoraccess.LevelView}}, []string{"/groups/1/board?model=m"}, "maintenance [upstream] and [upstream] done"},
		{"without_address", operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView, operatoraccess.ModuleGroups: operatoraccess.LevelView}}, []string{"/groups/1/board?model=m", "/accounts/1"}, "maintenance [upstream] and [upstream] done"},
		{"with_address", operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView, operatoraccess.ModuleGroups: operatoraccess.LevelView}, Flags: map[operatoraccess.Flag]bool{operatoraccess.FlagAccountsUpstreamAddress: true}}, []string{"/groups/1/board?model=m", "/accounts/1"}, "maintenance https://***@relay.example/private and socks5://***@other.example:1080/path done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, engine, _ := schedAdminHandlerFixture()
			reason := "maintenance https://proxy-user:proxy-secret@relay.example/private and socks5://second-user:second-secret@other.example:1080/path done"
			engine.p.GroupRows[1] = []service.SchedGroupRow{{GroupID: 1, Model: "m"}}
			engine.p.Overrides = []service.SchedOverride{{AccountID: 1, GroupID: 1, Mode: schedquality.OverrideDemote, Reason: reason}}
			r := schedAdminHandlerRouter(h, schedAdminSecurityOperator(tc.perms), middleware.AdminPermission())
			for _, path := range tc.paths {
				w := schedAdminHandlerRequest(r, "GET", path, "")
				require.Equal(t, 200, w.Code, w.Body.String())
				var result struct {
					Data struct {
						Accounts []struct{ Override *service.SchedAdminOverride }
						Groups   []struct{ Override *service.SchedAdminOverride }
					}
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				if len(result.Data.Accounts) > 0 {
					require.NotNil(t, result.Data.Accounts[0].Override)
					require.Equal(t, tc.want, result.Data.Accounts[0].Override.Reason)
				} else {
					require.Len(t, result.Data.Groups, 1)
					require.NotNil(t, result.Data.Groups[0].Override)
					require.Equal(t, tc.want, result.Data.Groups[0].Override.Reason)
				}
			}
			require.Equal(t, reason, engine.p.Overrides[0].Reason)
		})
	}
}

func TestSchedQualityOverrideAuditRepositoryReceivesNoURLCredentials(t *testing.T) {
	h, engine, _ := schedAdminHandlerFixture()
	repo := &schedAdminHandlerAudit{}
	audit := service.NewAuditLogService(repo, nil)
	audit.Start()
	t.Cleanup(audit.Stop)
	perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelManage}}
	r := schedAdminHandlerRouter(h, schedAdminSecurityOperator(perms), gin.HandlerFunc(middleware.NewAuditLogMiddleware(audit)), middleware.AdminPermission())
	w := schedAdminHandlerRequest(r, "PUT", "/accounts/1/override", `{"group_id":1,"mode":"demote","expires_at":null,"reason":"maintenance https://proxy-user:proxy-secret@relay.example/private and socks5://second-user:second-secret@other.example:1080/path done"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	audit.Stop()
	require.Len(t, repo.rows, 1)
	require.Equal(t, "sched_quality.override", repo.rows[0].Action)
	for _, credential := range []string{"proxy-user", "proxy-secret", "second-user", "second-secret"} {
		require.NotContains(t, repo.rows[0].RequestBody, credential)
	}
	var body struct{ Reason string }
	require.NoError(t, json.Unmarshal([]byte(repo.rows[0].RequestBody), &body))
	require.Equal(t, "maintenance https://***@relay.example/private and socks5://***@other.example:1080/path done", body.Reason)
	require.Equal(t, body.Reason, engine.override.Reason)
}
