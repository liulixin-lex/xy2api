//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const inference404NginxPage = `<html>
<head><title>404 Not Found</title></head>
<body>
<center><h1>404 Not Found</h1></center>
<hr><center>nginx</center>
</body>
</html>`

func TestInference404AnthropicForwardSwitchesAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		for _, sample := range []struct{ name, body string }{
			{"empty", ""},
			{"nginx page", inference404NginxPage},
			{"top-level generic JSON", `{"message":"Not Found"}`},
		} {
			t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough]+"/"+sample.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				upstream := &anthropicHTTPUpstreamRecorder{responses: []*http.Response{
					{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(sample.body))},
					{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_ok","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))},
				}}
				repo := &modelNotFoundAccountRepoStub{}
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: &RateLimitService{accountRepo: repo}}
				first, second := newAnthropicAPIKeyAccountForTest(), newAnthropicAPIKeyAccountForTest()
				first.Extra["anthropic_passthrough"], second.Extra["anthropic_passthrough"] = passthrough, passthrough
				second.ID++
				parsed := &ParsedRequest{Body: NewRequestBodyRef([]byte(`{"model":"requested-model","messages":[{"role":"user","content":"hi"}]}`)), Model: "requested-model"}
				result, err := svc.Forward(c.Request.Context(), c, first, parsed)
				require.Nil(t, result)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, resilience.CategoryModelUnsupported, ClassifyFailoverError(failover).Category)
				require.False(t, c.Writer.Written())
				require.Len(t, repo.modelRateLimitCalls, 1)
				call := repo.modelRateLimitCalls[0]
				require.Equal(t, first.ID, call.accountID)
				require.Equal(t, "requested-model", call.scope)
				require.WithinDuration(t, time.Now().Add(30*time.Minute), call.resetAt, 5*time.Second)
				first.Extra[modelRateLimitsKey] = map[string]any{call.scope: map[string]any{"rate_limit_reset_at": call.resetAt.UTC().Format(time.RFC3339), "reason": call.reason}}
				require.False(t, first.IsSchedulableForModelWithContext(context.Background(), parsed.Model))
				require.True(t, first.IsSchedulableForModelWithContext(context.Background(), "other-model"))
				require.True(t, second.IsSchedulableForModelWithContext(context.Background(), parsed.Model))
				eventsValue, ok := c.Get(OpsUpstreamErrorsKey)
				require.True(t, ok)
				events, ok := eventsValue.([]*OpsUpstreamErrorEvent)
				require.True(t, ok)
				require.Equal(t, "failover", events[0].Kind)
				require.Equal(t, string(resilience.CategoryModelUnsupported), events[0].ResilienceCategory)
				result, err = svc.Forward(c.Request.Context(), c, second, parsed)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Contains(t, recorder.Body.String(), "msg_ok")
				require.Empty(t, upstream.responses)
				require.Zero(t, repo.tempCalls)
			})
		}
	}
}

func TestInference404GeminiAndSharedRouting(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "nginx page", body: inference404NginxPage},
		{name: "plain not found", body: "404 Page Not Found"},
		{name: "plain status", body: "404"},
		{name: "server signatures", body: "404 Not Found nginx/1.2.3 OpenResty Apache Cloudflare Caddy"},
		{name: "HTML entity", body: "<h1>404 Not&nbsp;Found</h1><hr><center>openresty</center>"},
		{name: "top-level generic JSON", body: `{"message":"Not Found"}`},
		{name: "structured not found with punctuation", body: `{"error":{"status":"NOT_FOUND","message":"  NOT.FOUND!! "}}`},
		{name: "generic type", body: `{"error":{"type":"not_found_error","message":"Not Found"}}`},
		{name: "generic code", body: `{"error":{"code":"NotFound","message":"Not Found"}}`},
		{name: "empty fields", body: `{"error":{"code":"","type":"","message":""}}`},
		{name: "existing explicit unsupported model text", body: `{"error":{"type":"invalid_request_error","message":"The model 'requested-model' is not supported"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			repo := &modelNotFoundAccountRepoStub{}
			svc := &GeminiMessagesCompatService{rateLimitService: &RateLimitService{accountRepo: repo}}
			account := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey}
			require.True(t, svc.shouldFailoverGeminiUpstreamError(http.StatusNotFound, []byte(body)))
			require.True(t, shouldFailoverClassifiedUpstreamError(http.StatusNotFound, nil, []byte(body)))
			svc.handleGeminiUpstreamError(context.Background(), account, http.StatusNotFound, nil, []byte(body), "requested-model")
			require.Len(t, repo.modelRateLimitCalls, 1)
			require.Equal(t, "requested-model", repo.modelRateLimitCalls[0].scope)
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestInference404PreservesClientResourceErrors(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"previous_response_not_found","message":"Previous response not found"}}`,
		`{"error":{"code":"not_found","param":"file_id","message":"File not found"}}`,
		`{"error":{"code":"container_not_found","message":"Container not found: cntr_missing"}}`,
		"File not found",
		"<h1>404 Not Found</h1><p>File not found</p><hr><center>nginx</center>",
		"<h1>404 Not Found</h1><p>&lt;File&gt; not found</p>",
		`{"error":{"code":"container_not_found","message":"Not found"}}`,
		`{"error":{"code":"file_not_found","message":"Not found"}}`,
		`{"error":{"type":"container_not_found","message":"Not found"}}`,
		`{"error":{"param":"file_id","message":"Not found"}}`,
		`{"error":{"param":"","message":"Not found"}}`,
		`{"error":{"param":null,"message":"Not found"}}`,
		`{"message":"Not Found","response":{"error":{"code":"container_not_found","message":"Not Found"}}}`,
		`{"message":"Not Found","error":{"message":"File not found"}}`,
		`{"message":"Not Found","error":{"message":42}}`,
		`{"message":"Not Found","error":"File not found"}`,
		// These formerly broad positives are excluded by the revised evidence rule.
		"upstream route missing",
		"endpoint not found",
		`{"error":{"type":"not_found_error","message":"404!!!"}}`,
		`{"error":{"message":"endpoint not found"}}`,
		`{"error":{"type":"invalid_request_error","message":"Not found"}}`,
		`{"error":{"type":"not_found_error","message":"File not found: file_missing"}}`,
		`{"error":{"type":"invalid_request_error","message":"No response found with id 'resp_missing'"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			require.False(t, shouldFailoverClassifiedUpstreamError(http.StatusNotFound, nil, []byte(body)))
			require.False(t, (&GeminiMessagesCompatService{}).shouldFailoverGeminiUpstreamError(http.StatusNotFound, []byte(body)))
			repo := &modelNotFoundAccountRepoStub{}
			rl := &RateLimitService{accountRepo: repo}
			require.False(t, rl.HandleUpstreamModelNotFound(context.Background(), newAnthropicAPIKeyAccountForTest(), "requested-model", http.StatusNotFound, []byte(body)))
			require.Empty(t, repo.modelRateLimitCalls)
			c, _ := newOpenAIUpstreamErrorTestContext(t)
			svc := &OpenAIGatewayService{accountRepo: &openAIModelAvailabilityRepo{}}
			require.Nil(t, svc.failoverOpenAIModelAvailability(c.Request.Context(), c, newOpenAIUpstreamErrorTestAccount(),
				newOpenAIUpstreamErrorResponse(http.StatusNotFound, body), []byte(body), "requested-model", false))
		})
	}
}

func TestInference404PreservesExplicitAccountClassifications(t *testing.T) {
	for _, tc := range []struct {
		body string
		want resilience.Category
	}{
		{`{"error":{"code":"invalid_api_key","message":"invalid api key"}}`, resilience.CategoryAuth},
		{`{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`, resilience.CategoryBalance},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			result := classifyInferenceUpstreamHTTP(http.StatusNotFound, nil, []byte(tc.body), "")
			require.Equal(t, tc.want, result.Category)

			repo := &modelNotFoundAccountRepoStub{}
			rl := &RateLimitService{accountRepo: repo}
			rl.HandleUpstreamError(context.Background(), newAnthropicAPIKeyAccountForTest(), http.StatusNotFound, nil, []byte(tc.body), "requested-model")
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

func TestInference404OpenAIStopsAfterSecondHost(t *testing.T) {
	withResilienceSettings(t, nil)
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{accountRepo: &openAIModelAvailabilityRepo{}}
	for index, baseURL := range []string{"https://relay-a.example", "https://relay-b.example"} {
		account := newOpenAIUpstreamErrorTestAccount()
		account.ID = int64(index + 1)
		account.Type = AccountTypeAPIKey
		account.Credentials = map[string]any{"base_url": baseURL}
		failure := svc.failoverOpenAIModelAvailability(c.Request.Context(), c, account,
			newOpenAIUpstreamErrorResponse(http.StatusNotFound, ""), nil, "requested-model", false)
		require.NotNil(t, failure)
		if index == 0 {
			require.True(t, failure.ShouldRetryNextAccount())
			require.False(t, failure.RetryableOnSameAccount)
			require.Equal(t, resilience.CategoryModelUnsupported, ClassifyFailoverError(failure).Category)
			entry, exists := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
			require.True(t, exists)
			require.Equal(t, 1, entry.failureStreak)
		} else {
			require.False(t, failure.ShouldRetryNextAccount())
			require.Equal(t, GatewayFailureScopeRequest, failure.Scope)
			require.Equal(t, resilience.CategoryClientError, ClassifyFailoverError(failure).Category)
			_, exists := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
			require.False(t, exists)
		}
	}
}

func TestInference404CountTokensKeepsNonInferenceBehavior(t *testing.T) {
	for _, body := range []string{"", `{"error":{"message":"Not found: /v1/messages/count_tokens","type":"not_found_error"}}`} {
		t.Run(body, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			repo := &modelNotFoundAccountRepoStub{}
			svc := &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{accountRepo: repo},
				httpUpstream: &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}}
			parsed := &ParsedRequest{Model: "requested-model", Body: NewRequestBodyRef([]byte(`{"model":"requested-model","messages":[{"role":"user","content":"hi"}]}`))}
			err := svc.ForwardCountTokens(c.Request.Context(), c, newAnthropicAPIKeyAccountForTest(), parsed)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.Empty(t, repo.modelRateLimitCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestInference404GeminiNativeScope(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, mode := range []string{"normal", "pool", "custom"} {
		for _, action := range []string{"generateContent", "countTokens"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				account := geminiSignalTestAccount()
				switch mode {
				case "pool":
					account.Credentials["pool_mode"] = true
				case "custom":
					account.Credentials["custom_error_codes_enabled"] = true
					account.Credentials["custom_error_codes"] = []any{float64(http.StatusTooManyRequests)}
				}
				svc, upstream := newGeminiSkippedWriteService(http.StatusNotFound, "")
				repo := &modelNotFoundAccountRepoStub{}
				svc.rateLimitService = &RateLimitService{accountRepo: repo}
				c, rec := newGeminiNativeTestContext(t)
				_, err := svc.ForwardNative(c.Request.Context(), c, account, "requested-model", action, false, geminiSignalTestRequest())
				var failover *UpstreamFailoverError
				if action == "countTokens" {
					require.False(t, errors.As(err, &failover))
					if mode == "custom" {
						require.Equal(t, http.StatusInternalServerError, rec.Code)
					} else {
						require.Equal(t, http.StatusNotFound, rec.Code)
					}
					require.Empty(t, repo.modelRateLimitCalls)
				} else {
					require.ErrorAs(t, err, &failover)
					require.False(t, c.Writer.Written())
					if mode == "normal" {
						require.Len(t, repo.modelRateLimitCalls, 1)
					} else {
						require.Empty(t, repo.modelRateLimitCalls)
					}
				}
				require.Equal(t, 1, upstream.calls)
				require.Zero(t, repo.tempCalls)
			})
		}
	}
}

func TestInference404PreservesRequestRejections(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded; model is unavailable"}}`,
		`{"error":{"code":"content_policy_violation","message":"model is unavailable"}}`,
		`{"error":{"code":"cyber_policy","message":"model is unavailable"}}`,
		`{"error":{"code":"invalid_encrypted_content","message":"model is unavailable"}}`,
		`{"error":{"message":"{\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"maximum context length exceeded; model is unavailable\"}}"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			require.False(t, shouldFailoverClassifiedUpstreamError(http.StatusNotFound, nil, []byte(body)))
			require.False(t, (&GeminiMessagesCompatService{}).shouldFailoverGeminiUpstreamError(http.StatusNotFound, []byte(body)))
			repo := &modelNotFoundAccountRepoStub{}
			rl := &RateLimitService{accountRepo: repo}
			require.False(t, rl.HandleUpstreamModelNotFound(context.Background(), newAnthropicAPIKeyAccountForTest(), "requested-model", http.StatusNotFound, []byte(body)))
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

func TestInference404GeminiCountTokensConfiguredPolicyKeepsClassification(t *testing.T) {
	withResilienceSettings(t, nil)
	account := geminiSignalTestAccount()
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(http.StatusNotFound)}
	svc, _ := newGeminiSkippedWriteService(http.StatusNotFound, "")
	repo := &modelNotFoundAccountRepoStub{}
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	c, _ := newGeminiNativeTestContext(t)
	_, err := svc.ForwardNative(c.Request.Context(), c, account, "requested-model", "countTokens", false, geminiSignalTestRequest())
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, resilience.CategoryTransient, ClassifyFailoverError(failure).Category)
	events := upstreamErrorEventsFromContext(t, c)
	require.Len(t, events, 1)
	require.Equal(t, string(resilience.CategoryTransient), events[0].ResilienceCategory)
	require.Empty(t, repo.modelRateLimitCalls)
}
