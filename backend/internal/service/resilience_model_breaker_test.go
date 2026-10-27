//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestGeminiModelBreakerFailureIsolationAndSuccess(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformGemini)
	prev := Resilience()
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(prev) })
	st := resilience.NewRequestState()
	st.NoteTurn(0, " Requested-A ", "")
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now()))
	account.Credentials = map[string]any{"api_key": "test-key"}
	failure := func() *http.Response {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad gateway"}}`))}
	}
	httpStub := &queuedHTTPUpstreamStub{responses: []*http.Response{failure(), {
		StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"healthy"}]},"finishReason":"STOP"}]}`)),
	}}}
	svc := &GeminiMessagesCompatService{rateLimitService: rl, httpUpstream: httpStub, cfg: &config.Config{}}
	c, _ := newGeminiNativeTestContext(t)
	c.Request = c.Request.WithContext(ctx)
	result, err := svc.ForwardNative(ctx, c, account, "requested-a", "generateContent", false, []byte(geminiTransportTestNativeBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, httpStub.callCount, "an isolated 503 keeps its in-place retry")
	require.Zero(t, r.getModelBreakerState().size(), "a successful retry leaves no model failure")

	// One previous failed request is enough to skip the next in-place retry,
	// even before the breaker opens at streak two.
	svc.handleGeminiUpstreamError(ctx, account, http.StatusServiceUnavailable, nil, []byte(`{"error":{"message":"bad gateway"}}`))
	httpStub.responses, httpStub.callCount = []*http.Response{failure()}, 0
	c, _ = newGeminiNativeTestContext(t)
	c.Request = c.Request.WithContext(ctx)
	result, err = svc.ForwardNative(ctx, c, account, "requested-a", "generateContent", false, []byte(geminiTransportTestNativeBody))
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Nil(t, result)
	require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
	require.Equal(t, 1, httpStub.callCount, "an already failed model goes directly to failover")
	require.Zero(t, repo.tempCalls)
	require.True(t, resilienceModelBreakerBlocks(ctx, account, "requested-a"))
	require.False(t, resilienceModelBreakerBlocks(ctx, account, "requested-b"))
	require.False(t, (&GatewayService{}).isAccountSchedulableForModelSelection(ctx, account, "requested-a"))
	require.False(t, svc.isAccountUsableForRequest(ctx, account, "requested-a", PlatformGemini, false))
	tier, end, reason := resiliencePenaltyOf(account, "requested-a", time.Now())
	require.Equal(t, resilience.TierSoft, tier)
	require.Equal(t, "model_transient", reason)
	require.WithinDuration(t, time.Now().Add(10*time.Second), end, time.Second)

	// Success started before the failure, including a healthy other model,
	// must preserve the newer failed model's breaker.
	st = resilience.NewRequestState()
	st.NoteTurn(0, "requested-a", "")
	ctx = resilience.WithRequestState(context.Background(), st)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now().Add(-time.Minute)))
	r.NoteAccountSuccess(ctx, account)
	require.True(t, resilienceModelBreakerBlocks(ctx, account, "requested-a"))
	st.NoteTurn(0, "requested-b", "")
	r.NoteAccountSuccess(ctx, account)
	require.True(t, resilienceModelBreakerBlocks(ctx, account, "requested-a"))
	st.NoteTurn(0, "requested-a", "")
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, time.Now().Add(time.Second)))
	r.NoteAccountSuccess(ctx, account)
	require.False(t, resilienceModelBreakerBlocks(ctx, account, "requested-a"))
	require.False(t, resilienceGeminiSkipInPlaceRetry(ctx, account, http.StatusServiceUnavailable, nil, nil), "a cleared failure restores in-place retries")

	for _, reader := range []string{"end", "retry"} {
		state := r.getModelBreakerState()
		state.recordFailure(account.ID, st.Model(), time.Now().Add(-openAIModelTransientStreakTTL-time.Second))
		if reader == "end" {
			require.Zero(t, r.modelBreakerEnd(account, st.Model(), time.Now()))
		} else {
			require.False(t, resilienceGeminiSkipInPlaceRetry(ctx, account, http.StatusServiceUnavailable, nil, nil))
		}
		require.Zero(t, state.size(), "%s must delete the expired failure before returning", reader)
	}

	account.Credentials = map[string]any{"pool_mode": true}
	for range 2 {
		svc.handleGeminiUpstreamError(ctx, account, http.StatusBadGateway, nil, nil)
		rl.ApplyResilienceOutcome(ctx, account, resilience.Result{Category: resilience.CategoryStall}, "stall")
	}
	require.Zero(t, r.getModelBreakerState().size())
	require.Zero(t, repo.tempCalls)
}

func TestGatewayLastResortUsesModelBreakerAccount(t *testing.T) {
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 2,
	}})
	prev := Resilience()
	SetResilienceInstance(svc.resilience)
	t.Cleanup(func() { SetResilienceInstance(prev) })
	state := svc.resilience.getModelBreakerState()
	state.recordFailure(11, "model-a", time.Now())
	state.recordFailure(11, "model-a", time.Now())
	groupID := int64(7)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "model-a", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(11), result.Account.ID)
	require.Equal(t, int64(11), st.LastResort())
}

func TestOpenAICompatibleTransientKeepsAccountCooldown(t *testing.T) {
	rl, repo, account, r := newPenaltyFixture(t, PlatformDeepseek)
	st := resilience.NewRequestState()
	st.NoteTurn(0, "deepseek-chat", "")
	ctx := resilience.WithRequestState(context.Background(), st)
	for range 2 {
		rl.HandleUpstreamError(ctx, account, http.StatusServiceUnavailable, nil, []byte(`{"error":{"message":"bad gateway"}}`))
	}
	require.Positive(t, repo.tempCalls)
	require.WithinDuration(t, time.Now().Add(30*time.Second), repo.tempUntil, time.Second)
	require.Zero(t, r.getModelBreakerState().size())
}

func TestGatewayModelSoftBoundAccountKeepsBinding(t *testing.T) {
	svc, cache := newLastResortGatewayFixture(t, []Account{
		{ID: 31, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
		{ID: 32, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
	})
	prev := Resilience()
	SetResilienceInstance(svc.resilience)
	t.Cleanup(func() { SetResilienceInstance(prev) })
	state := svc.resilience.getModelBreakerState()
	state.recordFailure(31, "claude-sonnet-4", time.Now())
	state.recordFailure(31, "claude-sonnet-4", time.Now())
	cache.sessionBindings = map[string]int64{"sess": 31}
	groupID := int64(7)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	require.False(t, gatewayProfitControlGateActive(ctx))
	require.Empty(t, resilience.StateFrom(ctx).Model(), "eager binding cannot depend on NoteTurn")
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "sess", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(32), result.Account.ID)
	require.Equal(t, int64(31), cache.sessionBindings["sess"], "eager binding preserves the model-soft owner")
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &groupID, "sess", 32))
	require.Equal(t, int64(31), cache.sessionBindings["sess"])
	require.Zero(t, cache.deletedSessions["sess"])
}
