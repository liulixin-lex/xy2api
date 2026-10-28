//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type operatorAuthPermissionRepo struct {
	raw []byte
	err error
}

func (r *operatorAuthPermissionRepo) Get(context.Context, int64) ([]byte, bool, error) {
	return r.raw, true, r.err
}
func (r *operatorAuthPermissionRepo) Upsert(context.Context, int64, []byte, int64) error { return nil }

type operatorAuthUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *operatorAuthUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}
func (r *operatorAuthUserRepo) GetFirstAdmin(context.Context) (*service.User, error) {
	return &service.User{ID: 1, Role: service.RoleAdmin, Status: service.StatusActive}, nil
}

func (r *operatorAuthUserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type operatorAuthSettings struct{ service.SettingRepository }

func (*operatorAuthSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyAdminAPIKey {
		return "unit-admin-key", nil
	}
	return "", service.ErrSettingNotFound
}

func operatorAuthEnvironment(t *testing.T, role string, withService bool, h gin.HandlerFunc) (*gin.Engine, *operatorAuthUserRepo, *operatorAuthPermissionRepo, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	users := &operatorAuthUserRepo{user: &service.User{ID: 7, Role: role, Email: "operator@example.com", Status: service.StatusActive}}
	repo := &operatorAuthPermissionRepo{raw: []byte(`{"modules":{"accounts":"view"},"flags":[]}`)}
	auth := service.NewAuthService(nil, users, nil, nil, &config.Config{JWT: config.JWTConfig{Secret: "unit-operator-auth-secret", ExpireHour: 1}}, nil, nil, nil, nil, nil, nil, nil, nil)
	us := service.NewUserService(users, nil, nil, nil)
	settings := service.NewSettingService(&operatorAuthSettings{}, &config.Config{})
	var access *service.OperatorAccessService
	if withService {
		access = service.NewOperatorAccessService(repo, users, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	}
	r := gin.New()
	r.GET("/t", gin.HandlerFunc(NewAdminAuthMiddleware(auth, us, settings, nil, access)), h)
	token, err := auth.GenerateToken(context.Background(), users.user)
	require.NoError(t, err)
	return r, users, repo, token
}
func operatorAuthDo(r *gin.Engine, token string, key bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/t", nil)
	if key {
		req.Header.Set("x-api-key", "unit-admin-key")
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("operator_permissions", "forged")
	req.Header.Set("X-Admin-Actor", "super_admin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
func TestAdminAuthAcceptsOperatorAndSetsScope(t *testing.T) {
	r, users, repo, token := operatorAuthEnvironment(t, service.RoleOperator, true, func(c *gin.Context) {
		sc := operatoraccess.ScopeFrom(c.Request.Context())
		require.NotNil(t, sc)
		require.Equal(t, int64(7), sc.UserID)
		require.True(t, sc.Can(operatoraccess.ModuleAccounts, operatoraccess.LevelView))
		got, ok := c.Get(operatoraccess.GinKey)
		require.True(t, ok)
		require.Same(t, sc, got)
		require.Equal(t, operatoraccess.ActorOperator, operatoraccess.ActorFrom(c.Request.Context()).Kind)
		c.Status(204)
	})
	require.Equal(t, 204, operatorAuthDo(r, token, false).Code)
	repo.err = errors.New("unavailable")
	require.Equal(t, 503, operatorAuthDo(r, token, false).Code)
	repo.err = nil
	users.user.Role = service.RoleUser
	require.Equal(t, 403, operatorAuthDo(r, token, false).Code)
}
func TestAdminAuthRejectsPlainUser(t *testing.T) {
	r, _, _, token := operatorAuthEnvironment(t, service.RoleUser, true, func(*gin.Context) { t.Fatal("plain user reached admin handler") })
	require.Equal(t, 403, operatorAuthDo(r, token, false).Code)
}
func TestAdminAuthAPIKeyNeverCarriesOperatorScope(t *testing.T) {
	r, _, _, token := operatorAuthEnvironment(t, service.RoleOperator, true, func(c *gin.Context) {
		require.Nil(t, operatoraccess.ScopeFrom(c.Request.Context()))
		_, ok := c.Get(operatoraccess.GinKey)
		require.False(t, ok)
		role, _ := GetUserRoleFromContext(c)
		require.Equal(t, service.RoleAdmin, role)
		c.Status(204)
	})
	require.Equal(t, 204, operatorAuthDo(r, token, true).Code)
}
func TestAdminAuthOperatorFailsClosedWithoutService(t *testing.T) {
	for _, withService := range []bool{false, true} {
		r, _, repo, token := operatorAuthEnvironment(t, service.RoleOperator, withService, func(*gin.Context) { t.Fatal("unavailable operator reached handler") })
		repo.err = errors.New("failed read")
		w := operatorAuthDo(r, token, false)
		require.Equal(t, 503, w.Code)
		require.Contains(t, w.Body.String(), "OPERATOR_ACCESS_UNAVAILABLE")
	}
}
func TestAdminAuthSetsSuperAdminActor(t *testing.T) {
	for _, key := range []bool{false, true} {
		r, _, _, token := operatorAuthEnvironment(t, service.RoleAdmin, false, func(c *gin.Context) {
			require.Equal(t, operatoraccess.ActorSuperAdmin, operatoraccess.ActorFrom(c.Request.Context()).Kind)
			require.Nil(t, operatoraccess.ScopeFrom(c.Request.Context()))
			c.Status(204)
		})
		require.Equal(t, 204, operatorAuthDo(r, token, key).Code)
	}
}
