//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOpenAIEncryptedContentRejectionPayloadCoversHTTPAndInband(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{"error":{"code":"invalid_encrypted_content","message":"neutral"}}`),
		[]byte(`{"response":{"error":{"code":"encrypted_context_invalid"}}}`),
		[]byte(`{"response":{"error":{"message":"encrypted content hydration failed"}}}`),
		[]byte(`{"error":{"message":"{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"neutral\"}}"}}`),
	} {
		require.True(t, isOpenAIEncryptedContentRejectionPayload(payload))
	}
	require.False(t, isOpenAIEncryptedContentRejectionPayload([]byte(`{"error":{"message":"encrypted payload rejected"}}`)))
	require.False(t, isOpenAIEncryptedContentRejectionPayload([]byte(`{"response":{"error":{"message":"{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"neutral\"}}"}}`)))
	require.False(t, isOpenAIEncryptedContentRejectionPayload([]byte(`{"error":{"code":"upstream_error","message":"{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"neutral\"}}"}}`)))
}

func TestOpenAIEncryptedContentRepairBodyHonorsPrefixesAndPreservesOpaqueItems(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"cipher"},{"type":"function_call_output","call_id":"call_1","output":"keep"},{"type":"agent_message","content":[{"type":"encrypted_content","data":"keep"}]}]}`)
	next, changed, err := openAIEncryptedContentRepairBody(body, "encrypted content could not be verified")
	require.NoError(t, err)
	require.True(t, changed)
	require.NotContains(t, string(next), `"encrypted_content":"cipher"`)
	require.Contains(t, string(next), `"call_id":"call_1"`)
	require.Contains(t, string(next), `"agent_message"`)

	_, changed, err = openAIEncryptedContentRepairBody(body, "encrypted content could not be verified for item amsg_1")
	require.NoError(t, err)
	require.False(t, changed)
	_, changed, err = openAIEncryptedContentRepairBody(body, "encrypted function output content could not be decrypted")
	require.NoError(t, err)
	require.False(t, changed)
}

func TestOpenAIEncryptedContentRoutingAndOpsCategory(t *testing.T) {
	withResilienceSettings(t, nil)
	payload := []byte(`{"error":{"code":"invalid_encrypted_content","message":"relay rejected content"}}`)
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadGateway, "relay rejected content", payload))
	require.False(t, openAIStreamFailedEventShouldFailover(payload, "relay rejected content"))
	require.False(t, openAIStreamErrorEventShouldFailover(payload, "relay rejected content"))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadGateway,
		"The encrypted content could not be verified. Reason: encrypted content hydration failed", nil))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc.recordOpenAIStreamUpstreamError(c, account, false, "rid", "stream_failed", payload, "relay rejected content")
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, string(resilience.CategoryClientError), events[0].ResilienceCategory)
}

func TestClassifyOpenAIWSErrorEventUsesEncryptedContentRejection(t *testing.T) {
	reason, recoverable := classifyOpenAIWSErrorEventFromRaw(
		"encrypted_context_invalid", "invalid_request_error", "neutral",
	)
	require.Equal(t, "invalid_encrypted_content", reason)
	require.True(t, recoverable)

	reason, _ = classifyOpenAIWSErrorEventFromRaw(
		"invalid_request_error", "invalid_request_error", "encrypted payload rejected",
	)
	require.NotEqual(t, "invalid_encrypted_content", reason)
}

type encryptedContentAccountRepo struct {
	resilienceAccountRepo
	modelRateLimitCalls int
}

func (r *encryptedContentAccountRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	r.modelRateLimitCalls++
	return nil
}

func TestOpenAIEncryptedContentAccountSideEffectsExitBeforePolicies(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		entry                    string
		custom, inband, disabled bool
	}{
		{name: "gateway temp rule", entry: "gateway"},
		{name: "gateway custom status", entry: "gateway", custom: true},
		{name: "gateway inband", entry: "gateway", inband: true},
		{name: "gateway resilience off", entry: "gateway", disabled: true},
		{name: "check policy", entry: "check"},
		{name: "handle upstream", entry: "handle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = !tc.disabled })
			body := []byte(`{"error":{"code":"invalid_encrypted_content","message":"neutral"}}`)
			if tc.inband {
				body = []byte(`{"response":{"error":{"code":"invalid_encrypted_content","message":"neutral"}}}`)
			}
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{
					"custom_error_codes_enabled": tc.custom,
					"custom_error_codes":         []any{float64(http.StatusBadGateway)},
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules":   []any{map[string]any{"error_code": float64(http.StatusBadGateway), "keywords": []any{"neutral"}, "duration_minutes": float64(1)}},
				}}
			repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
			rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc := &OpenAIGatewayService{rateLimitService: rateLimit}
			snapshot := *account
			switch tc.entry {
			case "gateway":
				require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body, "gpt-5.5"))
			case "check":
				require.Equal(t, ErrorPolicyNone, rateLimit.CheckErrorPolicy(context.Background(), account, http.StatusBadGateway, body, "gpt-5.5"))
			case "handle":
				require.False(t, rateLimit.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body, "gpt-5.5"))
			}
			require.Equal(t, snapshot, *account)
			require.Zero(t, repo.modelRateLimitCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.errorCalls)
			require.Empty(t, svc.getOpenAIAccountModelTransientState().entries)
		})
	}
}

func TestOpenAIEncryptedContentNativeHTTPAndInbandOpsCategory(t *testing.T) {
	for _, inband := range []bool{false, true} {
		name := "http"
		if inband {
			name = "inband"
		}
		t.Run(name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			svc := &OpenAIGatewayService{cfg: &config.Config{}}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			c, _ := newOpenAIUpstreamErrorTestContext(t)
			if inband {
				svc.recordOpenAIStreamUpstreamError(c, account, false, "rid", "stream_failed", []byte(`{"response":{"error":{"code":"invalid_encrypted_content","message":"neutral"}}}`), "neutral")
			} else {
				_, err := svc.handleErrorResponse(context.Background(), newOpenAIUpstreamErrorResponse(http.StatusBadGateway, `{"error":{"code":"invalid_encrypted_content","message":"neutral"}}`), c, account, nil)
				require.Error(t, err)
			}
			raw, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok)
			events := raw.([]*OpsUpstreamErrorEvent)
			require.Len(t, events, 1)
			require.Equal(t, string(resilience.CategoryClientError), events[0].ResilienceCategory)
			require.Empty(t, events[0].UpstreamResponseBody)
		})
	}
	withResilienceSettings(t, nil)
	payload := []byte(`{"error":{"code":"upstream_error","message":"{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"neutral\"}}"}}`)
	require.False(t, isOpenAIEncryptedContentRejectionPayload(payload))
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	c, _ := newOpenAIUpstreamErrorTestContext(t)
	svc.recordOpenAIStreamUpstreamError(c, account, false, "rid", "stream_failed", payload, "neutral")
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, string(resilience.CategoryTransient), events[0].ResilienceCategory)
}

func TestForwardAsChatCompletions_EncryptedContentDoesNotFailoverOrTempUnschedule(t *testing.T) {
	withResilienceSettings(t, nil)
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstreamBody := []byte(`{"error":{"code":"invalid_encrypted_content","message":"relay rejected content"}}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}}}
	repo := &tempUnschedulableOpenAIAccountRepo{}
	rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}, httpUpstream: upstream, rateLimitService: rateLimit}
	account := &Account{ID: 9, Name: "encrypted", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://compat.example", "temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{"error_code": float64(http.StatusBadGateway), "keywords": []any{"relay rejected content"}, "duration_minutes": float64(1)}}},
		Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: true, openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses)},
	}
	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Zero(t, repo.modelRateLimitAccountID)
	require.Len(t, upstream.bodies, 1)
	require.Empty(t, svc.getOpenAIAccountModelTransientState().entries)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	require.Len(t, events.([]*OpsUpstreamErrorEvent), 1)
	require.Equal(t, string(resilience.CategoryClientError), events.([]*OpsUpstreamErrorEvent)[0].ResilienceCategory)
}

const encryptedRepairRequestBody = `{"model":"gpt-5.5","session_id":"repair-session","stream":true,"previous_response_id":"resp_parent","input":[{"type":"reasoning","id":"rs_test","encrypted_content":"cipher","summary":[{"type":"summary_text","text":"keep"}]},{"type":"function_call_output","call_id":"call_test","output":"keep output"}]}`

func requestRepairFailedStream(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: " + payload + "\n\n")),
	}
}

func TestForwardOpenAIRequestRepairReplaysInbandRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body, payload string
		encrypted, disabled bool
	}{
		{"encrypted", encryptedRepairRequestBody, `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified for item rs_test"}}}`, true, false},
		{"resilience off", encryptedRepairRequestBody, `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified for item rs_test"}}}`, true, true},
		{"rejected field", `{"model":"gpt-5.5","stream":true,"prompt_cache_breakpoint":4,"input":"hello"}`, `{"type":"response.failed","response":{"error":{"code":"invalid_parameter","param":"prompt_cache_breakpoint","message":"Unsupported parameter: prompt_cache_breakpoint"}}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Enabled = !tc.disabled })
			req := newWebSearchReplayRequest(t)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(tc.payload), webSearchSuccessStream("resp_repaired")}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			account := webSearchReplayAccount(false)
			result, err := svc.Forward(req.ctx, req.c, account, []byte(tc.body))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.bodies, 2)
			require.Contains(t, req.rec.Body.String(), "resp_repaired")
			require.NotContains(t, req.rec.Body.String(), "response.failed")
			require.Empty(t, req.session.failures)
			if tc.encrypted {
				require.True(t, gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").Exists())
				require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
				require.Equal(t, "keep", gjson.GetBytes(upstream.bodies[1], "input.0.summary.0.text").String())
				require.Equal(t, "call_test", gjson.GetBytes(upstream.bodies[1], "input.1.call_id").String())
				require.Equal(t, "keep output", gjson.GetBytes(upstream.bodies[1], "input.1.output").String())
				require.Equal(t, "resp_parent", gjson.GetBytes(upstream.bodies[1], "previous_response_id").String())
				require.True(t, openAIRequestRepairFrom(req.c).encryptedTried)
				require.Contains(t, svc.sessionInvalidEncryptedContentDigests(getOpenAIGroupIDFromContext(req.c), svc.GenerateSessionHash(req.c, []byte(tc.body))), openAIEncryptedContentDigest("cipher"))
			} else {
				require.False(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_breakpoint").Exists())
			}
		})
	}
}

func TestForwardOpenAIRequestRepairDoesNotReplayUnrepairableOrExhausted(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		exhausted     bool
	}{
		{"agent message", "The encrypted content could not be verified for item amsg_test", false},
		{"exhausted budget", "The encrypted content could not be verified for item rs_test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			req := newWebSearchReplayRequest(t)
			if tc.exhausted {
				for i := 1; i < req.budget.MaxAttempts(); i++ {
					req.budget.RecordAttemptAt(time.Now())
				}
			}
			payload := `{"type":"response.failed","response":{"error":{"code":"invalid_encrypted_content","message":"` + tc.message + `"}}}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(payload), webSearchSuccessStream("resp_unused")}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(encryptedRepairRequestBody))
			var failoverErr *UpstreamFailoverError
			if strings.Contains(tc.message, "amsg_") {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, OpenAIStateRepairReason, failoverErr.Reason)
				require.Equal(t, "item_not_unique", failoverErr.M3Cause)
			} else {
				require.NotErrorAs(t, err, &failoverErr)
			}
			require.Len(t, upstream.bodies, 1)
			if failoverErr == nil {
				require.Contains(t, req.rec.Body.String(), "response.failed")
				require.Contains(t, req.rec.Body.String(), tc.message)
			}
			repair := openAIRequestRepairFrom(req.c)
			require.False(t, repair.encryptedTried)
			require.Equal(t, upstream.bodies[0], repair.body)
			require.True(t, gjson.GetBytes(repair.body, "input.0.encrypted_content").Exists())
			require.False(t, svc.getOpenAIWSStateStore().HasAnySessionInvalidEncryptedContent())
			require.Empty(t, req.session.failures)
		})
	}
}

func TestForwardOpenAIRequestRepairHTTPAndInbandShareEncryptedRetry(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	payload := `{"error":{"code":"invalid_encrypted_content","message":"The encrypted content could not be verified for item rs_test"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIUpstreamErrorResponse(http.StatusBadRequest, payload),
		requestRepairFailedStream(`{"type":"response.failed","response":` + payload + `}`),
		webSearchSuccessStream("resp_unused"),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	_, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(encryptedRepairRequestBody))
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Len(t, upstream.bodies, 2)
	require.True(t, openAIRequestRepairFrom(req.c).encryptedTried)
	require.Contains(t, req.rec.Body.String(), "response.failed")
	require.Empty(t, req.session.failures)
}

func TestForwardOpenAIRequestRepairRetries502EncryptedRejection(t *testing.T) {
	withResilienceSettings(t, nil)
	req := newWebSearchReplayRequest(t)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIUpstreamErrorResponse(http.StatusBadGateway, `{"error":{"code":"encrypted_context_invalid","message":"neutral"}}`),
		webSearchSuccessStream("resp_http_502_repaired"),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	result, err := svc.Forward(req.ctx, req.c, webSearchReplayAccount(false), []byte(encryptedRepairRequestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
	require.True(t, openAIRequestRepairFrom(req.c).encryptedTried)
}
