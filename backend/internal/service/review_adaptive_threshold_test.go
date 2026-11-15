//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestReviewAdaptiveOfficialThresholdsRemainActive(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformKimi, PlatformZhipu, PlatformMiniMax} {
		for _, mode := range []string{"official", "relay_chat", "relay_anthropic", "old_relay_evidence", "official_evidence"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				withResilienceSettings(t, nil)
				accountSchedulingThresholdsSF.Forget(SettingKeyAccountSchedulingThresholds)
				accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{})
				t.Cleanup(func() { accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{}) })
				a := &Account{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{
					"api_protocol": APIProtocolAdaptive, accountSchedulingThresholdCredentialKey: 90,
				}, Extra: map[string]any{
					cnExtraKey(platform, cnExtraSuffix5hUsed):  95.0,
					cnExtraKey(platform, cnExtraSuffix5hReset): time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				}}
				if mode == "relay_chat" {
					a.Credentials["api_base_urls"] = map[string]any{APIProtocolChatCompletions: "https://relay.invalid"}
				}
				if mode == "relay_anthropic" {
					a.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://relay.invalid"}
				}
				if mode == "old_relay_evidence" {
					a.Extra[quotaCooldownEvidenceKey] = (QuotaCooldownEvidence{Owner: "unknown", Protocol: APIProtocolChatCompletions, OriginDigest: cooldownOriginDigest("https://relay.invalid")}).extra()
				}
				if mode == "official_evidence" {
					origin, err := upstreamBalanceOrigin(a.GetCNProtocolBaseURL(APIProtocolChatCompletions))
					require.NoError(t, err)
					a.Extra[quotaCooldownEvidenceKey] = (QuotaCooldownEvidence{Owner: "key", Protocol: APIProtocolChatCompletions, OriginDigest: cooldownOriginDigest(origin)}).extra()
				}
				repo := &rateLimitAccountRepoStub{}
				svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				svc.SetSettingService(NewSettingService(newMockSettingRepo(), &config.Config{}))
				want := mode == "official" || mode == "official_evidence"
				require.Equal(t, want, rawQuotaAppliesToTarget(context.Background(), a))
				require.Equal(t, want, svc.ApplyAccountSchedulingThreshold(context.Background(), a))
				if want {
					require.Equal(t, 1, repo.tempCalls)
				} else {
					require.Zero(t, repo.tempCalls)
				}
				_, known := cooldownAttemptTarget(context.Background(), a)
				require.False(t, known, "no invented attempt target")
				state := resilience.NewRequestState()
				state.SetUpstreamAttemptTarget(&resilience.UpstreamAttemptTarget{AccountID: 1, Protocol: APIProtocolChatCompletions, Origin: "https://relay.invalid"})
				require.False(t, rawQuotaAppliesToTarget(resilience.WithRequestState(context.Background(), state), a), "actual relay remains authoritative")
			})
		}
	}
}
