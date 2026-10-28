//go:build unit

package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type operatorHandlerUsers struct {
	service.UserRepository
	user *service.User
}

func (r *operatorHandlerUsers) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}

type operatorRefreshCache struct {
	service.RefreshTokenCache
	data *service.RefreshTokenData
}

func (r *operatorRefreshCache) GetRefreshToken(context.Context, string) (*service.RefreshTokenData, error) {
	return r.data, nil
}
func (r *operatorRefreshCache) StoreRefreshToken(_ context.Context, _ string, d *service.RefreshTokenData, _ time.Duration) error {
	r.data = d
	return nil
}
func (r *operatorRefreshCache) DeleteRefreshToken(context.Context, string) error { return nil }
func (r *operatorRefreshCache) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}
func (r *operatorRefreshCache) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}
func TestBackendModeLoginAndRefreshAllowOperator(t *testing.T) {
	for _, role := range []string{service.RoleAdmin, service.RoleOperator, service.RoleUser} {
		t.Run(role, func(t *testing.T) {
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-only-signing-secret", ExpireHour: 1, RefreshTokenExpireDays: 1}}
			settings := service.NewSettingService(&oauthCaptchaSettingRepo{values: map[string]string{service.SettingKeyBackendModeEnabled: "true"}}, cfg)
			require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{BackendModeEnabled: true}))
			t.Cleanup(func() {
				require.NoError(t, settings.UpdateSettings(context.Background(), &service.SystemSettings{BackendModeEnabled: false}))
			})
			u := &service.User{ID: 1, Role: role, Status: service.StatusActive, TokenVersion: 1, TokenVersionResolved: true}
			cache := &operatorRefreshCache{data: &service.RefreshTokenData{UserID: 1, TokenVersion: 1, FamilyID: "family", ExpiresAt: time.Now().Add(time.Hour)}}
			auth := service.NewAuthService(nil, &operatorHandlerUsers{user: u}, nil, cache, cfg, settings, nil, nil, nil, nil, nil, nil, nil)
			h := &AuthHandler{authService: auth, settingSvc: settings, cfg: cfg}
			passkey := &PasskeyHandler{settingSvc: settings}
			if role == service.RoleUser {
				require.Error(t, h.ensureBackendModeAllowsUser(context.Background(), u))
				require.Error(t, passkey.ensureBackendModeAllowsUser(context.Background(), u))
			} else {
				require.NoError(t, h.ensureBackendModeAllowsUser(context.Background(), u))
				require.NoError(t, passkey.ensureBackendModeAllowsUser(context.Background(), u))
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			rec := httptest.NewRecorder()
			c.Writer = ginWriterForOperatorTest(rec)
			c.Request = httptest.NewRequest("POST", "/refresh", strings.NewReader(`{"refresh_token":"rt_test"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			h.RefreshToken(c)
			want := 200
			if role == service.RoleUser {
				want = 403
			}
			require.Equal(t, want, rec.Code, rec.Body.String())
		})
	}
}
func ginWriterForOperatorTest(rec *httptest.ResponseRecorder) gin.ResponseWriter {
	c, _ := gin.CreateTestContext(rec)
	return c.Writer
}
