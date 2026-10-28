//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accessHandlerUsers struct{ service.UserRepository }

func (accessHandlerUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	if id == 404 {
		return nil, service.ErrUserNotFound
	}
	return &service.User{ID: id, Role: service.RoleOperator}, nil
}

type accessHandlerRepo struct {
	raw    []byte
	writes int
}

func (r *accessHandlerRepo) Get(context.Context, int64) ([]byte, bool, error) {
	return r.raw, r.raw != nil, nil
}
func (r *accessHandlerRepo) Upsert(_ context.Context, _ int64, raw []byte, _ int64) error {
	r.raw = raw
	r.writes++
	return nil
}

type accessHandlerSettings struct {
	service.SettingRepository
	stepup bool
}

func (r accessHandlerSettings) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyStepUpEnabled {
		if r.stepup {
			return "true", nil
		}
		return "false", nil
	}
	return "", service.ErrSettingNotFound
}
func (accessHandlerSettings) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (accessHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func accessHandlerRouter(t *testing.T, role string, stepup bool) (*gin.Engine, *accessHandlerRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &accessHandlerRepo{}
	users := accessHandlerUsers{}
	settings := service.NewSettingService(accessHandlerSettings{stepup: stepup}, &config.Config{})
	access := service.NewOperatorAccessService(repo, users, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil, nil, nil)
	h := NewOperatorAccessHandler(access, nil, service.NewUserService(users, nil, nil, nil), settings)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", middleware.AuthSubject{UserID: 7})
		c.Set("user_role", role)
		c.Set("auth_method", service.AuditAuthMethodAdminAPIKey)
	})
	r.GET("/me", h.MeAccess)
	r.GET("/users/:id", h.GetPermissions)
	r.PUT("/users/:id", h.PutPermissions)
	return r, repo
}
func accessHandlerRequest(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}
func TestMeAccessContractShape(t *testing.T) {
	for _, role := range []string{service.RoleOperator, service.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			r, repo := accessHandlerRouter(t, role, false)
			repo.raw = []byte(`{"modules":{"accounts":"manage"},"flags":["data.export","accounts.upstream_address"]}`)
			rec := accessHandlerRequest(r, "GET", "/me", "")
			require.Equal(t, 200, rec.Code, rec.Body.String())
			var envelope struct{ Data map[string]json.RawMessage }
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			require.Len(t, envelope.Data, 6)
			require.JSONEq(t, `[]`, string(envelope.Data["custom_menu_items"]))
			var settings map[string]any
			require.NoError(t, json.Unmarshal(envelope.Data["settings"], &settings))
			require.Len(t, settings, 10)
			var flags []string
			require.NoError(t, json.Unmarshal(envelope.Data["flags"], &flags))
			require.IsIncreasing(t, flags)
			var modules map[string]string
			require.NoError(t, json.Unmarshal(envelope.Data["modules"], &modules))
			if role == service.RoleAdmin {
				require.Len(t, modules, len(operatoraccess.Modules))
				require.Len(t, flags, len(operatoraccess.Flags))
			} else {
				require.Equal(t, map[string]string{"accounts": "manage"}, modules)
				require.Len(t, flags, 2)
			}
		})
	}
}
func TestGetOperatorPermissionsEmptyShape(t *testing.T) {
	r, _ := accessHandlerRouter(t, service.RoleAdmin, false)
	rec := accessHandlerRequest(r, "GET", "/users/8", "")
	require.Equal(t, 200, rec.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":{"modules":{},"flags":[]}}`, rec.Body.String())
	rec = accessHandlerRequest(r, "GET", "/users/404", "")
	require.Equal(t, 404, rec.Code)
	require.Contains(t, rec.Body.String(), "USER_NOT_FOUND")
}

type accessUnreadBody struct{ t *testing.T }

func (b accessUnreadBody) Read([]byte) (int, error) {
	b.t.Fatal("body read before step-up")
	return 0, io.EOF
}
func (accessUnreadBody) Close() error { return nil }
func TestPutOperatorPermissionsRequiresStepUp(t *testing.T) {
	r, repo := accessHandlerRouter(t, service.RoleAdmin, true)
	req := httptest.NewRequest("PUT", "/users/8", nil)
	req.Body = accessUnreadBody{t}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, 403, rec.Code)
	require.Contains(t, rec.Body.String(), "STEP_UP_ADMIN_API_KEY_FORBIDDEN")
	require.Zero(t, repo.writes)
}
func TestPutOperatorPermissionsRejectsUnknownKeys(t *testing.T) {
	for _, body := range []string{`{"unknown":1}`, `{"modules":{"unknown":"view"}}`, `{"modules":{"accounts":"none"}}`, `{"flags":["unknown"]}`} {
		r, repo := accessHandlerRouter(t, service.RoleAdmin, false)
		rec := accessHandlerRequest(r, "PUT", "/users/8", body)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "ADMIN_PERMISSION_INVALID")
		require.Zero(t, repo.writes)
	}
	r, repo := accessHandlerRouter(t, service.RoleAdmin, false)
	rec := accessHandlerRequest(r, "PUT", "/users/8", `{"modules":{},"flags":["data.export","data.export"]}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, 1, repo.writes)
	require.JSONEq(t, `{"modules":{},"flags":["data.export"]}`, string(repo.raw))
}
