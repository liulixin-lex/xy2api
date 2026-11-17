//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func n12RequireBillingMetadata(t *testing.T, bills []*service.UsageLog, unbilled []*service.GatewayUnbilledRequest) {
	t.Helper()
	require.Len(t, bills, 1)
	require.Empty(t, unbilled)
	require.NotNil(t, bills[0].GatewayRequestID)
	require.NotEmpty(t, *bills[0].GatewayRequestID)
	require.NotNil(t, bills[0].Result)
	require.EqualValues(t, resilience.RequestSuccess, *bills[0].Result)
	require.Equal(t, resilience.GatewayRequestScopeText, bills[0].RequestScope)
}

func TestN12ImageEditBillingMetadata(t *testing.T) {
	withHandlerResilience(t, nil)
	repo := &requestRecordRepo{}
	up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
		require.Equal(t, "/v1/images/edits", req.URL.Path)
		return captureResponse(200, `{"data":[{"b64_json":"ZmFrZQ=="}]}`, false), nil
	}}
	h, _, router := captureFixture(t, service.Account{}, up, captureOptions{usage: repo})
	router.POST("/v1/images/edits", h.Images)
	response := captureRequest(router, "/v1/images/edits", `{"model":"gpt-image-1","prompt":"fixture","images":[{"image_url":"https://example.test/image.png"}]}`)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Len(t, repo.bills, 1)
	require.Empty(t, repo.unbilled)
	require.NotNil(t, repo.bills[0].GatewayRequestID)
	require.NotEmpty(t, *repo.bills[0].GatewayRequestID)
	require.NotNil(t, repo.bills[0].Result)
	require.EqualValues(t, resilience.RequestSuccess, *repo.bills[0].Result)
	require.Equal(t, resilience.GatewayRequestScopeImage, repo.bills[0].RequestScope)
}

func TestN12ExistingOpenAIEntriesCarryBillingMetadata(t *testing.T) {
	for _, rawChat := range []bool{false, true} {
		mode := "responses_upstream"
		if rawChat {
			mode = "chat_upstream"
		}
		for _, entry := range []string{"responses", "chat/completions", "messages"} {
			t.Run(mode+"/"+entry, func(t *testing.T) {
				withHandlerResilience(t, nil)
				repo := &requestRecordRepo{}
				account := service.Account{}
				if rawChat {
					account.Extra = map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)}
				}
				up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
					if rawChat {
						require.Equal(t, "/v1/chat/completions", req.URL.Path)
						return captureResponse(200, `{"id":"chat-fixture","object":"chat.completion","model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, false), nil
					}
					return captureResponse(200, captureSSE(), true), nil
				}}
				_, _, router := captureFixture(t, account, up, captureOptions{usage: repo})
				response := captureRequest(router, "/v1/"+entry, `{"model":"gpt-5.1","input":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`)
				require.Equal(t, 200, response.Code, response.Body.String())
				n12RequireBillingMetadata(t, repo.bills, repo.unbilled)
			})
		}
	}
}

func TestN12ExistingGeminiEntriesCarryBillingMetadata(t *testing.T) {
	for _, entry := range []string{"chat", "responses", "messages", "native"} {
		t.Run(entry, func(t *testing.T) {
			withHandlerResilience(t, nil)
			f := newHandlerCompatFixture(t)
			f.group.Platform, f.account.Platform = service.PlatformGemini, service.PlatformGemini
			f.account.Extra = nil
			if entry == "responses" {
				// The baseline Responses bridge speaks Messages even for a Gemini
				// group backed by a compatible intermediary; preserve that routing.
				f.response = func(uint64) string {
					return strings.ReplaceAll(scopeAnthropicSSE, "claude-opus-4-8", "gemini-2.5-flash")
				}
			} else {
				f.account.Credentials = map[string]any{"api_key": "fixture", "base_url": "https://gemini.example"}
			}
			up := &captureUpstream{reply: func(_ int, req *http.Request, _ []byte) (*http.Response, error) {
				body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
				stream := strings.Contains(req.URL.Path, "streamGenerateContent")
				if stream {
					body = "data: " + body + "\n\n"
				}
				return captureResponse(200, body, stream), nil
			}}
			f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, up, nil, &config.Config{})
			response := httptest.NewRecorder()
			if entry == "native" {
				router := gin.New()
				router.POST("/v1beta/models/*modelAction", func(c *gin.Context) {
					c.Set(string(middleware.ContextKeyAPIKey), f.key)
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: f.key.UserID, Concurrency: 10})
					f.handler.GeminiV1BetaModels(c)
				})
				response = captureRequest(router, "/v1beta/models/gemini-2.5-flash:generateContent", `{"contents":[{"role":"user","parts":[{"text":"fixture"}]}]}`)
			} else {
				f.call(entry, `{"model":"gemini-2.5-flash","input":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":32}`, response)
			}
			require.Equal(t, 200, response.Code, response.Body.String())
			if entry == "responses" {
				require.Len(t, f.wire, 1)
				require.Zero(t, up.calls)
			} else {
				require.Equal(t, 1, up.calls)
			}
			n12RequireBillingMetadata(t, f.usage.logs, f.usage.unbilled)
		})
	}
}
