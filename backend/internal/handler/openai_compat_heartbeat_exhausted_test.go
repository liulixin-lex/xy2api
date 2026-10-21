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
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type compatHeartbeatFailureUpstream struct {
	service.HTTPUpstream
	c   *gin.Context
	t   *testing.T
	ids []int64
}

func (u *compatHeartbeatFailureUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.ids = append(u.ids, id)
	if len(u.ids) == 1 {
		// Simulate the staged reader's public write plus its stream-heartbeat
		// accounting. Do not install the independent keepalive rewriter: it
		// would hide a missing streamStarted propagation in the handler.
		u.c.Header("Content-Type", "text/event-stream")
		n, err := u.c.Writer.WriteString(":\n\n")
		require.NoError(u.t, err)
		u.c.Set("openai_stream_keepalive_bytes", n)
		recordGatewayStreamHeartbeat(u.c, n)
		u.c.Writer.Flush()
	}
	return &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"upstream failed"}}`))}, nil
}

func TestOpenAICompatibleHeartbeatExhaustionWritesProtocolTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, nil)
	for _, endpoint := range []string{EndpointChatCompletions, "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := &compatHeartbeatFailureUpstream{t: t}
			repo := &grokCredentialHandlerRepo{}
			for _, id := range []int64{801, 802} {
				repo.accounts = append(repo.accounts, service.Account{
					ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example"},
				})
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.MaxAccountSwitches = 1
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
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
			router := gin.New()
			router.POST(endpoint, func(c *gin.Context) {
				upstream.c = c
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
				if endpoint == EndpointChatCompletions {
					h.ChatCompletions(c)
				} else {
					h.Messages(c)
				}
			})
			req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.ElementsMatch(t, []int64{801, 802}, upstream.ids, "both accounts must fail after the first heartbeat")
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.True(t, strings.HasPrefix(body, ":\n\n"), body)
			if endpoint == EndpointChatCompletions {
				require.Contains(t, body, `data: {"error":`)
				require.Equal(t, 1, strings.Count(body, "data: [DONE]\n\n"))
				require.True(t, strings.HasSuffix(body, "data: [DONE]\n\n"), body)
			} else {
				require.Equal(t, 1, strings.Count(body, "event: error\n"))
				require.Contains(t, body, `"type":"error"`)
			}
		})
	}
}
