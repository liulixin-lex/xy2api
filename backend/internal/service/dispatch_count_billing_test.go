//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestDispatchCountPreservesBillingAndQuota(t *testing.T) {
	for _, tc := range []struct{ platform, model string }{{PlatformAnthropic, "claude-sonnet-4"}, {PlatformGemini, "gemini-2.5-flash"}, {PlatformOpenAI, "gpt-5.1"}} {
		t.Run(tc.platform, func(t *testing.T) {
			var baseline *UsageBillingCommand
			for _, count := range []int{0, 3} {
				logs := &openAIRecordUsageLogRepoStub{}
				bills := &openAIRecordUsageBillingRepoStub{}
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(context.Background(), st)
				for i := 0; i < count; i++ {
					st.NoteDispatch(time.Now(), "direct")
				}
				account := &Account{ID: 3, Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"quota": 100.0}}
				key := &APIKey{ID: 2, Quota: 100}
				user := &User{ID: 1}
				if tc.platform == PlatformOpenAI {
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, bills, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "dispatch-fixture", Model: tc.model, Usage: OpenAIUsage{InputTokens: 20, OutputTokens: 8}, Duration: time.Second}, APIKey: key, User: user, Account: account}))
				} else {
					svc := newGatewayRecordUsageServiceWithBillingRepoForTest(logs, bills, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
					if tc.platform == PlatformGemini {
						svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
							tc.model: {InputCostPerToken: 1e-6, OutputCostPerToken: 4e-6},
						}})
						svc.resolver = NewModelPricingResolver(nil, svc.billingService)
					}
					require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{Result: &ForwardResult{RequestID: "dispatch-fixture", Model: tc.model, Usage: ClaudeUsage{InputTokens: 20, OutputTokens: 8}, Duration: time.Second}, APIKey: key, User: user, Account: account}))
				}
				require.Equal(t, 1, bills.calls)
				require.NotNil(t, bills.lastCmd)
				require.Positive(t, bills.lastCmd.BalanceCost)
				cmd := *bills.lastCmd
				cmd.Usage = nil // compare the complete settlement command, excluding observation snapshot
				if count == 0 {
					baseline = &cmd
				} else {
					require.Equal(t, *baseline, cmd, "including dedup fingerprint, customer debit and every quota")
				}
				require.EqualValues(t, count, *logs.lastLog.DispatchCount)
			}
		})
	}
}
