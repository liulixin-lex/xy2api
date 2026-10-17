//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// A first-output timeout the gateway detects itself does not cool the
// account down, nor does it feed the per-model block (design §2): slow
// first output mostly reflects relay queues or long reasoning of that
// request. It switches accounts and counts for the session failure streak.
// Production review 2026-10-16 aligned the documents with this behaviour.
func TestLocalFirstOutputTimeoutLeavesTheAccountSchedulable(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	svc := &OpenAIGatewayService{rateLimitService: rl}
	c, _ := keepaliveStallContext("/v1/responses")
	failover := svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, account, nil, "",
		time.Now().Add(-181*time.Second), "gpt-5.1", "high", 180*time.Second, "first_output", http.Header{})
	require.NotNil(t, failover)
	_ = svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, account, nil, "",
		time.Now().Add(-181*time.Second), "gpt-5.1", "high", 180*time.Second, "first_output", http.Header{})
	require.True(t, repo.tempUntil.IsZero(), "no cooldown on the account")
	require.Zero(t, repo.errorCalls)
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.1"), "nor the per-model block of upstream 5xx")
	res := ClassifyFailoverError(failover)
	require.Equal(t, resilience.CategoryFirstOutputTimeout, res.Category)
	require.True(t, res.Category.CountsForSession())
	require.True(t, res.Category.SwitchAccount())
}

// A first_output_timeout answered by the upstream (a cascaded gateway) is
// classified FIRST_OUTPUT_TIMEOUT and, like the local one, leaves the
// account row alone. OpenAI API-key accounts keep the in-process per-model
// block of upstream 5xx: nothing after one failure, 10 s from the second in
// a row (design §2).
func TestUpstreamFirstOutputTimeoutLeavesTheAccountSchedulable(t *testing.T) {
	const message = "Upstream produced no output before the deadline"
	body := []byte(`{"error":{"type":"first_output_timeout","message":"` + message + `"}}`)
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI} {
		t.Run(platform, func(t *testing.T) {
			rl, repo, account, _ := newPenaltyFixture(t, platform)
			require.Equal(t, resilience.CategoryFirstOutputTimeout,
				classifyUpstreamHTTP(http.StatusGatewayTimeout, http.Header{}, body, message).Category)
			require.False(t, rl.HandleUpstreamError(context.Background(), account, http.StatusGatewayTimeout, http.Header{}, body))
			require.True(t, repo.tempUntil.IsZero(), "no cooldown on the account")
			require.Zero(t, repo.errorCalls)
			if platform != PlatformOpenAI {
				return
			}
			svc := &OpenAIGatewayService{rateLimitService: rl}
			svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusGatewayTimeout, http.Header{}, body, "gpt-5.1")
			require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.1"), "one failure blocks nothing")
			svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusGatewayTimeout, http.Header{}, body, "gpt-5.1")
			require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.1"), "the second in a row blocks the model")
			require.True(t, repo.tempUntil.IsZero(), "still no cooldown on the account")
		})
	}
}
