//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestThirdParty429EffectiveCooldown(t *testing.T) {
	week := http.Header{"X-Codex-Primary-Used-Percent": {"100"}, "X-Codex-Primary-Reset-After-Seconds": {"604800"}, "X-Codex-Primary-Window-Minutes": {"10080"}}
	for _, tc := range []struct {
		name         string
		headers      http.Header
		body         string
		cap, seconds int
	}{
		{"weekly", week, `{"error":{"message":"too many requests"}}`, 120, 120},
		{"five_hours", nil, `{"error":{"type":"usage_limit_reached","resets_in_seconds":18000}}`, 120, 120},
		{"retry120", http.Header{"Retry-After": {"120"}}, `{"error":{"message":"too many requests"}}`, 120, 120},
		{"retry30", http.Header{"Retry-After": {"30"}}, `{"error":{"message":"too many requests"}}`, 120, 30},
		{"date", http.Header{"Retry-After": {time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)}}, `{"error":{"message":"too many requests"}}`, 120, 30},
		{"cap45", week, `{"error":{"message":"too many requests"}}`, 45, 45},
		{"fallback", nil, `{"error":{"message":"too many requests"}}`, 120, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, func(s *resilience.Settings) { s.Failure.RetryAfterCapSeconds = tc.cap })
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			blocker := &runtimeBlockRecorder{}
			svc.SetAccountRuntimeBlocker(blocker)
			account := &Account{ID: 909, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid/v1"}}
			state := resilience.NewRequestState()
			state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: APIProtocolResponses, Origin: "https://relay.invalid:443"})
			ctx := resilience.WithRequestState(context.Background(), state)
			notBefore := time.Now().Add(time.Hour)
			state.NoteAccountRetryNotBefore(account.ID, resilience.UpstreamRecoveryHint{NotBefore: notBefore})
			start := time.Now()
			svc.HandleUpstreamError(ctx, account, 429, tc.headers, []byte(tc.body))
			require.Equal(t, 1, repo.rateLimitedCalls)
			require.InDelta(t, tc.seconds, repo.lastRateLimitedAt.Sub(start).Seconds(), 2)
			require.Len(t, blocker.until, 1)
			require.Equal(t, repo.lastRateLimitedAt, blocker.until[0])
			evidence := repo.lastExtraUpdates[quotaCooldownEvidenceKey].(map[string]any)
			require.Equal(t, "unknown", evidence["owner"])
			require.Equal(t, cooldownOriginDigest("https://relay.invalid"), evidence["origin_digest"])
			hint, ok := state.AccountRetryNotBefore(account.ID)
			require.True(t, ok)
			require.True(t, notBefore.Equal(hint.NotBefore), "effective cooldown must not shorten the original request wait")
			if tc.name == "weekly" || tc.name == "cap45" {
				require.EqualValues(t, 604800, repo.lastExtraUpdates["codex_7d_reset_after_seconds"])
			}
		})
	}
	t.Run("official_defaults_and_explicit", func(t *testing.T) {
		withResilienceSettings(t, nil)
		for _, base := range []string{"", "https://api.openai.com/v1"} {
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 910, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": base}}
			start := time.Now()
			svc.HandleUpstreamError(context.Background(), account, 429, week, []byte(`{"error":{"message":"rate limited"}}`))
			require.InDelta(t, 604800, repo.lastRateLimitedAt.Sub(start).Seconds(), 1)
		}
	})
	t.Run("adaptive_actual_builders_and_attempt_switch", func(t *testing.T) {
		withResilienceSettings(t, nil)
		for _, officialChat := range []bool{false, true} {
			chat, responses := "https://relay.invalid/v1", "https://api.deepseek.com"
			if officialChat {
				chat, responses = responses, chat
			}
			account := &Account{ID: 911, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_protocol": "adaptive", "api_base_urls": map[string]any{APIProtocolChatCompletions: chat, APIProtocolResponses: responses},
			}}
			gateway := &OpenAIGatewayService{cfg: &config.Config{}}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			for _, protocol := range []string{APIProtocolChatCompletions, APIProtocolResponses} {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
				var request *http.Request
				var err error
				if protocol == APIProtocolResponses {
					request, err = gateway.buildUpstreamRequest(ctx, c, account, []byte(`{"model":"deepseek-chat","input":"hello"}`), "test-key", false, "", false)
				} else {
					var target string
					target, err = gateway.rawChatCompletionsURL(account)
					require.NoError(t, err)
					request, err = newGroupPromptUpstreamRequest(ctx, http.MethodPost, target, []byte(`{"model":"deepseek-chat","messages":[]}`), GroupPromptChat)
				}
				require.NoError(t, err)
				origin, err := upstreamBalanceOrigin(request.URL.String())
				require.NoError(t, err)
				// The M8 send boundary supplies this snapshot in production. Exercise
				// the consumer here with the real builder's final request URL.
				state.SetUpstreamRecoveryHint(nil)
				state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: protocol, Origin: origin})
				target, known := cooldownAttemptTarget(ctx, account)
				require.True(t, known)
				wantOfficial := (protocol == APIProtocolChatCompletions) == officialChat
				require.Equal(t, wantOfficial, cooldownTargetOfficial(account, target))
				repo := &rateLimitAccountRepoStub{}
				svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				applied := svc.applyThirdParty429(ctx, account, week, []byte(`{"error":{"message":"too many requests"}}`))
				require.Equal(t, !wantOfficial, applied)
				if applied {
					require.InDelta(t, 120, time.Until(repo.lastRateLimitedAt).Seconds(), 1)
				}
			}
			state.SetUpstreamAttemptTarget(nil)
			_, known := cooldownAttemptTarget(ctx, account)
			require.False(t, known, "missing adaptive target must not guess the Chat URL")
		}
	})
	t.Run("unknown_snapshot_replaces_old_official_provenance", func(t *testing.T) {
		withResilienceSettings(t, nil)
		repo := &rateLimitAccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{ID: 918, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": "adaptive"}}
		state := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), state)
		official := &resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: APIProtocolResponses, Origin: "https://api.deepseek.com"}
		state.SetUpstreamAttemptTarget(official)
		svc.persistOpenAICodexSnapshot(ctx, account, week)
		account.Extra = repo.lastExtraUpdates
		require.True(t, rawQuotaAppliesToTarget(ctx, account))
		state.SetUpstreamAttemptTarget(nil)
		svc.persistOpenAICodexSnapshot(ctx, account, week)
		for key, value := range repo.lastExtraUpdates {
			account.Extra[key] = value
		}
		require.Equal(t, "unknown", account.Extra[quotaCooldownEvidenceKey].(map[string]any)["owner"])
		state.SetUpstreamAttemptTarget(official)
		require.False(t, rawQuotaAppliesToTarget(ctx, account), "new raw window must not inherit prior official provenance")
	})
	t.Run("raw_windows_cannot_repause_relay", func(t *testing.T) {
		withResilienceSettings(t, nil)
		accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
		accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
		t.Cleanup(func() { accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{}) })
		settings := newMockSettingRepo()
		settings.data[SettingKeyAccountSchedulingThresholds] = `{"openai":90}`
		repo := &rateLimitAccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		svc.SetSettingService(NewSettingService(settings, &config.Config{}))
		account := &Account{ID: 912, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Credentials: map[string]any{"base_url": "https://relay.invalid/v1"}}
		svc.HandleUpstreamError(context.Background(), account, 429, week, []byte(`{"error":{"message":"rate limited"}}`))
		account.Extra = repo.lastExtraUpdates
		account.Extra["auto_pause_7d_threshold"] = 0.9
		require.False(t, svc.ApplyAccountSchedulingThreshold(context.Background(), account))
		paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.False(t, paused)
		account.Credentials["base_url"] = "https://api.openai.com"
		require.False(t, rawQuotaAppliesToTarget(context.Background(), account), "old relay evidence cannot become official after an edit")
		svc.persistOpenAICodexSnapshot(context.Background(), account, week)
		account.Extra = repo.lastExtraUpdates
		account.Extra["auto_pause_7d_threshold"] = 0.9
		paused, _ = shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
		require.True(t, paused)
	})
	t.Run("stream429_and_other_account_target", func(t *testing.T) {
		withResilienceSettings(t, nil)
		repo := &rateLimitAccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		gateway := &OpenAIGatewayService{rateLimitService: svc}
		account := &Account{ID: 913, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
		state := resilience.NewRequestState()
		ctx := resilience.WithRequestState(context.Background(), state)
		state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: APIProtocolResponses, Origin: "https://relay.invalid"})
		body := []byte(`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","status_code":429,"message":"too many requests","retry_after":600}}}`)
		status, _ := gateway.handleOpenAIStreamTerminalAccountSideEffectsWithContext(ctx, nil, account, body, "too many requests", nil, "gpt-5.4")
		require.Equal(t, 429, status)
		require.InDelta(t, 120, time.Until(repo.lastRateLimitedAt).Seconds(), 1)
		state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID + 1, Protocol: APIProtocolResponses, Origin: "https://api.openai.com"})
		target, known := cooldownAttemptTarget(ctx, account)
		require.True(t, known)
		require.Equal(t, "https://relay.invalid", target.Origin, fmt.Sprint(target))
	})
}

func TestThirdParty429ExplicitModelOwnership(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, credits := range []bool{false, true} {
		t.Run(fmt.Sprint("credits=", credits), func(t *testing.T) {
			repo := &anthropicWindowLimitRepo{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 919, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.invalid"}}
			state := resilience.NewRequestState()
			state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: APIProtocolAnthropic, Origin: "https://relay.invalid"})
			ctx := resilience.WithRequestState(context.Background(), state)
			now := time.Now()
			reset := now.Add(80 * time.Hour).Truncate(time.Second)
			headers := fable429Headers(now.Add(time.Hour), reset)
			body := []byte(`{"error":{"message":"rate limited"}}`)
			if credits {
				headers = http.Header{"Anthropic-Ratelimit-Unified-Reset": {fmt.Sprint(reset.Unix())}}
				body = []byte(`{"error":{"details":{"error_code":"credits_required","model":"claude-fable-5"}}}`)
			}
			svc.HandleUpstreamError(ctx, account, 429, headers, body, "claude-fable-5")
			require.Equal(t, 0, repo.rateLimitCalls)
			require.Equal(t, 1, repo.modelRateLimitCalls)
			require.Equal(t, anthropicFableRateLimitKey, repo.lastModelRateLimitScope)
			require.True(t, reset.Equal(repo.lastModelRateLimitReset))
			evidence := repo.lastExtraUpdates[quotaCooldownEvidenceKey].(map[string]any)
			require.Equal(t, "model", evidence["owner"])
		})
	}
}
