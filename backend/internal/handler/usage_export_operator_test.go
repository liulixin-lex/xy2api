//go:build unit

package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

type operatorExportPermissions struct {
	raw   []byte
	reads int
}

func (r *operatorExportPermissions) Get(context.Context, int64) ([]byte, bool, error) {
	r.reads++
	return r.raw, true, nil
}
func (r *operatorExportPermissions) Upsert(context.Context, int64, []byte, int64) error { return nil }
func TestAdminExportDownloadChecksOperatorFlagAtConsumption(t *testing.T) {
	u := &service.User{ID: 1, Role: service.RoleOperator, Status: service.StatusActive, TokenVersion: 3}
	users := &operatorHandlerUsers{user: u}
	repo := &operatorExportPermissions{raw: []byte(`{"modules":{"usage":"view"},"flags":["data.export"]}`)}
	access := service.NewOperatorAccessService(repo, users, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &UsageExportHandler{users: users, access: access, sessions: exportTestSessions{data: &service.RefreshTokenData{UserID: 1, FamilyID: "family", TokenVersion: 3, ExpiresAt: time.Now().Add(time.Hour)}}}
	identity := usageexport.TicketIdentity{Owner: 1, Session: "family", TokenVersion: 3, TokenExpires: time.Now().Add(time.Hour).Unix()}
	check := func(want bool) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/download", nil)
		require.Equal(t, want, h.authorizeDownload(c, &usageexport.Task{Scope: "admin"}, identity))
	}
	check(true)
	repo.raw = []byte(`{"modules":{"usage":"view"},"flags":[]}`)
	check(false)
	repo.raw = []byte(`{"modules":{},"flags":["data.export"]}`)
	check(false)
	require.Equal(t, 3, repo.reads)
	u.Role = service.RoleAdmin
	check(true)
	u.Role = service.RoleUser
	check(false)
}
func TestUserExportDownloadAllowsOperatorInBackendMode(t *testing.T) {
	settings := service.NewSettingService(&oauthCaptchaSettingRepo{values: map[string]string{service.SettingKeyBackendModeEnabled: "true"}}, &config.Config{})
	require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{BackendModeEnabled: true}))
	t.Cleanup(func() {
		require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{BackendModeEnabled: false}))
	})
	for _, role := range []string{service.RoleOperator, service.RoleAdmin, service.RoleUser} {
		u := &service.User{ID: 1, Role: role, Status: service.StatusActive, TokenVersion: 3}
		h := &UsageExportHandler{users: exportTestUsers{u}, settings: settings, sessions: exportTestSessions{data: &service.RefreshTokenData{UserID: 1, FamilyID: "family", TokenVersion: 3, ExpiresAt: time.Now().Add(time.Hour)}}}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/download", nil)
		require.Equal(t, role != service.RoleUser, h.authorizeDownload(c, &usageexport.Task{Scope: "user"}, usageexport.TicketIdentity{Owner: 1, Session: "family", TokenVersion: 3, TokenExpires: time.Now().Add(time.Hour).Unix()}))
	}
}
