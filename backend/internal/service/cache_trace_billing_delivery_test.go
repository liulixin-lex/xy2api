//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type cacheTraceDeliveryAccountRepo struct {
	AccountRepository
	parent     *Account
	beforeRead func()
}

func (r cacheTraceDeliveryAccountRepo) GetByID(context.Context, int64) (*Account, error) {
	r.beforeRead()
	return r.parent, nil
}

func TestCacheTraceRecordUsageUsesBillingDeliveryDecision(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		abuse, disable, rewrite bool
	}{
		{"late_unbilled", false, false, false},
		{"late_abuse", true, false, false},
		{"switch_disabled_before_billing", false, true, false},
		{"billing_usage_rewrite", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			resilience.SetUndeliveredAbuseCheck(func(int64) bool { return tc.abuse })
			t.Cleanup(func() { resilience.SetUndeliveredAbuseCheck(nil) })
			f := cacheTraceNewContractFixture(t, tc.name, "responses")
			result := f.forward(t, "responses", false)
			rawPrompt, rawRead := result.Usage.InputTokens, result.Usage.CacheReadInputTokens
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			client, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			state := resilience.NewRequestState()
			state.InitDelivery(client, time.Now(), func() int64 { return 0 })
			state.SetAPIKey(f.api.ID)
			state.SetStage(resilience.StagePreOutput)
			ctx := resilience.WithRequestState(context.Background(), state)
			if tc.disable {
				cancel()
				state.ResolveDisconnect()
			}
			parentID := int64(5000)
			f.account.ParentAccountID = &parentID
			f.s.accountRepo = cacheTraceDeliveryAccountRepo{
				parent: &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
				beforeRead: func() {
					if tc.rewrite {
						result.Usage.InputTokens += 1000
					} else if tc.disable {
						settings := *resilience.Current()
						settings.Disconnect.Enabled = false
						resilience.Publish(settings)
					} else {
						cancel()
						state.ResolveDisconnect()
					}
				},
			}
			logs := t3SettlementCapture(t)
			require.NoError(t, f.s.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: result, APIKey: f.api, User: f.user, Account: f.account,
				PricingAt: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC),
			}))
			events := t3SettlementEvents(t, logs, "cache_trace")
			if !tc.abuse && !tc.disable && !tc.rewrite {
				require.Empty(t, events)
				require.Zero(t, f.rows.calls)
				require.Zero(t, f.billing.calls)
			} else {
				require.Len(t, events, 1)
				require.Equal(t, float64(rawPrompt), events[0]["P"])
				require.Equal(t, float64(rawRead), events[0]["read"])
				require.Equal(t, 1, f.rows.calls)
				require.Equal(t, 1, f.billing.calls)
				if tc.abuse {
					require.Equal(t, "undelivered", events[0]["na_reason"])
					require.Equal(t, UsageBillingModeUndeliveredAbuse, *f.rows.lastLog.BillingMode)
				} else {
					require.NotEqual(t, "undelivered", events[0]["na_reason"])
					require.Nil(t, f.rows.lastLog.BillingMode)
				}
			}
		})
	}
}

func TestCacheTraceObservationOnlyRespectsDisconnectSwitch(t *testing.T) {
	withResilienceSettings(t, nil)
	f := cacheTraceNewContractFixture(t, "disconnect_disabled", "responses")
	result := f.forward(t, "responses", false)
	awaitCacheTrace(t, f.s, f.c, result)
	t3SettlementWorkerDrained(t, f.s)
	ctx := disconnectedState(t, 0, f.api.ID)
	settings := *resilience.Current()
	settings.Disconnect.Enabled = false
	resilience.Publish(settings)
	logs := t3SettlementCapture(t)
	f.s.observeCacheUsage(result, f.account, time.Now())
	f.s.settleCacheTrace(ctx, result, f.account)
	events := t3SettlementEvents(t, logs, "cache_trace")
	require.Len(t, events, 1)
	require.NotEqual(t, "undelivered", events[0]["na_reason"])
}
