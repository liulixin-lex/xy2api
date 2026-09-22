package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/usageexport"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

type exportTestUsers struct{ user *service.User }

func (u exportTestUsers) GetByID(context.Context, int64) (*service.User, error) { return u.user, nil }

type exportTestSessions struct {
	service.RefreshTokenCache
	data *service.RefreshTokenData
}

func (s exportTestSessions) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return []string{"hash"}, nil
}
func (s exportTestSessions) GetRefreshToken(context.Context, string) (*service.RefreshTokenData, error) {
	return s.data, nil
}
func TestUsageExportDownloadCurrentPermissions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*service.User, *usageexport.TicketIdentity, *service.RefreshTokenData)
		allowed bool
	}{
		{"valid", func(*service.User, *usageexport.TicketIdentity, *service.RefreshTokenData) {}, true},
		{"disabled", func(u *service.User, _ *usageexport.TicketIdentity, _ *service.RefreshTokenData) {
			u.Status = "disabled"
		}, false},
		{"role_revoked", func(u *service.User, _ *usageexport.TicketIdentity, _ *service.RefreshTokenData) { u.Role = "user" }, false},
		{"password_changed", func(u *service.User, _ *usageexport.TicketIdentity, _ *service.RefreshTokenData) { u.TokenVersion++ }, false},
		{"login_expired", func(_ *service.User, i *usageexport.TicketIdentity, _ *service.RefreshTokenData) { i.TokenExpires = 1 }, false},
		{"session_expired", func(_ *service.User, _ *usageexport.TicketIdentity, s *service.RefreshTokenData) {
			s.ExpiresAt = time.Unix(1, 0)
		}, false},
		{"session_replaced", func(_ *service.User, _ *usageexport.TicketIdentity, s *service.RefreshTokenData) {
			s.FamilyID = "other"
		}, false},
		{"other_owner", func(_ *service.User, _ *usageexport.TicketIdentity, s *service.RefreshTokenData) { s.UserID = 2 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &service.User{ID: 1, Status: service.StatusActive, Role: service.RoleAdmin, TokenVersion: 3}
			i := usageexport.TicketIdentity{Owner: 1, Session: "family", TokenVersion: 3, TokenExpires: time.Now().Add(time.Minute).Unix()}
			s := &service.RefreshTokenData{UserID: 1, FamilyID: "family", TokenVersion: 3, ExpiresAt: time.Now().Add(time.Hour)}
			tc.mutate(u, &i, s)
			h := UsageExportHandler{users: exportTestUsers{u}, sessions: exportTestSessions{data: s}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/download", nil)
			require.Equal(t, tc.allowed, h.authorizeDownload(c, &usageexport.Task{Scope: "admin"}, i))
		})
	}
}
