//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func nativeControlAuthFixture(t *testing.T, mode string, mutate func(*service.APIKey)) *gin.Engine {
	t.Helper()
	group := &service.Group{ID: 7, Status: service.StatusActive, Hydrated: true}
	user := &service.User{ID: 9, Status: service.StatusActive, Role: service.RoleUser, Balance: 0}
	key := &service.APIKey{ID: 10, Key: "native-control-fixture", UserID: user.ID, User: user, GroupID: &group.ID, Group: group, Status: service.StatusActive}
	if mode == "quota" {
		key.Status = service.StatusAPIKeyQuotaExhausted
	}
	if mode == "subscription" {
		group.SubscriptionType = service.SubscriptionTypeSubscription
	}
	if mutate != nil {
		mutate(key)
	}
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return key, nil }}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	var sub *service.SubscriptionService
	if mode == "subscription" {
		sub = service.NewSubscriptionService(nil, &stubUserSubscriptionRepo{getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
			return nil, errors.New("fixture subscription spent")
		}}, nil, nil, cfg)
		t.Cleanup(sub.Stop)
	}
	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg), sub, cfg)))
	router.Any("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return router
}

func TestNativeResponseControlAuthAfterGenerationSpend(t *testing.T) {
	for _, mode := range []string{"balance", "quota", "subscription"} {
		for _, root := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
			for _, item := range []struct{ method, suffix string }{{http.MethodGet, "/resp_fixture"}, {http.MethodGet, "/resp_fixture?stream=true&starting_after=1"}, {http.MethodPost, "/resp_fixture/cancel"}} {
				t.Run(mode+root+item.suffix, func(t *testing.T) {
					router := nativeControlAuthFixture(t, mode, nil)
					req := httptest.NewRequest(item.method, root+item.suffix, nil)
					req.Header.Set("x-api-key", "native-control-fixture")
					out := httptest.NewRecorder()
					router.ServeHTTP(out, req)
					require.Equal(t, http.StatusNoContent, out.Code, out.Body.String())
				})
			}
		}
	}
}

func TestNativeResponseControlAuthDoesNotBypassGenerationOrPermissions(t *testing.T) {
	for _, item := range []struct{ method, path string }{
		{http.MethodPost, "/v1/responses"}, {http.MethodGet, "/v1/responses"},
		{http.MethodPost, "/v1/responses/resp_fixture/input_items"}, {http.MethodPost, "/v1/responses/compact"},
		{http.MethodPost, "/v1/responses/resp_fixture/cancel/extra"}, {http.MethodPost, "/v1/responses/not_a_response/cancel"},
		{http.MethodGet, "/v1/responses/resp_fixture/input_items"}, {http.MethodGet, "/v1/responses//resp_fixture"},
	} {
		t.Run(item.method+item.path, func(t *testing.T) {
			router := nativeControlAuthFixture(t, "balance", nil)
			req := httptest.NewRequest(item.method, item.path, nil)
			req.Header.Set("x-api-key", "native-control-fixture")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			require.Equal(t, http.StatusForbidden, out.Code, out.Body.String())
		})
	}
	for name, mutate := range map[string]func(*service.APIKey){
		"key_disabled":   func(k *service.APIKey) { k.Status = "disabled" },
		"user_disabled":  func(k *service.APIKey) { k.User.Status = "disabled" },
		"group_disabled": func(k *service.APIKey) { k.Group.Status = "disabled" },
		"group_deleted":  func(k *service.APIKey) { k.Group = nil },
	} {
		t.Run(name, func(t *testing.T) {
			router := nativeControlAuthFixture(t, "balance", mutate)
			req := httptest.NewRequest(http.MethodGet, "/v1/responses/resp_fixture", nil)
			req.Header.Set("x-api-key", "native-control-fixture")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			require.NotEqual(t, http.StatusNoContent, out.Code, out.Body.String())
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, out.Code)
		})
	}
}
