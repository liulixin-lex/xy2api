//go:build unit

package routes

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/handler"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cachePanelRouteReader struct {
	calls int
	err   error
}

func (r *cachePanelRouteReader) Query(_ context.Context, kind string, _ service.CachePanelQuery) (any, error) {
	r.calls++
	return map[string]any{"view": kind}, r.err
}
func cachePanelRouter(reader adminhandler.CachePanelReader, auth gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	admin := r.Group("/api/v1/admin")
	if auth != nil {
		admin.Use(auth)
	}
	registerCacheObservabilityRoutes(admin, &handler.Handlers{Admin: &handler.AdminHandlers{CacheObservability: adminhandler.NewCacheObservabilityHandler(reader)}})
	return r
}
func TestCacheNorthstarAdminPermissionAllReadEndpoints(t *testing.T) {
	reader := &cachePanelRouteReader{}
	r := cachePanelRouter(reader, cacheOrdinaryUserAuth(t))
	for _, path := range []string{"northstar", "breakdown?dimension=group", "stats", "experiments"} {
		status, body := cacheCall(t, r, "GET", "/api/v1/admin/cache/"+path, nil)
		require.Equal(t, 403, status)
		require.Equal(t, "FORBIDDEN", body["code"])
	}
	require.Zero(t, reader.calls)
}
func TestCacheNorthstarRoutesValidateAndReadOnly(t *testing.T) {
	reader := &cachePanelRouteReader{}
	r := cachePanelRouter(reader, nil)
	for _, path := range []string{"northstar?window=1h", "breakdown?dimension=session", "stats?window=30d", "experiments?limit=10"} {
		status, body := cacheCall(t, r, "GET", "/api/v1/admin/cache/"+path, nil)
		require.Equal(t, 400, status)
		require.Equal(t, "CACHE_PANEL_INVALID_PARAM", body["reason"])
	}
	require.Zero(t, reader.calls)
	for _, path := range []string{"northstar", "breakdown?dimension=model", "stats", "experiments"} {
		status, body := cacheCall(t, r, "GET", "/api/v1/admin/cache/"+path, nil)
		require.Equal(t, 200, status)
		require.EqualValues(t, 0, body["code"])
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/admin/cache/"+path, nil))
		require.Equal(t, 404, rec.Code)
	}
	reader.err = &service.CachePolicyError{Status: 503, Reason: "CACHE_ROLLUP_NOT_READY", Message: "cache rollup has no complete hour"}
	status, body := cacheCall(t, r, "GET", "/api/v1/admin/cache/northstar", nil)
	require.Equal(t, 503, status)
	require.Equal(t, "CACHE_ROLLUP_NOT_READY", body["reason"])
}
