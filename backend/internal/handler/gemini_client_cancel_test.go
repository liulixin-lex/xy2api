//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	middleware "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

// clientCancelUpstream 模拟上游尚未响应时客户端断开：Do 期间取消请求 context，
// 并按 net/http 的形态返回包裹 context.Canceled 的 *url.Error。
type clientCancelUpstream struct {
	service.HTTPUpstream
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (u *clientCancelUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	u.cancel()
	return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: context.Canceled}
}

type geminiClientCancelFixture struct {
	handler  *GatewayHandler
	group    *service.Group
	apiKey   *service.APIKey
	upstream *clientCancelUpstream
	ctx      context.Context
}

func newGeminiClientCancelFixture(t *testing.T) *geminiClientCancelFixture {
	t.Helper()
	groupID := int64(9200)
	accountID := int64(9201)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive}
	account := &service.Account{
		ID:            accountID,
		Platform:      service.PlatformGemini,
		Type:          service.AccountTypeAPIKey,
		Credentials:   map[string]any{"api_key": "test-key"},
		Status:        service.StatusActive,
		Schedulable:   true,
		Concurrency:   1,
		Priority:      1,
		AccountGroups: []service.AccountGroup{{AccountID: accountID, GroupID: groupID}},
	}
	h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
	t.Cleanup(cleanup)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	upstream := &clientCancelUpstream{cancel: cancel}
	h.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, upstream, nil, &config.Config{})

	apiKey := &service.APIKey{
		ID: 9202, UserID: 9203, GroupID: &groupID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9203, Concurrency: 10, Balance: 100},
	}
	return &geminiClientCancelFixture{handler: h, group: group, apiKey: apiKey, upstream: upstream, ctx: ctx}
}

// serve 经 ops 错误日志中间件执行请求，返回响应与 ops 队列中的条目数。
func (f *geminiClientCancelFixture) serve(t *testing.T, route, path, body string, call func(*gin.Context)) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	setupOpsErrorLogTestQueue(t, 4)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST(route, func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), f.apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.apiKey.UserID, Concurrency: 10})
		call(c)
	})

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(f.ctx, ctxkey.Group, f.group))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, OpsErrorLogQueueLength()
}

// 原生入口：上游响应前客户端断开，响应未提交，标记 499；纯客户端取消不落 ops 错误日志。
func TestGeminiV1BetaModels_ClientCancelBeforeUpstreamResponseMarks499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newGeminiClientCancelFixture(t)

	rec, opsQueued := f.serve(t,
		"/v1beta/models/*modelAction",
		"/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
		f.handler.GeminiV1BetaModels,
	)

	require.Equal(t, int32(1), f.upstream.calls.Load())
	require.Equal(t, statusClientClosedRequest, rec.Code)
	require.Zero(t, rec.Body.Len())
	require.Zero(t, opsQueued)
}

// Chat Completions 兼容入口（Gemini 分组）：同一场景标记 499，不再补写 502。
func TestGatewayChatCompletions_GeminiClientCancelBeforeUpstreamResponseMarks499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newGeminiClientCancelFixture(t)

	rec, opsQueued := f.serve(t,
		"/v1/chat/completions",
		"/v1/chat/completions",
		`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`,
		f.handler.ChatCompletions,
	)

	require.Equal(t, int32(1), f.upstream.calls.Load())
	require.Equal(t, statusClientClosedRequest, rec.Code)
	require.Zero(t, rec.Body.Len())
	require.Zero(t, opsQueued)
}

type geminiStallUpstream struct {
	service.HTTPUpstream
	body io.ReadCloser
}

func (u *geminiStallUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: u.body}, nil
}

func TestGatewayGeminiStreamStallWritesOneTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withHandlerResilience(t, func(s *resilience.Settings) { s.Stall.TextStallSeconds = 1 })
	for _, protocol := range []string{"messages", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			f := newGeminiClientCancelFixture(t)
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			written := make(chan struct{})
			go func() {
				defer close(written)
				_, _ = io.WriteString(writer, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n\n")
			}()
			f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil,
				&geminiStallUpstream{body: reader}, nil, &config.Config{})
			path, call := EndpointMessages, f.handler.Messages
			if protocol == "chat" {
				path, call = "/v1/chat/completions", f.handler.ChatCompletions
			}
			rec, _ := f.serve(t, path, path,
				`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":true}`, call)
			<-written
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, body, "hello")
			require.Equal(t, 1, strings.Count(body, `"type":"stream_timeout"`), body)
			require.NotContains(t, body, "Upstream request failed")
			if protocol == "messages" {
				require.Equal(t, 1, strings.Count(body, "event: error"), body)
			} else {
				require.Equal(t, 1, strings.Count(body, "data: [DONE]"), body)
			}
		})
	}
}
