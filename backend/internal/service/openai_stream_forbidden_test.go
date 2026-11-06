//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamForbiddenUsesSoftCooldownWithoutHTTP403Penalty(t *testing.T) {
	withResilienceSettings(t, nil)
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"permission_error","code":"forbidden","message":"workspace forbidden","status":403}}}`)
	message := "workspace forbidden"
	require.True(t, openAIStreamFailedEventShouldFailover(payload, message))
	require.True(t, openAIStreamErrorEventShouldFailover(payload, message))
	require.False(t, openAIStream403AccountFailure(payload, message))
	require.False(t, openAIStream403TransientForbidden([]byte(`{"error":{"code":"invalid_api_key","status":403}}`), "invalid api key"))
	require.False(t, openAIStream403TransientForbidden([]byte(`{"error":{"code":"content_policy_violation","status":403}}`), "policy refusal"))

	for _, tc := range []struct {
		name        string
		passthrough bool
		wrapped401  bool
	}{
		{"native", false, false},
		{"passthrough", true, false},
		{"native wrapped 401", false, true},
		{"passthrough wrapped 401", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newWebSearchReplayRequest(t)
			account := webSearchReplayAccount(tc.passthrough)
			account.Credentials["pool_mode"] = false
			require.False(t, account.IsPoolMode(), "exercise the rate-limit path without the pool-mode exemption")
			repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
			rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			counter := &countingOpenAI403CounterCache{}
			rl.SetOpenAI403CounterCache(counter)
			frame := string(payload)
			wantStatus, wantWrapped := http.StatusForbidden, 0
			if tc.wrapped401 {
				frame = `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"401","status_code":401,"message":"invalid api key"}}}`
				wantStatus, wantWrapped = http.StatusUnauthorized, http.StatusUnauthorized
			}
			upstream := &httpUpstreamRecorder{responses: []*http.Response{requestRepairFailedStream(frame)}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			svc.rateLimitService = rl
			_, err := svc.Forward(req.ctx, req.c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, OpenAIStreamForbiddenReason, failoverErr.Reason)
			require.Equal(t, wantStatus, failoverErr.StatusCode)
			require.Equal(t, wantWrapped, failoverErr.WrappedStatus)
			require.False(t, failoverErr.IsCredentialFailure())
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Len(t, upstream.bodies, 1)
			require.False(t, req.c.Writer.Written(), "pre-output failure must remain available for failover")
			require.Empty(t, req.rec.Body.String())

			require.Zero(t, counter.increments, "non-pool stream 403 must not increment the HTTP 403 counter")
			require.Zero(t, repo.errorCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.modelRateLimitCalls)

			classified := ClassifyFailoverError(failoverErr)
			require.Equal(t, resilience.CategoryTransient, classified.Category)
			require.Equal(t, string(OpenAIStreamForbiddenReason), classified.Reason)
			require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
			entry, ok := svc.openaiModelTransient.readEntry(account.ID, "gpt-5.5", time.Now())
			require.True(t, ok)
			require.Equal(t, 1, entry.failureStreak)
		})
	}
}

func TestOpenAIWrappedForbiddenUsesSoftCooldown(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name    string
		event   string
		payload string
		status  int
	}{
		{"E1 with inner body without status", "error", `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"code":"upstream_error","message":"upstream rejected"}}}`, http.StatusForbidden},
		{"E2", "response.failed", `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"403","status_code":403,"message":"upstream rejected"}}}`, http.StatusForbidden},
		{"wrapped 401 credentials", "error", `{"type":"proxy.upstream_error","status":401,"upstream_body":{"error":{"code":"invalid_api_key","message":"invalid api key"}}}`, http.StatusUnauthorized},
		{"wrapped 403 deactivated", "error", `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"code":"account_deactivated","message":"account deactivated"}}}`, http.StatusForbidden},
		{"wrapped 403 credentials", "error", `{"type":"proxy.upstream_error","status":403,"upstream_body":{"error":{"code":"invalid_api_key","message":"invalid api key"}}}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newNonStreamingFailoverContext(t)
			account := newNonStreamingFailoverAccount()
			account.Credentials["pool_mode"] = false
			repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
			svc := newNonStreamingFailoverService()
			svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			counter := &countingOpenAI403CounterCache{}
			svc.rateLimitService.SetOpenAI403CounterCache(counter)
			payload := []byte(tc.payload)
			for attempt := 1; attempt <= 2; attempt++ {
				failoverErr := svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, tc.event, payload, "upstream rejected", "model")
				require.NotNil(t, failoverErr)
				require.Equal(t, OpenAIStreamForbiddenReason, failoverErr.Reason)
				require.Equal(t, GatewayFailureScopeRequest, failoverErr.Scope)
				require.Equal(t, tc.status, failoverErr.StatusCode)
				require.Equal(t, tc.status, failoverErr.WrappedStatus)
				require.False(t, failoverErr.IsCredentialFailure())
				require.True(t, failoverErr.ShouldRetryNextAccount())
				require.False(t, failoverErr.RetryableOnSameAccount)
				require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
				require.Equal(t, resilience.CategoryTransient, ClassifyFailoverError(failoverErr).Category)
				entry, ok := svc.openaiModelTransient.readEntry(account.ID, "model", time.Now())
				require.True(t, ok)
				require.Equal(t, attempt, entry.failureStreak, "each failed attempt must be counted once")
				if attempt == 2 {
					require.Equal(t, 10*time.Second, entry.blockUntil.Sub(entry.lastFailure))
				} else {
					require.True(t, entry.blockUntil.IsZero(), "the first failure does not start a cooldown")
				}
			}
			MarkResponseCommitted(c)
			require.Nil(t, svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, tc.event, payload, "upstream rejected", "model"))
			entry, ok := svc.openaiModelTransient.readEntry(account.ID, "model", time.Now())
			require.True(t, ok)
			require.Equal(t, 2, entry.failureStreak, "post-output failure must not add a forbidden cooldown")
			require.Zero(t, counter.increments, "wrapped stream 403 must not enter the HTTP 403 counter")
			require.Zero(t, repo.errorCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.modelRateLimitCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
		})
	}
}

func TestOpenAIStreamForbiddenRequiresCooldownEvidence(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name     string
		payload  string
		cooldown bool
	}{
		{"numeric response status", `{"type":"response.failed","response":{"error":{"status":403,"type":"permission_error"}}}`, true},
		{"numeric error status code", `{"error":{"status_code":403,"type":"permission_error"}}`, true},
		{"numeric top-level status", `{"status":403,"error":{"type":"permission_error"}}`, true},
		{"untyped response upstream error", `{"type":"response.failed","response":{"error":{"code":"upstream_error"}}}`, true},
		{"untyped error upstream error", `{"error":{"code":"upstream_error"}}`, true},
		{"untyped top-level upstream error", `{"code":"upstream_error"}`, true},
		{"text only", `{"error":{"message":"Upstream access forbidden"}}`, false},
		{"string status", `{"error":{"status":"403"}}`, false},
		{"fractional status", `{"error":{"status":403.5}}`, false},
		{"typed upstream error", `{"error":{"code":"upstream_error","type":"permission_error"}}`, false},
		{"empty type is present", `{"error":{"code":"upstream_error","type":""}}`, false},
		{"null type is present", `{"error":{"code":"upstream_error","type":null}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newNonStreamingFailoverContext(t)
			account := newNonStreamingFailoverAccount()
			svc := newNonStreamingFailoverService()
			payload := []byte(tc.payload)
			const message = "Upstream access forbidden"
			require.True(t, openAIStreamFailedEventShouldFailover(payload, message))
			require.True(t, openAIStreamErrorEventShouldFailover(payload, message))
			for attempt := 1; attempt <= 2; attempt++ {
				failoverErr := svc.newOpenAIStreamFailoverErrorWithModel(c, account, false, "", payload, message, "model")
				require.Equal(t, OpenAIStreamForbiddenReason, failoverErr.Reason)
				require.Equal(t, GatewayFailureScopeRequest, failoverErr.Scope)
				require.True(t, failoverErr.ShouldRetryNextAccount())
				require.False(t, failoverErr.RetryableOnSameAccount)
				require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
				entry, recorded := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "model", time.Now())
				require.Equal(t, tc.cooldown, recorded)
				if recorded {
					require.Equal(t, attempt, entry.failureStreak)
				}
			}
		})
	}
	require.False(t, openAIStreamForbiddenCooldownEvidence([]byte(`{"error":{"status":403}`), 0), "invalid JSON cannot establish cooldown evidence")
}

func TestOpenAIStream403NonWrappedDeactivatedKeepsAccountFailure(t *testing.T) {
	withResilienceSettings(t, nil)
	c, _ := newNonStreamingFailoverContext(t)
	account := newNonStreamingFailoverAccount()
	account.Credentials["pool_mode"] = false
	repo := &encryptedContentAccountRepo{resilienceAccountRepo: resilienceAccountRepo{account: account}}
	svc := newNonStreamingFailoverService()
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	payload := []byte(`{"type":"error","error":{"code":"account_deactivated","status":403,"message":"account deactivated"}}`)

	failoverErr := svc.nonStreamingTerminalFailureFailover(c, newNonStreamingSSEResponse(), account, false, "error", payload, "account deactivated", "model")

	require.NotNil(t, failoverErr)
	require.Zero(t, failoverErr.WrappedStatus)
	require.Equal(t, OpenAIUpstreamAccessStateReason, failoverErr.Reason)
	require.True(t, failoverErr.IsCredentialFailure())
	require.Equal(t, 1, repo.errorCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	_, cooled := svc.getOpenAIAccountModelTransientState().readEntry(account.ID, "model", time.Now())
	require.False(t, cooled, "non-wrapped account faults must not enter forbidden soft cooldown")
}
