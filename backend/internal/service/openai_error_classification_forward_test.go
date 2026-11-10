//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func forwardErrorClassificationRoute(svc *OpenAIGatewayService, c *gin.Context, account *Account, route string) error {
	var err error
	switch {
	case strings.HasPrefix(route, "chat"):
		_, err = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(`{"model":"requested-model","messages":[{"role":"user","content":"hello"}]}`), "", "")
	case strings.HasPrefix(route, "messages"):
		_, err = svc.ForwardAsAnthropic(c.Request.Context(), c, account, []byte(`{"model":"requested-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`), "", "")
	default:
		_, err = svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"requested-model","stream":false,"instructions":"test","input":"hello"}`))
	}
	return err
}

func TestOpenAIDeviceBindingRequiredForwardRoutes(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "passthrough", "chat-responses", "chat-raw", "messages-responses", "messages-raw"} {
		for _, binding := range []bool{false, true} {
			t.Run(route+map[bool]string{false: "/ordinary409", true: "/device_binding"}[binding], func(t *testing.T) {
				body := `{"error":{"code":"conflict","message":"conflict"}}`
				if binding {
					body = `{"error":{"code":"device_binding_required","message":"device binding required"}}`
				}
				upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(http.StatusConflict, body)}
				repo := &openAIModelAvailabilityRepo{}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
					rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
				account := openAIFailoverCachedBodyTestAccount(941, "fixture", nil)
				account.Extra = map[string]any{"openai_passthrough": route == "passthrough", "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
				if strings.HasSuffix(route, "raw") {
					account.Type = AccountTypeAPIKey
					account.Credentials["api_key"] = "test-key"
					account.Extra["openai_responses_mode"] = "force_chat_completions"
				}
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				err := forwardErrorClassificationRoute(svc, c, account, route)
				require.Error(t, err)
				require.Len(t, upstream.bodies, 1)
				expectedEndpoint := "/responses"
				if strings.HasSuffix(route, "raw") {
					expectedEndpoint = "/chat/completions"
				}
				require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, expectedEndpoint), upstream.lastReq.URL.Path)
				var failure *UpstreamFailoverError
				if binding {
					require.ErrorAs(t, err, &failure)
					require.True(t, failure.IsCredentialFailure())
					require.True(t, failure.ShouldRetryNextAccount())
					require.False(t, failure.RetryableOnSameAccount)
					require.Equal(t, http.StatusBadGateway, failure.ClientStatusCode)
					require.Equal(t, resilience.CategoryAuth, ClassifyFailoverError(failure).Category)
					require.Equal(t, string(resilience.CategoryAuth), opsHTTPResilienceCategory(http.StatusConflict, nil, []byte(body), "", account))
					require.Equal(t, 1, repo.setErrorCalls)
					require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
					require.Empty(t, rec.Body.String())
				} else {
					require.NotErrorAs(t, err, &failure)
					require.Zero(t, repo.setErrorCalls)
					require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
					require.NotEmpty(t, rec.Body.String())
				}
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
			})
		}
	}
}

func TestGrokCustomGeneric404TemporaryRuleAcrossEntrypoints(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "chat", "messages"} {
		t.Run(route, func(t *testing.T) {
			repo := &openAIModelAvailabilityRepo{}
			upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(404, `{"message":"not found"}`)}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
				rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
			account := openAIModelNotFoundTempAccount()
			account.Platform = PlatformGrok
			account.Credentials["api_key"] = "test-key"
			account.Credentials["base_url"] = "https://relay.example/v1"
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(404)}
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			err := forwardErrorClassificationRoute(svc, c, account, route)
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.True(t, failure.ShouldRetryNextAccount())
			require.Empty(t, rec.Body.String())
			require.Len(t, upstream.bodies, 1)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Len(t, repo.modelRateLimitCalls, 1)
			call := repo.modelRateLimitCalls[0]
			require.Equal(t, "requested-model", call.scope)
			require.WithinDuration(t, time.Now().Add(10*time.Minute), call.resetAt, 5*time.Second)
			var state TempUnschedState
			require.NoError(t, json.Unmarshal([]byte(call.reason), &state))
			require.Equal(t, "not found", state.MatchedKeyword)
		})
	}
}

func TestGeminiGeneric404PolicyAcrossEntrypoints(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, route := range []string{"native", "chat", "messages"} {
		for _, policy := range []string{"default", "custom", "temporary"} {
			t.Run(route+"/"+policy, func(t *testing.T) {
				repo := &openAIModelAvailabilityRepo{}
				svc, _ := newGeminiSkippedWriteService(404, `{"message":"not found"}`)
				svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				svc.accountRepo = repo
				account := openAIModelNotFoundTempAccount()
				account.Platform = PlatformGemini
				account.Credentials["api_key"] = "test-key"
				account.Credentials["temp_unschedulable_enabled"] = policy == "temporary"
				account.Credentials["custom_error_codes_enabled"] = policy != "default"
				account.Credentials["custom_error_codes"] = []any{float64(404)}
				c, rec := newGeminiNativeTestContext(t)
				var err error
				switch route {
				case "native":
					_, err = svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
				case "chat":
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}]}`))
				case "messages":
					_, err = svc.Forward(context.Background(), c, account, []byte(`{"model":"gemini-2.5-flash","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`))
				}
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.False(t, failure.RetryableOnSameAccount)
				require.Empty(t, rec.Body.String())
				require.Zero(t, repo.tempCalls)
				if policy == "custom" {
					require.Equal(t, 1, repo.setErrorCalls)
					require.Empty(t, repo.modelRateLimitCalls)
				} else {
					require.Zero(t, repo.setErrorCalls)
					require.Len(t, repo.modelRateLimitCalls, 1)
					call := repo.modelRateLimitCalls[0]
					require.Equal(t, "gemini-2.5-flash", call.scope)
					if policy == "temporary" {
						require.WithinDuration(t, time.Now().Add(10*time.Minute), call.resetAt, 5*time.Second)
						var state TempUnschedState
						require.NoError(t, json.Unmarshal([]byte(call.reason), &state))
						require.Equal(t, "not found", state.MatchedKeyword)
					}
				}
			})
		}
	}
}
