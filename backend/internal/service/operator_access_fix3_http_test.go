//go:build unit

package service_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	adminhandler "github.com/liulixin-lex/xy2api/internal/handler/admin"
	"github.com/liulixin-lex/xy2api/internal/pkg/operatoraccess"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type fix3MonitorRepo struct {
	service.ChannelMonitorRepository
	row *service.ChannelMonitor
}

func (r *fix3MonitorRepo) GetByID(context.Context, int64) (*service.ChannelMonitor, error) {
	row := *r.row
	row.BodyOverride = maps.Clone(row.BodyOverride)
	row.ExtraHeaders = maps.Clone(row.ExtraHeaders)
	return &row, nil
}
func (r *fix3MonitorRepo) Update(_ context.Context, row *service.ChannelMonitor) error {
	r.row = row
	return nil
}
func (*fix3MonitorRepo) InsertHistoryBatch(context.Context, []*service.ChannelMonitorHistoryRow) error {
	return nil
}

type fix3MonitorRuntime struct{}

func (fix3MonitorRuntime) GetChannelMonitorRuntime(context.Context) service.ChannelMonitorRuntime {
	return service.ChannelMonitorRuntime{Enabled: true, Mode: service.ChannelMonitorModeV1}
}

type fix3Encryptor struct{}

func (fix3Encryptor) Encrypt(s string) (string, error) { return s, nil }
func (fix3Encryptor) Decrypt(s string) (string, error) { return s, nil }

type fix3UserRepo struct {
	service.UserRepository
	row *service.User
}

func (r *fix3UserRepo) GetByID(context.Context, int64) (*service.User, error) { return r.row, nil }

func (*fix3UserRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

type fix3Permissions struct{ raw []byte }

func (r fix3Permissions) Get(context.Context, int64) ([]byte, bool, error) { return r.raw, true, nil }
func (fix3Permissions) Upsert(context.Context, int64, []byte, int64) error { return nil }

type fix3Transport func(*http.Request) (*http.Response, error)

func (fn fix3Transport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestOperatorMonitorNetworkChangeDropsOldProbeBody(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		fresh       bool
	}{
		{"absent", "", false},
		{"null", `,"body_override":null`, false},
		{"empty_object", `,"body_override":{}`, false},
		{"masked", `,"body_override":"******"`, false},
		{"new_object", `,"body_override":{"fresh":"new-body"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type received struct{ host, body string }
			posts := make(chan received, 4)
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "body read failed", http.StatusBadRequest)
					return
				}
				posts <- received{host: r.Header.Get("X-Test-Original-Host"), body: string(body)}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"0"}]}]}`)
			}))
			t.Cleanup(receiver.Close)
			destination, err := url.Parse(receiver.URL)
			require.NoError(t, err)
			transport := &http.Transport{}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: fix3Transport(func(r *http.Request) (*http.Response, error) {
				forward := r.Clone(r.Context())
				forward.Header.Set("X-Test-Original-Host", r.URL.Host)
				forward.URL.Scheme, forward.URL.Host = destination.Scheme, destination.Host
				return transport.RoundTrip(forward)
			})}
			service.SwapOperatorMonitorHTTPClientsForTest(t, client)
			repo := &fix3MonitorRepo{row: &service.ChannelMonitor{ID: 10, Name: "monitor", Provider: "openai", APIMode: "responses", Endpoint: "https://203.0.113.10", APIKey: "old-key", PrimaryModel: "test", IntervalSeconds: 60, BodyOverrideMode: "merge", BodyOverride: map[string]any{"old_secret": "prior-body-secret"}}}
			monitor := service.NewChannelMonitorService(repo, fix3Encryptor{})
			monitor.SetRuntimeReader(fix3MonitorRuntime{})
			users := &fix3UserRepo{row: &service.User{ID: 7, Role: service.RoleOperator, Status: service.StatusActive}}
			perms := operatoraccess.Permissions{Modules: map[operatoraccess.Module]operatoraccess.Level{operatoraccess.ModuleChannels: operatoraccess.LevelManage}, Flags: map[operatoraccess.Flag]bool{operatoraccess.FlagAccountsUpstreamAddress: true}}
			raw, err := json.Marshal(perms.Wire())
			require.NoError(t, err)
			access := service.NewOperatorAccessService(fix3Permissions{raw}, users, nil, nil, nil, nil, nil, nil, nil, nil, monitor, nil, nil, nil, nil, nil, nil, nil)
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "unit-fix3-signing", ExpireHour: 1}}
			auth := service.NewAuthService(nil, users, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
			token, err := auth.GenerateToken(context.Background(), users.row)
			require.NoError(t, err)
			gin.SetMode(gin.TestMode)
			router := gin.New()
			g := router.Group("/api/v1/admin")
			g.Use(gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(users, nil, nil, nil), nil, nil, access)), middleware.AdminPermission())
			handler := adminhandler.NewChannelMonitorHandler(monitor)
			g.PUT("/channel-monitors/:id", handler.Update)
			g.POST("/channel-monitors/:id/run", handler.Run)
			do := func(method, path, body string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, "/api/v1/admin/channel-monitors/10"+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(rec, req)
				return rec
			}
			rec := do("PUT", "", `{"endpoint":"https://203.0.113.11","api_key":"new-key"`+tc.field+`}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			rec = do("POST", "/run", "{}")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, posts, 1, "the real RunCheck path must send a probe")
			post := <-posts
			require.Equal(t, "203.0.113.11", post.host)
			require.NotContains(t, post.body, "prior-body-secret")
			require.NotContains(t, post.body, "old_secret")
			if tc.fresh {
				require.Contains(t, post.body, `"fresh":"new-body"`)
				require.Equal(t, "merge", repo.row.BodyOverrideMode)
			} else {
				require.Equal(t, "off", repo.row.BodyOverrideMode)
				require.Empty(t, repo.row.BodyOverride)
			}
		})
	}
}
