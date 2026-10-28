//go:build unit

package server

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unsafe"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/handler"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/liulixin-lex/xy2api/internal/web"
	"github.com/stretchr/testify/require"
)

type probedRoute struct {
	method, path string
	admin        bool
	names        []string
}
type enumerationSettings struct{ service.SettingRepository }

func (enumerationSettings) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (enumerationSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}
func (enumerationSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func sentinelJWT(c *gin.Context)         { c.AbortWithStatus(204) }
func sentinelOptionalJWT(c *gin.Context) { c.AbortWithStatus(204) }
func sentinelAdminAuth(c *gin.Context)   { c.Set("pra.admin", true); c.AbortWithStatus(204) }
func sentinelAPIKey(c *gin.Context)      { c.AbortWithStatus(204) }
func sentinelAudit(c *gin.Context)       { c.AbortWithStatus(204) }
func sentinelStepUp(c *gin.Context)      { c.AbortWithStatus(204) }

// Fill exported handler pointers only: their services deliberately remain nil.
func enumerationHandlers() *handler.Handlers {
	h := &handler.Handlers{}
	v := reflect.ValueOf(h).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.Pointer {
			v.Field(i).Set(reflect.New(v.Field(i).Type().Elem()))
		}
	}
	a := reflect.ValueOf(h.Admin).Elem()
	for i := 0; i < a.NumField(); i++ {
		if a.Field(i).Kind() == reflect.Pointer {
			a.Field(i).Set(reflect.New(a.Field(i).Type().Elem()))
		}
	}
	return h
}
func enumerationEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = io.Discard
	t.Cleanup(func() { gin.DefaultErrorWriter = oldWriter })
	cfg := &config.Config{}
	settings := service.NewSettingService(enumerationSettings{}, cfg)
	return ProvideRouter(cfg, enumerationHandlers(), sentinelJWT, sentinelOptionalJWT, sentinelAdminAuth, sentinelAPIKey, sentinelAudit, sentinelStepUp, nil, nil, nil, settings, nil, nil)
}
func probeRoutes(r *gin.Engine) []probedRoute {
	out := []probedRoute{}
	for _, route := range r.Routes() {
		path := route.Path
		parts := strings.Split(path, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, ":") {
				parts[i] = "1"
			}
			if strings.HasPrefix(p, "*") {
				parts[i] = "probe"
			}
		}
		req := httptest.NewRequest(route.Method, strings.Join(parts, "/"), nil)
		rec := httptest.NewRecorder()
		// Capture the actual Gin chain, including the sentinel, before it aborts.
		var names []string
		var admin bool
		c := gin.CreateTestContextOnly(rec, r)
		c.Request = req
		r.HandleContext(c)
		names = c.HandlerNames()
		admin = c.GetBool("pra.admin")
		out = append(out, probedRoute{route.Method, route.Path, admin, names})
	}
	return out
}
func catalogGaps(routes []probedRoute) []string {
	gaps := []string{}
	seen := map[string]int{}
	for _, r := range routes {
		if r.admin {
			k := operatoraccess.Key(r.method, r.path)
			seen[k]++
			if _, ok := operatoraccess.Lookup(k); !ok {
				gaps = append(gaps, k)
			}
		}
	}
	for k, n := range seen {
		if n != 1 {
			gaps = append(gaps, fmt.Sprintf("%s occurs %d times", k, n))
		}
	}
	sort.Strings(gaps)
	return gaps
}
func nameIndex(names []string, fragment string) int {
	for i, n := range names {
		if strings.Contains(n, fragment) {
			return i
		}
	}
	return -1
}
func TestEveryAdminAuthRouteHasCatalogEntry(t *testing.T) {
	require.Empty(t, catalogGaps(probeRoutes(enumerationEngine(t))))
}
func TestEveryCatalogEntryHasRoute(t *testing.T) {
	counts := map[string]int{}
	for _, r := range probeRoutes(enumerationEngine(t)) {
		counts[operatoraccess.Key(r.method, r.path)]++
	}
	for _, k := range operatoraccess.Keys() {
		require.Equal(t, 1, counts[k], k)
	}
}
func TestEveryAdminAuthRouteRunsAdminPermission(t *testing.T) {
	for _, r := range probeRoutes(enumerationEngine(t)) {
		if !r.admin || operatoraccess.Key(r.method, r.path) == operatoraccess.MeAccessKey {
			continue
		}
		a := nameIndex(r.names, "sentinelAdminAuth")
		p := nameIndex(r.names, "AdminPermission")
		c := nameIndex(r.names, "AdminComplianceGuard")
		require.Greater(t, p, a, r.path)
		require.Greater(t, c, p, r.path)
	}
}
func TestMeAccessSkipsComplianceAndPermission(t *testing.T) {
	found := false
	for _, r := range probeRoutes(enumerationEngine(t)) {
		if operatoraccess.Key(r.method, r.path) == operatoraccess.MeAccessKey {
			found = true
			require.True(t, r.admin)
			require.NotEqual(t, -1, nameIndex(r.names, "PanelRateLimiter"))
			require.Equal(t, -1, nameIndex(r.names, "AdminPermission"))
			require.Equal(t, -1, nameIndex(r.names, "AdminComplianceGuard"))
		}
	}
	require.True(t, found)
}
func TestDownloadTicketIsTheOnlyAdminPathWithoutAdminAuth(t *testing.T) {
	for _, r := range probeRoutes(enumerationEngine(t)) {
		if strings.HasPrefix(r.path, "/api/v1/admin/") && !r.admin {
			require.Equal(t, operatoraccess.DownloadTicketKey, operatoraccess.Key(r.method, r.path))
		}
	}
}
func TestCatalogGapsReportsUnregisteredRoute(t *testing.T) {
	r := enumerationEngine(t)
	r.Group("/api/v1/admin", sentinelAdminAuth).GET("/__pra_unregistered", func(c *gin.Context) { c.Status(200) })
	require.Contains(t, catalogGaps(probeRoutes(r)), "GET /__pra_unregistered")
}
func TestEnumerationCoversPaymentPagesAndWebSocket(t *testing.T) {
	keys := map[string]bool{}
	for _, r := range probeRoutes(enumerationEngine(t)) {
		if r.admin {
			keys[operatoraccess.Key(r.method, r.path)] = true
		}
	}
	for _, key := range []string{"POST /payment/orders/:id/refund", "GET /api/v1/pages", "GET /ops/ws/qps"} {
		require.True(t, keys[key], key)
	}
}
func functionNames(chain gin.HandlersChain) []string {
	names := make([]string, len(chain))
	for i, f := range chain {
		names[i] = runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	}
	return names
}
func globalAllowlist() []string {
	names := []string{"gin.CustomRecoveryWithWriter.", "middleware.RequestLogger.", "middleware.SessionBindingContext.", "middleware.MonitorProbe.", "middleware.Logger.", "middleware.CORS.", "middleware.SecurityHeaders.", "middleware.ServerTiming."}
	if web.HasEmbeddedFrontend() {
		names = append(names, "web.(*FrontendServer).Middleware.")
	}
	return names
}
func globalViolations(r *gin.Engine) []string {
	names := functionNames(r.Handlers)
	expected := globalAllowlist()
	if len(names) != len(expected) {
		return []string{fmt.Sprint(names)}
	}
	for i, n := range names {
		if !strings.Contains(n, expected[i]) {
			return []string{fmt.Sprint(names)}
		}
	}
	return nil
}
func fallbackChainViolations(r *gin.Engine) []string {
	out := globalViolations(r)
	v := reflect.ValueOf(r).Elem()
	for _, name := range []string{"noRoute", "noMethod", "allNoRoute", "allNoMethod"} {
		field := v.FieldByName(name)
		if !field.IsValid() || !field.CanAddr() {
			out = append(out, "missing gin field "+name)
			continue
		}
		chain, ok := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(gin.HandlersChain)
		if !ok {
			out = append(out, "unexpected gin field type "+name)
			continue
		}
		if strings.HasPrefix(name, "all") {
			if !reflect.DeepEqual(functionNames(chain), functionNames(r.Handlers)) {
				out = append(out, name)
			}
		} else if len(chain) != 0 {
			out = append(out, name)
		}
	}
	return out
}
func TestGlobalMiddlewareAllowlist(t *testing.T) {
	require.Empty(t, globalViolations(enumerationEngine(t)))
}
func TestEngineHasNoCustomFallbackChain(t *testing.T) {
	require.Empty(t, fallbackChainViolations(enumerationEngine(t)))
}
func TestFallbackCheckRejectsSelectiveFallback(t *testing.T) {
	selective := func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/v1/admin/") {
			c.AbortWithStatus(200)
		}
	}
	for _, kind := range []string{"NoRoute", "NoMethod", "Use"} {
		t.Run(kind, func(t *testing.T) {
			r := enumerationEngine(t)
			switch kind {
			case "NoRoute":
				r.NoRoute(selective)
			case "NoMethod":
				r.NoMethod(selective)
			case "Use":
				r.Use(selective)
				require.NotEmpty(t, globalViolations(r))
			}
			require.NotEmpty(t, fallbackChainViolations(r))
		})
	}
}
func TestUnknownAdminPathsNeverReachAHandler(t *testing.T) {
	r := enumerationEngine(t)
	for _, tc := range []struct{ method, path string }{{"GET", "/api/v1/admin/__unknown"}, {"POST", "/api/v1/admin/users/1/__unknown"}, {"PATCH", "/api/v1/admin/users/1"}, {"GET", "/api/v1/admin/users/"}} {
		rec := httptest.NewRecorder()
		c := gin.CreateTestContextOnly(rec, r)
		c.Request = httptest.NewRequest(tc.method, tc.path, nil)
		r.HandleContext(c)
		require.Contains(t, []int{301, 307, 404, 405}, rec.Code)
		require.False(t, c.GetBool("pra.admin"))
		require.Equal(t, -1, nameIndex(c.HandlerNames(), "sentinel"))
	}
}

var _ middleware.AdminAuthMiddleware = sentinelAdminAuth
