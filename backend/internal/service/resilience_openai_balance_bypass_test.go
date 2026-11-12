//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestStickyBypassBalancePause(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
		for _, mode := range []string{"legacy", "load_batch", "advanced"} {
			for _, pause := range []string{"active", "expired", "disabled", "request_failed"} {
				t.Run(platform+"/"+mode+"/"+pause, func(t *testing.T) {
					withResilienceSettings(t, nil)
					groupID := int64(10)
					until := time.Now().Add(time.Minute)
					accounts := []Account{
						{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"openai_capabilities": []any{"chat_completions"}}, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}, TempUnschedulableUntil: &until, TempUnschedulableReason: resilience.BalancePauseReason("HTTP 402")},
						{ID: 2, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"openai_capabilities": []any{"chat_completions"}}, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{groupID}},
					}
					if pause == "expired" {
						until = time.Now().Add(-time.Minute)
					}
					if pause == "disabled" {
						accounts[0].Schedulable = false
					}
					cfg := &config.Config{}
					cfg.Gateway.Scheduling.LoadBatchEnabled = mode == "load_batch"
					svc := &OpenAIGatewayService{
						accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
						cache:       &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:balance-session": 1}},
						cfg:         cfg, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
						resilience:       NewResilienceService(nil, newMemResilienceStore()),
						rateLimitService: newOpenAISelectionRateLimitService(),
					}
					state := resilience.NewRequestState()
					ctx := resilience.WithRequestState(context.Background(), state)
					var excluded map[int64]struct{}
					if pause == "request_failed" {
						state.MarkAccountFailed(1)
						excluded = map[int64]struct{}{1: {}}
					}
					logs := captureOpenAIObservationLogs(t)
					selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "balance-session", "grok-4.3", excluded, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, platform)
					require.NoError(t, err)
					require.NotNil(t, selection)
					if selection.ReleaseFunc != nil {
						defer selection.ReleaseFunc()
					}
					if pause == "expired" {
						require.Equal(t, int64(1), selection.Account.ID)
						require.NotContains(t, logs.String(), `"msg":"openai.sticky_bypass"`)
						return
					}
					require.Equal(t, int64(2), selection.Account.ID)
					require.Equal(t, 1, strings.Count(logs.String(), `"msg":"openai.sticky_bypass"`))
					switch pause {
					case "active":
						require.Contains(t, logs.String(), `"reason":"balance_exhausted"`)
					case "request_failed":
						require.Contains(t, logs.String(), `"reason":"owner_failed_in_request"`)
					case "disabled":
						require.NotContains(t, logs.String(), `"reason":"balance_exhausted"`)
					}
				})
			}
		}
	}
}
