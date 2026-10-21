//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// A canceled transport and a canceled staged reader return the same contract:
// nil result plus an error wrapping context.Canceled. Exercise the actual
// handler reporting boundary without depending on a particular stream reader.
func TestOpenAICompatibleClientCancelDoesNotReportAccountFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(&contentModerationHandlerSettingRepo{values: map[string]string{}}, &config.Config{})
	original, err := settings.GetAllSettings(context.Background())
	require.NoError(t, err)
	enabled := *original
	enabled.OpenAIAdvancedSchedulerEnabled = true
	require.NoError(t, settings.UpdateSettings(context.Background(), &enabled))
	t.Cleanup(func() { require.NoError(t, settings.UpdateSettings(context.Background(), original)) })
	withHandlerResilience(t, nil)
	for _, endpoint := range []string{EndpointChatCompletions, "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			upstream := &clientCancelUpstream{cancel: cancel}
			repo := &grokCredentialHandlerRepo{accounts: []service.Account{{
				ID: 801, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"},
			}}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			gateway := service.NewOpenAIGatewayService(
				repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billing, upstream,
				&service.DeferredService{}, nil, nil, nil, nil, nil, settings, nil,
			)
			cache := &concurrencyCacheMock{
				acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			groupID := int64(901)
			key := &service.APIKey{ID: 902, GroupID: &groupID,
				User:  &service.User{ID: 903, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
			}
			setupOpsErrorLogTestQueue(t, 4)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
			var state *resilience.RequestState
			router.POST(endpoint, func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
				if endpoint == EndpointChatCompletions {
					h.ChatCompletions(c)
				} else {
					h.Messages(c)
				}
				state = resilience.StateFrom(c.Request.Context())
				require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
			})
			req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, int32(1), upstream.calls.Load())
			require.Zero(t, gateway.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount, "client cancellation must not report an account result")
			require.NotNil(t, state)
			require.False(t, state.TurnFailed())
			require.False(t, state.AccountFailed(801))
			require.Equal(t, statusClientClosedRequest, rec.Code)
			require.Empty(t, rec.Body.String())
			require.Zero(t, OpsErrorLogQueueLength(), "pure client cancellation is not an upstream ops error")
		})
	}
}

// A write error is a downstream disconnect even before HTTP cancellation arrives.
type compatDisconnectedWriter struct{ gin.ResponseWriter }

func (*compatDisconnectedWriter) Write([]byte) (int, error)       { return 0, io.ErrClosedPipe }
func (*compatDisconnectedWriter) WriteString(string) (int, error) { return 0, io.ErrClosedPipe }

type compatWriteDisconnectUpstream struct {
	service.HTTPUpstream
	calls int
}

func (u *compatWriteDisconnectUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"content\"}\n\n"))}, nil
}

func TestOpenAICompatibleWriteDisconnectDoesNotReportAccountFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(&contentModerationHandlerSettingRepo{values: map[string]string{}}, &config.Config{})
	original, err := settings.GetAllSettings(context.Background())
	require.NoError(t, err)
	enabled := *original
	enabled.OpenAIAdvancedSchedulerEnabled = true
	require.NoError(t, settings.UpdateSettings(context.Background(), &enabled))
	t.Cleanup(func() { require.NoError(t, settings.UpdateSettings(context.Background(), original)) })
	withHandlerResilience(t, nil)
	for _, endpoint := range []string{EndpointChatCompletions, "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx := context.Background()
			upstream := &compatWriteDisconnectUpstream{}
			repo := &grokCredentialHandlerRepo{accounts: []service.Account{{
				ID: 801, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"},
			}}}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			gateway := service.NewOpenAIGatewayService(
				repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billing, upstream,
				&service.DeferredService{}, nil, nil, nil, nil, nil, settings, nil,
			)
			cache := &concurrencyCacheMock{
				acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			groupID := int64(901)
			key := &service.APIKey{ID: 902, GroupID: &groupID,
				User:  &service.User{ID: 903, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
			}
			setupOpsErrorLogTestQueue(t, 4)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)))
			var state *resilience.RequestState
			router.POST(endpoint, func(c *gin.Context) {
				c.Writer = &compatDisconnectedWriter{ResponseWriter: c.Writer}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
				if endpoint == EndpointChatCompletions {
					h.ChatCompletions(c)
				} else {
					h.Messages(c)
				}
				state = resilience.StateFrom(c.Request.Context())
				require.False(t, c.GetBool(ginKeyResilienceTurnCounted))
			})
			req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, 1, upstream.calls)
			require.NoError(t, ctx.Err(), "write failure must be handled even while request context is live")
			require.Zero(t, gateway.SnapshotOpenAIAccountSchedulerMetrics().RuntimeStatsAccountCount, "client write failure must not report an account result")
			require.NotNil(t, state)
			require.False(t, state.TurnFailed())
			require.False(t, state.AccountFailed(801))

			require.Empty(t, rec.Body.String())
			require.Zero(t, OpsErrorLogQueueLength(), "client write failure is not an upstream ops error")
		})
	}
}
