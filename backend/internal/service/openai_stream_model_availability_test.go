//go:build unit

package service

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const streamModelUnavailablePayload = `{"response":{"error":{"code":"model_not_found","message":"The 'requested-model' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}}}`
const streamModelRequest = `{"model":"requested-model","stream":true,"input":"hello"}`

func streamModelResponse(payload, prefix string) *http.Response {
	resp := newOpenAIUpstreamErrorResponse(http.StatusOK, prefix+"event: response.failed\ndata: "+payload+"\n\n")
	resp.Header.Set("Content-Type", "text/event-stream")
	return resp
}

func streamModelAccount(id int64, passthrough bool, host string) *Account {
	account := openAIFailoverCachedBodyTestAccount(id, "relay", nil)
	account.Type = AccountTypeAPIKey
	account.Credentials["api_key"] = "test-key"
	account.Credentials["base_url"] = host
	account.Extra = map[string]any{"openai_passthrough": passthrough, "openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeOff}
	return account
}

func TestOpenAIStreamModelAvailabilityBeforeOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			success := newOpenAIUpstreamErrorResponse(200, keepaliveStallCreated+keepaliveStallDelta1+`data: {"type":"response.completed","response":{"id":"resp_ok","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
			success.Header.Set("Content-Type", "text/event-stream")
			upstream := &httpUpstreamRecorder{responses: []*http.Response{streamModelResponse(streamModelUnavailablePayload, keepaliveStallCreated), success}}
			repo := &openAIModelAvailabilityRepo{}
			svc := compatStreamService(upstream, 0, 0, 0)
			svc.accountRepo = repo
			svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
			first := streamModelAccount(1, path == "passthrough", "https://relay-a.example/v1")
			second := streamModelAccount(2, path == "passthrough", "https://relay-b.example/v1")
			// Seed one earlier failure so the event must advance the existing
			// two-failure threshold to a real cooldown, exactly once.
			svc.recordOpenAIAccountModelTransientFailure(first, "requested-model", time.Now())
			_, err := svc.Forward(c.Request.Context(), c, first, []byte(streamModelRequest))
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.True(t, failure.IsOpenAIModelUnavailable())
			require.True(t, failure.ShouldRetryNextAccount())
			require.False(t, failure.RetryableOnSameAccount)
			require.False(t, failure.ShouldReportAccountScheduleFailure())
			_, _, healthFailure := classifyOpenAIAPIKeyHealthFailure(failure)
			require.False(t, healthFailure)
			require.Empty(t, rec.Body.String())
			entry, ok := svc.getOpenAIAccountModelTransientState().readEntry(first.ID, "requested-model", time.Now())
			require.True(t, ok)
			require.Equal(t, 2, entry.failureStreak)
			require.True(t, entry.blockUntil.After(time.Now()))
			_, err = svc.Forward(c.Request.Context(), c, second, []byte(streamModelRequest))
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 2)
			require.Contains(t, rec.Body.String(), "resp_ok")
			require.NotContains(t, rec.Body.String(), "response.failed")
			require.NotContains(t, rec.Body.String(), "model_not_found")
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

func TestOpenAIStreamModelAvailabilityAfterOutput(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			state := resilience.NewRequestState()
			c.Request = c.Request.WithContext(resilience.WithRequestState(c.Request.Context(), state))
			InstallDeliveryTracking(c, time.Now())
			upstream := &httpUpstreamRecorder{resp: streamModelResponse(streamModelUnavailablePayload, keepaliveStallCreated+keepaliveStallDelta1)}
			repo := &openAIModelAvailabilityRepo{}
			svc := compatStreamService(upstream, 0, 0, 0)
			svc.accountRepo = repo
			svc.rateLimitService = NewRateLimitService(repo, nil, svc.cfg, nil, nil)
			account := streamModelAccount(1, path == "passthrough", "https://relay-a.example/v1")
			svc.recordOpenAIAccountModelTransientFailure(account, "requested-model", time.Now())
			_, err := svc.Forward(c.Request.Context(), c, account, []byte(streamModelRequest))
			require.Error(t, err)
			var failure *UpstreamFailoverError
			require.False(t, errors.As(err, &failure), "output must not be replayed")
			entry, ok := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
			require.True(t, ok)
			require.Equal(t, 2, entry.failureStreak)
			require.True(t, entry.blockUntil.After(time.Now()))
			require.Len(t, upstream.bodies, 1)
			require.Positive(t, state.DeliveredBytes())
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: response.failed"))
			require.Contains(t, rec.Body.String(), "model_not_found")
			require.False(t, state.TurnFailed(), "model availability must not count as account-health failure")
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Empty(t, repo.modelRateLimitCalls)
		})
	}
}

func TestOpenAIStreamModelAvailabilityPreservesRequestRejections(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		for _, sample := range []struct{ name, errBody string }{
			{"input param", `{"code":"model_not_found","type":"invalid_request_error","param":"input[0]","message":"model is unavailable"}`},
			{"state", `{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"model is unavailable"}`},
			{"context", `{"code":"context_length_exceeded","type":"invalid_request_error","message":"maximum context length exceeded; model is unavailable"}`},
		} {
			t.Run(path+"/"+sample.name, func(t *testing.T) {
				c, _ := newOpenAIUpstreamErrorTestContext(t)
				upstream := &httpUpstreamRecorder{resp: streamModelResponse(`{"response":{"error":`+sample.errBody+`}}`, keepaliveStallCreated)}
				svc := compatStreamService(upstream, 0, 0, 0)
				svc.accountRepo = &openAIModelAvailabilityRepo{}
				account := streamModelAccount(1, path == "passthrough", "https://relay-a.example/v1")
				_, err := svc.Forward(c.Request.Context(), c, account, []byte(streamModelRequest))
				require.Error(t, err)
				var failure *UpstreamFailoverError
				require.False(t, errors.As(err, &failure))
				_, recorded := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
				require.False(t, recorded)
				require.Len(t, upstream.bodies, 1)
			})
		}
	}
}

func TestOpenAIStreamModelAvailabilityStopsAcrossHosts(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, path := range []string{"native", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			payload := `{"response":{"error":{"code":"model_not_found","type":"invalid_request_error","param":"model","message":"model is unavailable"}}}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{streamModelResponse(payload, ""), streamModelResponse(payload, "")}}
			svc := compatStreamService(upstream, 0, 0, 0)
			svc.accountRepo = &openAIModelAvailabilityRepo{}
			for i, host := range []string{"https://relay-a.example/v1", "https://relay-b.example/v1"} {
				account := streamModelAccount(int64(i+1), path == "passthrough", host)
				_, err := svc.Forward(c.Request.Context(), c, account, []byte(streamModelRequest))
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.True(t, failure.IsOpenAIModelUnavailable())
				require.Equal(t, i == 0, failure.ShouldRetryNextAccount())
				require.Equal(t, http.StatusBadRequest, failure.StatusCode)
				require.JSONEq(t, gjson.Get(payload, "response").Raw, string(failure.ResponseBody))
				_, recorded := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "requested-model", time.Now())
				require.Equal(t, i == 0, recorded)
				if i == 1 {
					require.Equal(t, resilience.CategoryClientError, ClassifyFailoverError(failure).Category)
				}
			}
			require.Len(t, upstream.bodies, 2)
			require.Empty(t, rec.Body.String())
		})
	}
}
