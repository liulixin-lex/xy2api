//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFinalB04BufferedBillingEligibility(t *testing.T) {
	for _, entry := range []struct {
		name, body string
		native     bool
	}{
		{"chat", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"fixture"}],"stream":false}`, false},
		{"responses", `{"model":"claude-opus-4-8","input":"fixture","stream":false}`, false},
		{"native_chat", `{"model":"k3","messages":[{"role":"user","content":"fixture"}],"stream":false}`, true},
		{"native_responses", `{"model":"k3","input":"fixture","stream":false}`, true},
	} {
		for _, tc := range []struct {
			name, wire           string
			billed, success      bool
			input, output, cache int
		}{
			{"error_without_usage", strings.ReplaceAll(gwUsageOverloaded, "overloaded_error", "invalid_request_error"), false, false, 0, 0, 0},
			{"content_without_usage", strings.Replace(gwUsageStart, `,"usage":{"input_tokens":100,"cache_read_input_tokens":40}`, "", 1) + gwUsageOverloaded, false, false, 0, 0, 0},
			{"partial_usage_error", gwUsageStart + gwUsageOverloaded, true, false, 100, 0, 40},
			{"cache_only_error", strings.Replace(gwUsageStart, `"input_tokens":100,`, "", 1) + gwUsageOverloaded, true, false, 0, 0, 40},
			{"success", scopeAnthropicSSE, true, true, 1, 1, 0},
		} {
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				withHandlerResilience(t, nil)
				var bills []*service.UsageLog
				var unbilled []*service.GatewayUnbilledRequest
				var response *httptest.ResponseRecorder
				var id string
				if entry.native {
					repo := &requestRecordRepo{}
					up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
						id = resilience.StateFrom(req.Context()).GatewayRequestID()
						require.True(t, strings.HasSuffix(req.URL.Path, "/v1/messages"), req.URL.Path)
						return captureResponse(200, tc.wire, true), nil
					}}
					account := service.Account{ID: 9901, Platform: service.PlatformKimi, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{"api_key": "fixture", "api_protocol": service.APIProtocolAnthropic, "api_base_urls": map[string]any{service.APIProtocolAnthropic: "https://capture.example"}}}
					h, _, _ := captureFixture(t, account, up, captureOptions{usage: repo, repo: &openAIWSUsageHandlerAccountRepoStub{account: account}})
					path := "/v1/responses"
					if entry.name == "native_chat" {
						path = "/v1/chat/completions"
					}
					response = httptest.NewRecorder()
					c, _ := gin.CreateTestContext(response)
					group := &service.Group{ID: 4201, Platform: service.PlatformKimi, Status: service.StatusActive, Hydrated: true}
					key := &service.APIKey{ID: 1801, UserID: 1701, GroupID: &group.ID, Group: group, User: &service.User{ID: 1701, Status: service.StatusActive}}
					c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(entry.body))
					ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
					c.Request = c.Request.WithContext(resilience.WithRequestState(ctx, resilience.NewRequestState()))
					c.Set(string(middleware.ContextKeyAPIKey), key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1701, Concurrency: 2})
					service.InstallDeliveryTracking(c, time.Now())
					if entry.name == "native_chat" {
						h.ChatCompletions(c)
					} else {
						h.Responses(c)
					}
					bills, unbilled = repo.bills, repo.unbilled
					require.Equal(t, 1, up.calls)
				} else {
					f := newHandlerCompatFixture(t)
					f.response = func(uint64) string { return tc.wire }
					response = httptest.NewRecorder()
					c := f.call(entry.name, entry.body, response)
					id = resilience.StateFrom(c.Request.Context()).GatewayRequestID()
					bills, unbilled = f.usage.logs, f.usage.unbilled
				}
				require.NotEmpty(t, id)
				t.Logf("status=%d bills=%d unbilled=%d", response.Code, len(bills), len(unbilled))
				if tc.success {
					require.Equal(t, 200, response.Code)
				} else {
					require.Equal(t, 502, response.Code)
				}
				if !tc.billed {
					require.Empty(t, bills, "a failure without usage must not create a bill")
					require.Len(t, unbilled, 1)
					row := unbilled[0]
					require.Equal(t, id, row.RequestID)
					require.Equal(t, resilience.RequestFailed, row.Result)
					require.Equal(t, new(false), row.UsageObserved)
					require.Equal(t, new("missing"), row.UsageSource)
					require.Equal(t, new(true), row.TerminalEventSeen)
					require.Equal(t, new(true), row.TerminalEventValid)
					require.Equal(t, new("sse"), row.ResponseBodyType)
					return
				}
				require.Empty(t, unbilled)
				require.Len(t, bills, 1)
				row := bills[0]
				require.Equal(t, new(id), row.GatewayRequestID)
				require.Equal(t, tc.input, row.InputTokens)
				require.Equal(t, tc.output, row.OutputTokens)
				require.Equal(t, tc.cache, row.CacheReadTokens)
				require.Equal(t, new(true), row.UsageObserved)
				require.Equal(t, new("upstream_sse_progressive"), row.UsageSource)
			})
		}
	}
}
