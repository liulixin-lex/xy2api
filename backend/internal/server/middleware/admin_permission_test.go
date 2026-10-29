//go:build unit

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type permissionGuard struct {
	target  func(operatoraccess.TargetRule, operatoraccess.Request) error
	fields  func(operatoraccess.FieldRule, operatoraccess.Request) ([]byte, error)
	targets []operatoraccess.TargetRule
	rules   []operatoraccess.FieldRule
}

func (g *permissionGuard) CheckTarget(_ context.Context, _ *operatoraccess.Scope, rule operatoraccess.TargetRule, r operatoraccess.Request) error {
	g.targets = append(g.targets, rule)
	if g.target != nil {
		return g.target(rule, r)
	}
	return nil
}
func (g *permissionGuard) CheckFields(_ context.Context, _ *operatoraccess.Scope, rule operatoraccess.FieldRule, r operatoraccess.Request) ([]byte, error) {
	g.rules = append(g.rules, rule)
	if g.fields != nil {
		return g.fields(rule, r)
	}
	return nil, nil
}
func permissionScope() *operatoraccess.Scope {
	p := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{}, Flags: map[operatoraccess.Flag]bool{}}
	for _, m := range operatoraccess.Modules {
		p.Modules[m] = operatoraccess.LevelManage
	}
	for _, f := range operatoraccess.Flags {
		p.Flags[f] = true
	}
	return &operatoraccess.Scope{UserID: 7, Perms: p, Guard: &permissionGuard{}}
}
func permissionPath(key string) (string, string, string) {
	method, path, _ := strings.Cut(key, " ")
	if !strings.HasPrefix(path, "/api/") {
		path = operatoraccess.AdminPrefix + path
	}
	actual := path
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ":") {
			actual = strings.ReplaceAll(actual, part, "2")
		}
		if strings.HasPrefix(part, "*") {
			actual = strings.ReplaceAll(actual, part, "x")
		}
	}
	return method, path, actual
}
func permissionRouter(key string, scope *operatoraccess.Scope, role string, h gin.HandlerFunc) (*gin.Engine, string, string) {
	gin.SetMode(gin.TestMode)
	method, path, actual := permissionPath(key)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUserRole), role)
		if scope != nil {
			c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), scope))
		}
		c.Next()
	})
	router.Handle(method, path, AdminPermission(), h)
	return router, method, actual
}
func permissionRequest(t *testing.T, key string, scope *operatoraccess.Scope, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	router, method, path := permissionRouter(key, scope, service.RoleOperator, h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func permissionOK(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) }
func TestAdminPermissionSuperAdminPassesEveryCatalogRoute(t *testing.T) {
	for _, key := range operatoraccess.Keys() {
		t.Run(key, func(t *testing.T) {
			reached := false
			r, method, path := permissionRouter(key, nil, service.RoleAdmin, func(c *gin.Context) { reached = true; c.Status(204) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			require.True(t, reached)
			require.Equal(t, 204, w.Code)
		})
	}
}
func TestAdminPermissionOperatorMatrix(t *testing.T) {
	cases := []struct {
		key     string
		modules map[operatoraccess.Module]operatoraccess.Level
		flags   []operatoraccess.Flag
		code    int
		reason  string
	}{
		{"GET /accounts/:id", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelView}, nil, 200, ""},
		{"GET /accounts/:id", nil, nil, 403, "ADMIN_PERMISSION_DENIED"},
		{"POST /dashboard/users-usage", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleUsers: operatoraccess.LevelView}, nil, 200, ""},
		{"GET /groups/all", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleSubscriptions: operatoraccess.LevelView}, nil, 200, ""},
		{"GET /redeem-codes/export", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleRedeemCodes: operatoraccess.LevelView}, []operatoraccess.Flag{operatoraccess.FlagDataExport}, 403, "ADMIN_PERMISSION_DENIED"},
		{"GET /redeem-codes/export", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleRedeemCodes: operatoraccess.LevelView}, []operatoraccess.Flag{operatoraccess.FlagDataExport, operatoraccess.FlagRedeemCodesIssue}, 200, ""},
		{"POST /accounts/:id/shadow", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleAccounts: operatoraccess.LevelManage}, nil, 403, "ADMIN_PERMISSION_DENIED"},
		{"POST /payment/orders/:id/refund", map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleOrders: operatoraccess.LevelManage}, []operatoraccess.Flag{operatoraccess.FlagOrdersRefund}, 200, ""},
		{"GET /api/v1/pages", nil, nil, 403, "SUPER_ADMIN_REQUIRED"},
		{"PUT /settings", nil, nil, 403, "SUPER_ADMIN_REQUIRED"},
		{"GET /compliance", nil, nil, 200, ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d/%d", tc.key, tc.code, len(tc.flags)), func(t *testing.T) {
			sc := permissionScope()
			sc.Perms.Modules = tc.modules
			sc.Perms.Flags = map[operatoraccess.Flag]bool{}
			for _, f := range tc.flags {
				sc.Perms.Flags[f] = true
			}
			w := permissionRequest(t, tc.key, sc, "{}", permissionOK)
			require.Equal(t, tc.code, w.Code, w.Body.String())
			if tc.reason != "" {
				require.Contains(t, w.Body.String(), tc.reason)
			}
		})
	}
}
func TestAdminPermissionUnknownRouteDenied(t *testing.T) {
	w := permissionRequest(t, "GET /future-route", permissionScope(), "", permissionOK)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), `"required":"catalog"`)
}

func TestAdminPermissionSupportTicketsPermissionMatrix(t *testing.T) {
	for _, key := range []string{"GET /support-tickets", "GET /support-tickets/summary", "GET /support-tickets/:id", "POST /support-tickets/:id/messages", "POST /support-tickets/:id/status", "DELETE /support-tickets/:id"} {
		for _, level := range []operatoraccess.Level{operatoraccess.LevelNone, operatoraccess.LevelView, operatoraccess.LevelManage} {
			t.Run(key+level.String(), func(t *testing.T) {
				sc := permissionScope()
				sc.Perms.Modules[operatoraccess.ModuleSupportTickets] = level
				reached := false
				w := permissionRequest(t, key, sc, "{}", func(c *gin.Context) { reached = true; c.Status(204) })
				allowed := level == operatoraccess.LevelManage || (level == operatoraccess.LevelView && strings.HasPrefix(key, "GET "))
				require.Equal(t, allowed, reached)
				if allowed {
					require.Equal(t, 204, w.Code)
				} else {
					require.Equal(t, 403, w.Code)
					require.Contains(t, w.Body.String(), "ADMIN_PERMISSION_DENIED")
				}
			})
		}
	}
}
func TestAdminPermissionOpenAITOTPRequiresSuperAdmin(t *testing.T) {
	for _, key := range []string{"GET /accounts/:id/openai-totp", "PUT /accounts/:id/openai-totp", "POST /accounts/:id/openai-totp/rotate", "POST /accounts/:id/openai-totp/reconcile", "POST /accounts/:id/openai-totp/export", "POST /accounts/openai-login", "GET /accounts/openai-login/:task_id", "POST /accounts/openai-login/:task_id/import"} {
		reached := false
		w := permissionRequest(t, key, permissionScope(), "{}", func(c *gin.Context) { reached = true; c.Status(204) })
		require.False(t, reached, key)
		require.Equal(t, 403, w.Code, key)
		require.Contains(t, w.Body.String(), "SUPER_ADMIN_REQUIRED")
	}
}
func TestAdminPermissionEmptyFullPathDenied(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/", nil)
	c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), permissionScope()))
	AdminPermission()(c)
	require.True(t, c.IsAborted())
	require.Equal(t, 403, w.Code)
}
func TestAdminPermissionNonAdminWithoutScopeDenied(t *testing.T) {
	for _, role := range []string{"", service.RoleUser, service.RoleOperator} {
		r, method, path := permissionRouter("GET /accounts", nil, role, permissionOK)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		require.Equal(t, 403, w.Code)
	}
}
func TestAdminPermissionTargetRules(t *testing.T) {
	for _, key := range []string{"PUT /users/:id", "POST /users/batch-concurrency", "PUT /api-keys/:id", "DELETE /subscriptions/:id", "DELETE /groups/:id"} {
		for _, target := range []string{"admin", "operator", "self", "mixed"} {
			t.Run(key+target, func(t *testing.T) {
				sc := permissionScope()
				g := sc.Guard.(*permissionGuard)
				g.target = func(_ operatoraccess.TargetRule, r operatoraccess.Request) error {
					require.NotEmpty(t, r.Key)
					return infraerrors.Forbidden("ADMIN_TARGET_FORBIDDEN", "Target user is not allowed")
				}
				w := permissionRequest(t, key, sc, `{"user_ids":[1,2]}`, permissionOK)
				require.Equal(t, 403, w.Code)
				require.Len(t, g.targets, 1)
				require.Empty(t, g.rules)
			})
		}
	}
}
func TestAdminPermissionRestoresBody(t *testing.T) {
	body := `{"User_IDs":[2,3],"concurrency":4}`
	sc := permissionScope()
	sc.Guard.(*permissionGuard).target = func(_ operatoraccess.TargetRule, r operatoraccess.Request) error {
		require.Equal(t, body, string(r.Body))
		return nil
	}
	w := permissionRequest(t, "POST /users/batch-concurrency", sc, body, func(c *gin.Context) {
		got, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(got))
		c.JSON(200, gin.H{})
	})
	require.Equal(t, 200, w.Code)
}
func TestAdminPermissionCanonicalAndBodyLimit(t *testing.T) {
	for _, body := range []string{`{"user_ids":[],"User_IDs":[1]}`, `{} {}`, `[]`} {
		w := permissionRequest(t, "POST /users/batch-concurrency", permissionScope(), body, permissionOK)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), "noncanonical")
	}
	w := permissionRequest(t, "POST /users/batch-concurrency", permissionScope(), strings.Repeat("x", (16<<20)+1), permissionOK)
	require.Equal(t, 413, w.Code)
	require.Contains(t, w.Body.String(), "REQUEST_TOO_LARGE")
	r, m, p := permissionRouter("GET /promo-codes", permissionScope(), service.RoleOperator, permissionOK)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(m, p+"?search=x&search=y", nil))
	require.Equal(t, 400, w.Code)
}
func TestAdminPermissionFieldRuleRewritesBody(t *testing.T) {
	sc := permissionScope()
	sc.Guard.(*permissionGuard).fields = func(rule operatoraccess.FieldRule, r operatoraccess.Request) ([]byte, error) {
		require.Equal(t, operatoraccess.FieldRule("monitor_update"), rule)
		require.Contains(t, string(r.Body), "******")
		return []byte(`{"extra_headers":{"x-key":"stored"}}`), nil
	}
	w := permissionRequest(t, "PUT /channel-monitors/:id", sc, `{"extra_headers":{"x-key":"******"}}`, func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, int64(len(raw)), c.Request.ContentLength)
		require.JSONEq(t, `{"extra_headers":{"x-key":"stored"}}`, string(raw))
		c.JSON(200, gin.H{})
	})
	require.Equal(t, 200, w.Code)
}
func TestAdminPermissionCallsGuardForEveryMiddlewareFieldRule(t *testing.T) {
	for _, key := range operatoraccess.Keys() {
		entry, _ := operatoraccess.Lookup(key)
		if entry.Fields == "" {
			continue
		}
		t.Run(key, func(t *testing.T) {
			sc := permissionScope()
			g := sc.Guard.(*permissionGuard)
			w := permissionRequest(t, key, sc, "{}", permissionOK)
			require.Equal(t, 200, w.Code, w.Body.String())
			if operatoraccess.ServiceEnforcedFields[entry.Fields] {
				require.Empty(t, g.rules)
			} else {
				require.Equal(t, []operatoraccess.FieldRule{entry.Fields}, g.rules)
			}
		})
	}
}
func TestAdminPermissionRedactsJSONOnly(t *testing.T) {
	sc := permissionScope()
	delete(sc.Perms.Flags, operatoraccess.FlagAccountsUpstreamAddress)
	w := permissionRequest(t, "GET /accounts/:id", sc, "", func(c *gin.Context) {
		c.JSON(200, gin.H{"platform": "openai", "type": "apikey", "credentials": gin.H{"base_url": "https://hidden.example"}})
	})
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "hidden.example")
	for _, media := range []string{"application/octet-stream", "text/plain"} {
		w = permissionRequest(t, "GET /accounts/:id", sc, "", func(c *gin.Context) {
			c.Header("Content-Type", media)
			_, err := c.Writer.WriteString("raw-bytes")
			require.NoError(t, err)
			c.Writer.Flush()
		})
		require.Equal(t, "raw-bytes", w.Body.String())
		require.True(t, w.Flushed)
	}
	w = permissionRequest(t, "GET /proxies", sc, "", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		_, err := c.Writer.WriteString("data: raw\n\n")
		require.NoError(t, err)
		c.Writer.Flush()
	})
	require.Equal(t, "data: raw\n\n", w.Body.String())
	w = permissionRequest(t, "GET /accounts/:id", sc, "", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		c.Header("Content-Disposition", "attachment; filename=x.json")
		_, err := c.Writer.WriteString(`{"raw":"kept"}`)
		require.NoError(t, err)
	})
	require.Equal(t, `{"raw":"kept"}`, w.Body.String())
	w = permissionRequest(t, "GET /accounts/:id", sc, "", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		_, err := c.Writer.WriteString("invalid https://hidden.example")
		require.NoError(t, err)
	})
	require.Equal(t, 500, w.Code)
	require.Contains(t, w.Body.String(), "ADMIN_REDACTION_FAILED")
	require.NotContains(t, w.Body.String(), "hidden.example")
}
func TestAdminPermissionScrubsSSEErrorEvents(t *testing.T) {
	errors := []string{"Post https://h.example/x: timeout", "Invalid base URL: invalid url: h.example:8080", "lookup h.example: no such host", "x509: certificate is valid for a.example, not h.example", "dial tcp 203.0.113.5:443: connect: connection refused"}
	for _, flag := range []bool{false, true} {
		for _, detail := range errors {
			t.Run(fmt.Sprintf("%t/%s", flag, detail), func(t *testing.T) {
				sc := permissionScope()
				sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = flag
				r, m, p := permissionRouter("POST /accounts/:id/test", sc, service.RoleOperator, func(c *gin.Context) {
					c.Header("Content-Type", "text/event-stream")
					raw, _ := json.Marshal(gin.H{"type": "error", "error": detail})
					line := append(append([]byte("data: "), raw...), []byte("\n\n")...)
					mid := len(line) / 2
					_, err := c.Writer.Write(line[:mid])
					require.NoError(t, err)
					c.Writer.Flush()
					if !flag {
						under := c.Writer.(*adminPermissionWriter).ResponseWriter
						require.Equal(t, 0, under.Size())
					}
					_, err = c.Writer.Write(line[mid:])
					require.NoError(t, err)
					c.Writer.Flush()
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(m, p, nil))
				require.True(t, w.Flushed)
				if flag {
					require.Contains(t, w.Body.String(), detail)
				} else {
					require.Contains(t, w.Body.String(), operatoraccess.ErrorDetailHidden)
					require.NotContains(t, w.Body.String(), "h.example")
					require.NotContains(t, w.Body.String(), "203.0.113.5")
					require.NotContains(t, w.Body.String(), ":443")
					var event map[string]any
					require.NoError(t, json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(w.Body.Bytes(), []byte("data: "))), &event))
				}
			})
		}
	}
	sc := permissionScope()
	sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = false
	w := permissionRequest(t, "POST /accounts/:id/test", sc, "", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		_, err := c.Writer.WriteString("data: broken h.example:8080")
		require.NoError(t, err)
	})
	require.Contains(t, w.Body.String(), operatoraccess.ErrorDetailHidden)
	require.NotContains(t, w.Body.String(), "h.example")
}
func TestAdminPermissionScrubsURLsIn5xxForAccountRows(t *testing.T) {
	for _, flag := range []bool{false, true} {
		sc := permissionScope()
		sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = flag
		w := permissionRequest(t, "GET /accounts/:id", sc, "", func(c *gin.Context) {
			c.JSON(502, gin.H{"error": `Post "https://h/x": timeout`, "message": "dial tcp 203.0.113.5:443: refused"})
		})
		require.Equal(t, 502, w.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		if flag {
			require.Contains(t, body["message"], "203.0.113.5")
		} else {
			require.Equal(t, operatoraccess.ErrorDetailHidden, body["error"])
			require.Equal(t, operatoraccess.ErrorDetailHidden, body["message"])
		}
	}
}
func TestAdminPermissionRecomputesETag(t *testing.T) {
	sc := permissionScope()
	sc.Perms.Flags[operatoraccess.FlagAccountsUpstreamAddress] = false
	handler := func(c *gin.Context) {
		require.Empty(t, c.GetHeader("If-None-Match"))
		c.Header("ETag", `"raw"`)
		c.JSON(200, gin.H{"error": "https://h/x"})
	}
	r, m, p := permissionRouter("GET /accounts/:id", sc, service.RoleOperator, handler)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(m, p, nil))
	etag := w.Header().Get("ETag")
	require.NotEqual(t, `"raw"`, etag)
	require.True(t, strings.HasPrefix(etag, `W/"`))
	req := httptest.NewRequest(m, p, nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 304, w.Code)
	require.Empty(t, w.Body.String())
}
func TestAdminPermissionMounts(t *testing.T) {
	for _, tc := range []struct{ path, auth, permission, compliance string }{{"../routes/admin.go", "admin.Use(gin.HandlerFunc(auditLog))", "admin.Use(middleware.AdminPermission())", "admin.Use(middleware.AdminComplianceGuard(settingService))"}, {"../routes/payment.go", "adminGroup.Use(gin.HandlerFunc(auditLog))", "adminGroup.Use(middleware.AdminPermission())", "adminGroup.Use(middleware.AdminComplianceGuard(settingService))"}, {"../../handler/page_handler.go", "adminPages.Use(adminAuth)", "adminPages.Use(middleware2.AdminPermission())", "adminPages.Use(middleware2.AdminComplianceGuard(settingService))"}} {
		raw, err := os.ReadFile(tc.path)
		require.NoError(t, err)
		s := string(raw)
		require.Equal(t, 1, strings.Count(s, tc.permission))
		require.Less(t, strings.Index(s, tc.auth), strings.Index(s, tc.permission))
		require.Less(t, strings.Index(s, tc.permission), strings.Index(s, tc.compliance))
	}
}

func TestAdminPermissionWebSocketClosesAfterRevoke(t *testing.T) {
	for _, which := range []string{"permission", "role", "error"} {
		t.Run(which, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sc := permissionScope()
			sc.Reload = func(context.Context) (operatoraccess.Permissions, bool, error) {
				switch which {
				case "role":
					return sc.Perms, false, nil
				case "error":
					return sc.Perms, true, errors.New("load failed")
				default:
					return operatoraccess.Permissions{}, true, nil
				}
			}
			done := make(chan struct{})
			go func() { recheckOperatorWebSocket(ctx, cancel, sc, time.Millisecond); close(done) }()
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("revoked connection remained open")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("recheck leaked")
			}
		})
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	sc := permissionScope()
	var handlerCtx context.Context
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(operatoraccess.WithScope(c.Request.Context(), sc))
	}, AdminPermission())
	r.GET("/api/v1/admin/ops/ws/qps", func(c *gin.Context) {
		handlerCtx = c.Request.Context()
		require.NoError(t, handlerCtx.Err())
		c.Status(204)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/ops/ws/qps", nil))
	require.Equal(t, 204, rec.Code)
	require.ErrorIs(t, handlerCtx.Err(), context.Canceled)
}
