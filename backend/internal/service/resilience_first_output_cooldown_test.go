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

func TestPlannedFirstOutputTimeoutSkipsAccountLevelPenalties(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	settings := &openAIAPIKeyHealthSettingRepo{value: `{"enabled":true,"action":"temp_unsched","threshold_count":1,"temp_unsched_minutes":1}`}
	rl.SetSettingService(NewSettingService(settings, &config.Config{}))
	healthCache := &openAIAPIKeyHealthCacheStub{tripped: true}
	rl.SetOpenAIAPIKeyHealthCache(healthCache)
	account.Credentials = map[string]any{"pool_mode": true}
	svc := &OpenAIGatewayService{rateLimitService: rl}
	c, _ := keepaliveStallContext("/v1/responses")
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(c.Request.Context(), st)
	c.Request = c.Request.WithContext(ctx)
	start := time.Now().Add(-181 * time.Second)
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: "gpt-5.1", AttemptStart: start, Warm: false})
	failover := svc.newOpenAIFirstOutputTimeoutError(c.Request.Context(), c, account, nil, "",
		start, "gpt-5.1", "high", 180*time.Second, "first_output", http.Header{})
	require.NotNil(t, failover)
	require.Equal(t, PlannedFirstOutputTimeoutReason, failover.Reason)
	require.True(t, repo.tempUntil.IsZero(), "no cooldown on the account")
	require.Zero(t, repo.errorCalls)
	require.False(t, rl.ObserveOpenAIAPIKeyHealthFailure(ctx, account, failover))
	require.Zero(t, healthCache.recordCalls)
	require.Zero(t, settings.getCalls, "both account-level penalties are skipped before settings lookup")
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "gpt-5.1"))
	res := ClassifyFailoverError(failover)
	require.Equal(t, resilience.CategoryFirstOutputTimeout, res.Category)
	require.True(t, res.Category.CountsForSession())
	require.True(t, res.Category.SwitchAccount())
}

func TestCompatPlannedFirstOutputTimeoutUsesAttemptOutboundModel(t *testing.T) {
	withResilienceSettings(t, func(s *resilience.Settings) {
		s.Budget.ModelOverrides = map[string]int{"public-alias": 45, "upstream-a": 65, "upstream-b": 90}
	})
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"public-alias": "upstream-a", "upstream-a": "upstream-b",
		}},
	}
	c, _ := keepaliveStallContext("/v1/chat/completions")
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(c.Request.Context(), st)
	c.Request = c.Request.WithContext(ctx)
	start := time.Now()
	st.SetBudget(resilience.NewBudget(ctx, resilience.Current(), resilience.KindText, start))
	model := OpenAIAttemptModel(account, "public-alias")
	require.Equal(t, "upstream-a", model)
	st.SetAttemptProfile(resilience.AttemptProfile{AccountID: account.ID, Model: model, AttemptStart: start, Warm: true, Stream: true})
	require.Equal(t, 65*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, account.ID, "public-alias", "", start))
	require.Equal(t, 65*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, account.ID, model, "", start))
	require.Equal(t, 45*time.Second, svc.openAIFirstOutputTimeoutFor(ctx, account.ID+1, "public-alias", "", start), "mismatched profile uses the caller model")
	fe := &openAICompatFirstEvent{started: start, timeout: 65 * time.Second, model: model}
	for range 2 {
		var failover *UpstreamFailoverError
		require.ErrorAs(t, svc.openAICompatFirstEventTimeout(ctx, c, account, fe, "first_output", http.Header{}), &failover)
		require.Equal(t, PlannedFirstOutputTimeoutReason, failover.Reason)
	}
	state := svc.getOpenAIAccountModelTransientState()
	require.True(t, state.isBlocked(account.ID, model, time.Now()), "slow failures use the actual outbound model")
	require.False(t, state.isBlocked(account.ID, "upstream-b", time.Now()), "canonical model is not mapped twice")
	require.Equal(t, 1, state.size())
}

func TestUnplannedFirstOutputTimeoutStillHandlesStreamTimeout(t *testing.T) {
	rl, repo, account, _ := newPenaltyFixture(t, PlatformOpenAI)
	rl.SetSettingService(NewSettingService(&openAIAPIKeyHealthSettingRepo{value: `{"enabled":true,"action":"temp_unsched","threshold_count":1,"temp_unsched_minutes":1}`}, &config.Config{}))
	svc := &OpenAIGatewayService{rateLimitService: rl}
	c, _ := keepaliveStallContext("/v1/responses")
	failover := svc.newOpenAIFirstOutputTimeoutError(c.Request.Context(), c, account, nil, "",
		time.Now().Add(-181*time.Second), "gpt-5.1", "high", 180*time.Second, "first_output", http.Header{})
	require.Equal(t, GatewayFailureReason("first_output_timeout"), failover.Reason)
	require.False(t, repo.tempUntil.IsZero(), "legacy timeout still triggers the enabled account penalty")
	_, _, eligible := classifyOpenAIAPIKeyHealthFailure(failover)
	require.True(t, eligible, "unplanned upstream failure remains eligible for API-key health tracking")
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

func TestHandleOpenAITransientError_OAuthAndPoolFastFailureBreakSlowStreak(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			rl, _, account, _ := newPenaltyFixture(t, PlatformOpenAI)
			account.Type = accountType
			account.Credentials = map[string]any{
				"pool_mode":                    true,
				"pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)},
			}
			svc := &OpenAIGatewayService{rateLimitService: rl}
			state := svc.getOpenAIAccountModelTransientState()
			now := time.Now()
			state.recordSlowFailure(account.ID, "gpt-test", now.Add(-2*time.Second))
			state.recordSlowFailure(account.ID, "gpt-test", now.Add(-time.Second))
			key, ok := openAIAccountModelTransientKey(account.ID, "gpt-test")
			require.True(t, ok)
			state.mu.Lock()
			before := state.entries[key]
			state.mu.Unlock()
			require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, []byte(`{"error":{"message":"temporary upstream failure"}}`), "gpt-test"))
			state.mu.Lock()
			after := state.entries[key]
			state.mu.Unlock()
			require.Zero(t, after.slowStreak)
			before.slowStreak = 0
			require.Equal(t, before, after, "only the consecutive slow count changes")
			next := state.recordSlowFailure(account.ID, "gpt-test", now)
			require.Equal(t, 1, next.SlowStreak)
			require.Equal(t, openAIModelTransientLongCooldown, next.Cooldown, "a new slow streak uses the ordinary failure gradient")
			require.Equal(t, before.blockUntil, next.BlockUntil, "the existing slow block is preserved")
		})
	}
}
