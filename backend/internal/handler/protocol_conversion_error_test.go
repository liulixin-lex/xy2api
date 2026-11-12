//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIProtocolConversionRejectsBeforeUpstreamAndHealthReporting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := service.NewSettingService(&contentModerationHandlerSettingRepo{values: map[string]string{}}, &config.Config{})
	withHandlerResilience(t, nil)
	cases := []struct{ name, platform, endpoint, body, field string }{
		{"chat_stop", service.PlatformOpenAI, EndpointChatCompletions, `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stop":["END"],"stream":true}`, "stop"},
		{"messages_stop", service.PlatformOpenAI, EndpointMessages, `{"model":"gpt-5.4","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stop_sequences":["END"],"stream":false}`, "stop"},
		{"legacy_responses_stop", service.PlatformOpenAI, EndpointResponses, `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stop":["END"],"stream":false}`, "stop"},
		{"grok_bridge_format", service.PlatformGrok, EndpointChatCompletions, `{"model":"grok-4.7","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"not_a_format"},"prompt_cache_key":"fixture-session","stream":true}`, "response_format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &compatWriteDisconnectUpstream{}
			repo := &grokCredentialHandlerRepo{accounts: []service.Account{{ID: 801, Platform: tc.platform, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-key", "base_url": "https://upstream.example", "api_protocol": service.APIProtocolResponses}, Extra: map[string]any{"openai_responses_supported": true}}}}
			if tc.platform == service.PlatformGrok {
				repo.accounts[0].Type = service.AccountTypeOAuth
				repo.accounts[0].Credentials["access_token"] = "fixture-access"
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
				service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, settings, nil)
			cache := &concurrencyCacheMock{
				acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
				acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
			}
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
			groupID := int64(901)
			key := &service.APIKey{ID: 902, GroupID: &groupID, User: &service.User{ID: 903, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: tc.platform, Status: service.StatusActive, AllowMessagesDispatch: true}}
			router := gin.New()
			var state *resilience.RequestState
			router.POST(tc.endpoint, func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), key)
				c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.User.ID, Concurrency: 1})
				switch tc.endpoint {
				case EndpointChatCompletions:
					h.ChatCompletions(c)
				case EndpointMessages:
					h.Messages(c)
				default:
					h.Responses(c)
				}
				state = resilience.StateFrom(c.Request.Context())
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.endpoint, strings.NewReader(tc.body)))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), tc.field)
			require.Zero(t, upstream.calls)
			require.Empty(t, repo.setErrorIDs)
			require.Empty(t, repo.setTempIDs)
			require.Empty(t, repo.rateLimitIDs)
			require.False(t, state.AccountFailed(801))
			require.Equal(t, "terminal_rejection", *state.RecoveryOutcome())
		})
	}
}

func TestGatewayNonClaudeConversionRejectsWithoutUpstream(t *testing.T) {
	for _, platform := range []string{service.PlatformGemini, service.PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			f := newHandlerCompatFixture(t)
			f.group.Platform = platform
			f.account.Platform = platform
			f.account.Extra = nil
			model := "gemini-2.5-flash"
			if platform == service.PlatformGemini {
				f.handler.geminiCompatService = service.NewGeminiMessagesCompatService(nil, nil, nil, nil, nil, nil, nil, nil, &config.Config{})
			} else {
				model = "claude-sonnet-4-5"
				f.account.Type = service.AccountTypeOAuth
				f.account.Credentials["model_mapping"] = map[string]any{model: model}
				f.handler.antigravityGatewayService = &service.AntigravityGatewayService{}
			}
			body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"unsupported_format"},"stream":false}`, model))
			rec := httptest.NewRecorder()
			f.call("chat", string(body), rec)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Contains(t, rec.Body.String(), "response_format")
			require.Empty(t, f.wire)
			require.Zero(t, f.accounts.penalties)
		})
	}
}

func TestUnsupportedParameterErrorContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			err := fmt.Errorf("convert request: %w", &apicompat.UnsupportedConversionError{Field: "n", Reason: "only 1 is supported"})
			require.True(t, handleUnsupportedProtocolConversion(c, err, protocol, false))
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, "unsupported_parameter", gjson.Get(rec.Body.String(), "error.code").String())
			require.Equal(t, "n", gjson.Get(rec.Body.String(), "error.param").String())
			require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "n")
		})
	}
}

func TestAnthropicConversionParameterErrorsBeforeUpstream(t *testing.T) {
	for _, entry := range []string{"chat", "responses"} {
		for _, tc := range []struct{ model, field, fields string }{
			{"claude-sonnet-4-5", "n", `"n":2`},
			{"claude-sonnet-5-5", "n", `"n":2`},
			{"claude-sonnet-5-5", "temperature", `"temperature":0.7`},
			{"claude-sonnet-5-5", "tool_choice", `"tool_choice":"required"`},
			{"claude-sonnet-5-5", "reasoning", `"reasoning":{"effort":"invalid"},"reasoning_effort":"invalid"`},
		} {
			t.Run(entry+tc.model+tc.field, func(t *testing.T) {
				f := newHandlerCompatFixture(t)
				f.account.Extra = nil
				input := `"input":"hi"`
				if entry == "chat" {
					input = `"messages":[{"role":"user","content":"hi"}]`
				}
				body := fmt.Sprintf(`{"model":%q,%s,%s}`, tc.model, input, tc.fields)
				rec := httptest.NewRecorder()
				f.call(entry, body, rec)
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
				require.Equal(t, "unsupported_parameter", gjson.Get(rec.Body.String(), "error.code").String())
				require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), tc.field)
				require.Empty(t, f.wire)
				require.Zero(t, f.accounts.penalties)
			})
		}
	}
}

func TestUnsupportedParameterStreamErrorContract(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			require.True(t, handleUnsupportedProtocolConversion(c, &apicompat.UnsupportedConversionError{Field: "n", Reason: "only 1 is supported"}, protocol, true))
			require.Contains(t, rec.Body.String(), `"code":"unsupported_parameter"`)
			if protocol == "responses" {
				require.Contains(t, rec.Body.String(), `"message":"unsupported protocol conversion at n: only 1 is supported"`)
			} else {
				require.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
			}
		})
	}
}
