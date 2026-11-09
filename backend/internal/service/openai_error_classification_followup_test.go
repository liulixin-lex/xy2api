//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/liulixin-lex/xy2api/internal/config"
)

// Review B T2: a 409 device_binding_required on a chained Messages compat turn.
func TestOpenAIDeviceBindingChainedMessagesSingleAttempt(t *testing.T) {
	mk := func() *http.Response {
		return &http.Response{StatusCode: http.StatusConflict,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{"error":{"message":"device binding required","type":"invalid_request_error","code":"device_binding_required"}}`))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{mk(), mk(), mk()}}
	svc := &OpenAIGatewayService{httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example/v1"}}
	svc.bindOpenAICompatSessionResponseID(context.Background(), nil, account, "stable-cache-key", "resp_prev")
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "stable-cache-key", "gpt-6-astra")
	var fo *UpstreamFailoverError
	t.Logf("upstream_requests=%d failover=%v err=%v", len(upstream.requests), errors.As(err, &fo), err)
	for i, b := range upstream.bodies {
		t.Logf("request[%d] previous_response_id=%q", i, gjson.GetBytes(b, "previous_response_id").String())
	}
	require.True(t, errors.As(err, &fo), "must fail over")
	require.Len(t, upstream.requests, 1, "account-level 409 must not be replayed on the same account")
}

func TestOpenAICompatStreamModelAvailability(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"chat", "messages"} {
		for _, shape := range []string{"top", "object", "failed"} {
			for _, after := range []bool{false, true} {
				name := path + "/" + shape
				if after {
					name += "/after"
				} else {
					name += "/before"
				}
				t.Run(name, func(t *testing.T) {
					payload := bareErrorPayload(shape, `"code":"model_not_found","message":"Requested resource absent","param":"model"`)
					frame := bareErrorFrame(payload)
					if shape == "failed" {
						frame = "event: response.failed\ndata: " + `{"type":"response.failed","response":{"error":{"code":"model_not_found","message":"Requested resource absent","param":"model"}}}` + "\n\n"
					}
					prefix := keepaliveStallCreated
					if after {
						prefix += keepaliveStallDelta1
					}
					upstream := &httpUpstreamRecorder{resp: bareErrorResponse(prefix + frame)}
					svc := compatStreamService(upstream, 0, 0, 0)
					repo := &openAIModelAvailabilityRepo{}
					svc.accountRepo = repo
					svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
					account := streamModelAccount(1, false, "https://relay.example/v1")
					svc.recordOpenAIAccountModelTransientFailure(account, "public", time.Now())
					c, rec := newOpenAIUpstreamErrorTestContext(t)
					state := resilience.NewRequestState()
					c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
					InstallDeliveryTracking(c, time.Now())
					var writes strings.Builder
					err := forwardBareErrorPath(svc, c, account, path, true, &writes)
					require.Error(t, err)
					var failure *UpstreamFailoverError
					if after {
						require.NotErrorAs(t, err, &failure)
						require.True(t, state.TurnFailed())
						require.Equal(t, 1, strings.Count(rec.Body.String(), `"error":`), rec.Body.String())
						require.NotContains(t, rec.Body.String(), "message_stop")
					} else {
						require.ErrorAs(t, err, &failure)
						require.True(t, failure.IsOpenAIModelUnavailable())
						require.True(t, failure.ShouldRetryNextAccount())
						require.False(t, failure.RetryableOnSameAccount)
						require.False(t, failure.ShouldReportAccountScheduleFailure())
						require.Equal(t, http.StatusNotFound, failure.StatusCode)
						require.Empty(t, rec.Body.String())
					}
					entry, ok := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "public", time.Now())
					require.True(t, ok)
					require.Equal(t, 2, entry.failureStreak)
					require.True(t, entry.blockUntil.After(time.Now()))
					require.Len(t, upstream.bodies, 1)
					require.Zero(t, repo.setErrorCalls)
					require.Zero(t, repo.tempCalls)
					require.Empty(t, repo.modelRateLimitCalls)
				})
			}
		}
	}
}

func TestOpenAIDeviceBindingClassificationPlatformBoundary(t *testing.T) {
	withResilienceSettings(t, nil)
	body := []byte(`{"error":{"code":"device_binding_required","message":"binding needed"}}`)
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformGemini, PlatformAnthropic, ""} {
		t.Run(platform, func(t *testing.T) {
			account := &Account{Platform: platform}
			category := opsHTTPResilienceCategory(409, nil, body, "", account)
			observed := opsResilienceCategory(&OpsUpstreamErrorEvent{Platform: platform, UpstreamStatusCode: 409, UpstreamResponseBody: string(body)})
			require.Equal(t, category, observed)
			if platform == PlatformOpenAI {
				require.Equal(t, string(resilience.CategoryAuth), category)
			} else {
				require.NotEqual(t, string(resilience.CategoryAuth), category)
			}
		})
	}
	require.NotEqual(t, resilience.CategoryAuth, classifyUpstreamHTTP(409, nil, body, "").Category)
}

func TestOpenAIDeviceBindingWSHandshake(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, code := range []string{"device_binding_required", "conflict"} {
		t.Run(code, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "websocket", strings.ToLower(r.Header.Get("Upgrade")))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":{"code":"`+code+`","message":"binding needed"}}`)
			}))
			defer server.Close()
			cfg := rawChatCompletionsTestConfig()
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.RetryBackoffInitialMS = 1
			cfg.Gateway.OpenAIWS.RetryBackoffMaxMS = 1
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			upstream := &httpUpstreamRecorder{}
			repo := &openAIModelAvailabilityRepo{}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, accountRepo: repo, rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil)}
			account := streamModelAccount(1, false, server.URL+"/v1")
			account.Extra = map[string]any{"responses_websockets_v2_enabled": true}
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"gpt-5.5","stream":false,"input":"hello"}`))
			require.Error(t, err)
			require.Empty(t, upstream.bodies, "handshake failure must not silently fall back to HTTP")
			var failure *UpstreamFailoverError
			if code == "device_binding_required" {
				require.EqualValues(t, 1, calls.Load())
				require.ErrorAs(t, err, &failure)
				require.True(t, failure.IsCredentialFailure())
				require.True(t, failure.ShouldRetryNextAccount())
				require.False(t, failure.RetryableOnSameAccount)
				require.Empty(t, rec.Body.String())
				require.Equal(t, 1, repo.setErrorCalls)
				require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
			} else {
				require.EqualValues(t, openAIWSReconnectRetryLimit+1, calls.Load())
				require.NotErrorAs(t, err, &failure)
				require.Zero(t, repo.setErrorCalls)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			}
		})
	}
}

func TestInference404AnthropicEnvelopeRequiresEvidence(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{}}`,
		`{"type":"error","error":{"code":"","type":null,"message":"  "}}`,
		`{"type":"error","error":{"message":"File not found: file_missing"}}`,
		`{"type":"error","error":{"resource":"file_missing"}}`,
		`{"type":"error","error":{"type":"error"}}`,
		`{"type":"error"}`,
	} {
		t.Run(body, func(t *testing.T) {
			require.False(t, isGenericInferenceNotFound([]byte(body)))
			require.Equal(t, resilience.CategoryTransient, classifyInferenceUpstreamHTTP(404, nil, []byte(body), "").Category)
		})
	}
	for _, body := range []string{
		`{"type":"error","error":{"code":"not_found"}}`,
		`{"type":"error","error":{"type":"not_found_error"}}`,
		`{"type":"error","error":{"message":"Not Found"}}`,
	} {
		require.True(t, isGenericInferenceNotFound([]byte(body)))
		require.Equal(t, resilience.CategoryModelUnsupported, classifyInferenceUpstreamHTTP(404, nil, []byte(body), "").Category)
	}
}
