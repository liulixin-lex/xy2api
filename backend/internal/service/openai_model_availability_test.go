//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIModelAvailabilityRepo struct {
	modelNotFoundAccountRepoStub
	setErrorCalls int
}

func (r *openAIModelAvailabilityRepo) SetError(context.Context, int64, string) error {
	r.setErrorCalls++
	return nil
}

func TestOpenAIModelAvailabilityForwardSwitchesAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		for _, sample := range []struct {
			name, body  string
			status      int
			restriction bool
		}{
			{"unsupported", `{"error":{"type":"invalid_request_error","message":"The 'requested-model' model is not supported when using Codex with a ChatGPT account."}}`, 400, false},
			{"unavailable", `model is unavailable`, 404, false},
			{"relay unavailable", `{"error":{"type":"internal_server_error","message":"model is unavailable"}}`, 503, false},
			{"upstream request restriction", `{"error":{"type":"invalid_request_error","code":"basispoints_request_invalid","message":"inline images exceed 32 MiB"}}`, 502, true},
		} {
			t.Run(name+"/"+sample.name, func(t *testing.T) {
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				body := []byte(`{"model":"requested-model","stream":false,"instructions":"test","input":"hello"}`)
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					newOpenAIUpstreamErrorResponse(sample.status, sample.body),
					newOpenAIUpstreamErrorResponse(200, `{"id":"resp_ok","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`),
				}}
				repo := &openAIModelAvailabilityRepo{}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
					rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
				first := openAIFailoverCachedBodyTestAccount(1, "first", map[string]any{"requested-model": "scheduled-model", "scheduled-model": "must-not-remap"})
				second := openAIFailoverCachedBodyTestAccount(2, "second", nil)
				for _, account := range []*Account{first, second} {
					account.Type = AccountTypeAPIKey
					account.Credentials["api_key"] = "test-key"
					account.Extra = map[string]any{"openai_passthrough": passthrough, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
				}
				// Pool policy must not turn model unavailability into a same-account retry.
				first.Credentials["pool_mode"] = true
				first.Credentials["pool_mode_retry_status_codes"] = []any{float64(sample.status)}
				require.True(t, first.IsPoolModeRetryableStatus(sample.status))

				result, err := svc.Forward(c.Request.Context(), c, first, body)
				require.Nil(t, result)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				expectedReason, expectedCategory := openAIModelUnavailableReason, resilience.CategoryModelUnsupported
				if sample.restriction {
					expectedReason, expectedCategory = openAIUpstreamRequestRestrictionReason, resilience.CategoryUpstreamRequestRestriction
				}
				require.Equal(t, expectedReason, failover.Reason)
				require.Equal(t, sample.status, failover.StatusCode)
				require.True(t, failover.ShouldRetryNextAccount())
				require.False(t, failover.RetryableOnSameAccount)
				require.False(t, failover.ShouldReportAccountScheduleFailure())
				require.Equal(t, expectedCategory, ClassifyFailoverError(failover).Category)
				_, _, healthFailure := classifyOpenAIAPIKeyHealthFailure(failover)
				require.False(t, healthFailure)
				require.False(t, IsResponseCommitted(c))
				require.Empty(t, rec.Body.String())
				events, ok := c.Get(OpsUpstreamErrorsKey)
				require.True(t, ok)
				upstreamEvents, ok := events.([]*OpsUpstreamErrorEvent)
				require.True(t, ok)
				require.Len(t, upstreamEvents, 1)
				require.Equal(t, string(expectedCategory), upstreamEvents[0].ResilienceCategory)
				upstreamModel := "scheduled-model"
				if passthrough {
					upstreamModel = "requested-model"
				}
				require.Equal(t, upstreamModel, gjson.GetBytes(upstream.bodies[0], "model").String())
				entry, recorded := svc.getOpenAIAccountModelTransientState().readEntry(first.ID, upstreamModel, time.Now())
				require.Equal(t, !sample.restriction, recorded)
				if recorded {
					require.Equal(t, 1, entry.failureStreak)
				}
				_, remapped := svc.openaiModelTransient.readEntry(first.ID, "must-not-remap", time.Now())
				require.False(t, remapped)

				result, err = svc.Forward(c.Request.Context(), c, second, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 2)
				require.Contains(t, rec.Body.String(), "resp_ok")
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(first))
			})
		}
	}
}

func TestOpenAIModelAvailabilityPreservesRequestRejections(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, passthrough := range []bool{false, true} {
		name := "native"
		if passthrough {
			name = "passthrough"
		}
		for _, sample := range []struct{ name, body string }{
			{"ordinary 400", openAIInvalidFunctionParametersBody},
			{"input parameter", `{"error":{"type":"invalid_request_error","param":"input","message":"Streaming for this model is not supported"}}`},
			{"ordinary request restriction on 400", `{"error":{"type":"invalid_request_error","code":"basispoints_request_invalid","message":"inline images exceed 32 MiB"}}`},
			{"echoed content", `{"error":{"message":"Invalid input"},"input":{"message":"model is unavailable"}}`},
			{"context", `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded; model is unavailable"}}`},
			{"content safety", `{"error":{"code":"content_policy_violation","message":"model is unavailable"}}`},
			{"cyber", `{"error":{"code":"cyber_policy","message":"model is unavailable"}}`},
			{"state", `{"error":{"code":"invalid_encrypted_content","message":"model is unavailable"}}`},
		} {
			t.Run(name+"/"+sample.name, func(t *testing.T) {
				c, rec := newOpenAIUpstreamErrorTestContext(t)
				upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(400, sample.body)}
				repo := &openAIModelAvailabilityRepo{}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: repo,
					rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil)}
				account := openAIFailoverCachedBodyTestAccount(1, "first", nil)
				account.Extra = map[string]any{"openai_passthrough": passthrough, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
				_, err := svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"requested-model","stream":false,"instructions":"test","input":"hello"}`))
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover))
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Len(t, upstream.bodies, 1)
				_, recorded := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
				require.False(t, recorded)
				require.Zero(t, repo.setErrorCalls)
				require.Zero(t, repo.tempCalls)
				require.Empty(t, repo.modelRateLimitCalls)
			})
		}
	}
}

func TestOpenAIModelAvailabilityStopsAcrossHosts(t *testing.T) {
	withResilienceSettings(t, nil)
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{accountRepo: &openAIModelAvailabilityRepo{}}
	body := []byte(`{"error":{"type":"invalid_request_error","param":"model","message":"model is unavailable"}}`)
	resp := newOpenAIUpstreamErrorResponse(400, string(body))
	for i, baseURL := range []string{"https://relay-a.example/v1", "https://relay-a.example/another", "https://relay-b.example/v1"} {
		account := &Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": baseURL}}
		failure := svc.failoverOpenAIModelAvailability(c.Request.Context(), c, account, resp, body, "model", false)
		require.NotNil(t, failure)
		_, cooled := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "model", time.Now())
		require.Equal(t, i < 2, failure.ShouldRetryNextAccount())
		require.Equal(t, i < 2, cooled)
		if i == 2 {
			require.Equal(t, GatewayFailureScopeRequest, failure.Scope)
			require.Equal(t, resilience.CategoryClientError, ClassifyFailoverError(failure).Category)
			require.True(t, failure.IsOpenAIModelUnavailable())
		}
	}
	require.Empty(t, rec.Body.String())
}

func TestOpenAIModelAvailabilityUsesExistingTransientCooldown(t *testing.T) {
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{accountRepo: &openAIModelAvailabilityRepo{}}
	account := newOpenAIUpstreamErrorTestAccount()
	resp := newOpenAIUpstreamErrorResponse(400, "")
	body := []byte(`{"error":{"code":"model_not_found","message":"model not found"}}`)
	for range 2 {
		require.NotNil(t, svc.failoverOpenAIModelAvailability(c.Request.Context(), c, account, resp, body, "requested-model", false))
	}
	entry, ok := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
	require.True(t, ok)
	require.Equal(t, 2, entry.failureStreak)
	require.WithinDuration(t, time.Now().Add(openAIModelTransientShortCooldown), entry.blockUntil, time.Second)
	_, otherModel := svc.openaiModelTransient.readEntry(account.ID, "other-model", time.Now())
	require.False(t, otherModel)

	// Once output starts, the same response must not record another failure.
	_, err := c.Writer.Write([]byte("visible output"))
	require.NoError(t, err)
	require.Nil(t, svc.failoverOpenAIModelAvailability(c.Request.Context(), c, account, resp, body, "requested-model", false))
	entry, ok = svc.openaiModelTransient.readEntry(account.ID, "requested-model", time.Now())
	require.True(t, ok)
	require.Equal(t, 2, entry.failureStreak)
	require.Equal(t, "visible output", rec.Body.String())
}
