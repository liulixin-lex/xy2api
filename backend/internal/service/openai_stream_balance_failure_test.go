//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

const openAISubscriptionSharingFailedPayload = `{"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","type":"invalid_request_error","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}}`

func TestOpenAIStreamBalanceFailureUsesStructuredCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    bool
	}{
		{"response error", openAISubscriptionSharingFailedPayload, true},
		{"error code", `{"type":"error","error":{"code":"billing_hard_limit_reached","type":"invalid_request_error","message":"Request blocked."}}`, true},
		{"invalid request", `{"type":"error","error":{"type":"invalid_request_error","message":"Unknown parameter."}}`, false},
		{"echoed balance text", `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid input: subscription sharing usage limit"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			message := extractOpenAISSEErrorMessage(payload)
			require.Equal(t, tc.want, openAIStreamFailedEventShouldFailover(payload, message))
			require.Equal(t, tc.want, openAIStreamErrorEventShouldFailover(payload, message))
		})
	}
}

func TestOpenAIStreamSubscriptionSharingPausesAccount(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	payload := []byte(openAISubscriptionSharingFailedPayload)

	status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffectsWithContext(context.Background(), nil, account, payload, extractOpenAISSEErrorMessage(payload), nil, "gpt-test")

	require.Equal(t, http.StatusBadGateway, status)
	require.True(t, disabled)
	requireBalancePause(t, repo, 5*time.Minute)

	status429Payload := []byte(`{"type":"response.failed","status":429,"response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","type":"invalid_request_error","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}}`)
	status, disabled = svc.handleOpenAIStreamTerminalAccountSideEffectsWithContext(context.Background(), nil, account, status429Payload, extractOpenAISSEErrorMessage(status429Payload), http.Header{"x-codex-primary-reset-at": []string{"2099-01-01T00:00:00Z"}}, "gpt-test")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.True(t, disabled)
	requireBalancePause(t, repo, 5*time.Minute)
}

func TestOpenAIStreamSubscriptionSharingAfterOutputOnlyPauses(t *testing.T) {
	rl, repo, account, state := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}, rateLimitService: rl}
	rec := newRecordingSessions()
	for i := 0; i < postOutputCooldownThreshold; i++ {
		ctx := postOutputRequest(rec)
		attempt := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
		payload := []byte(openAISubscriptionSharingFailedPayload)
		message := extractOpenAISSEErrorMessage(payload)
		status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffectsWithContext(ctx, nil, account, payload, message, nil, "gpt-test")
		require.Equal(t, http.StatusBadGateway, status)
		require.True(t, disabled)
		svc.resilienceOpenAIFailedAfterOutput(ctx, account, attempt, payload, message, true)
		require.False(t, resilience.StateFrom(ctx).AccountFailed(account.ID))
	}
	requireBalancePause(t, repo, 5*time.Minute)
	require.Empty(t, rec.of(account.ID), "balance exhaustion is not a failed turn of the account")
	require.Empty(t, state.postOutput.failures, "no post-output soft cooldown is accumulated")
}

func TestOpenAIStreamBalanceFailoverPreservesStructuredCode(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"billing_hard_limit_reached","type":"invalid_request_error","message":"Request blocked."}}}`)
	err := svc.newOpenAIStreamFailoverErrorWithModel(nil, account, false, "", payload, extractOpenAISSEErrorMessage(payload), "gpt-test")

	require.JSONEq(t, string(payload), string(err.ResponseBody))
	require.Equal(t, resilience.CategoryBalance, ClassifyFailoverError(err).Category)
	requireBalancePause(t, repo, 5*time.Minute)
}
