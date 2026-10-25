//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// The real scheduler selects LR, real Forward fails, a new selection chooses a
// normal account and real Forward succeeds. The switch marker is the same hook
// used by handlers; this service-level test does not execute the handler loop.
func TestCacheTraceRoutingLastResortThenFailedForwardAndSuccessfulFailover(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	for _, protocol := range []string{"responses", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "t4-lr-then-failover", protocol)
			state := resilience.NewRequestState()
			f.c.Request = f.c.Request.WithContext(resilience.WithRequestState(f.c.Request.Context(), state))
			session := f.s.GenerateSessionHash(f.c, f.body)
			ctx := f.c.Request.Context()
			trace := cacheTraceFromGin(f.c)
			require.NotNil(t, trace)
			group := int64(38100)
			accounts := newLegacySchedulerDecisionTestAccounts(group, true)[:1]
			until := time.Now().Add(time.Minute)
			accounts[0].RateLimitResetAt = &until
			scheduler := newLegacySchedulerDecisionTestService(accounts, true, schedulerTestConcurrencyCache{})
			scheduler.resilience = NewResilienceService(nil, newMemResilienceStore())
			scheduler.accountRepo = openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
			first, _, err := scheduler.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			require.NoError(t, err)
			require.NotNil(t, first)
			releaseLegacySchedulerDecisionSelection(first)
			require.Equal(t, "last_resort", trace.data.RouteReason)
			require.Equal(t, first.Account.ID, trace.routing.lastResort)
			f.account.ID = first.Account.ID
			failedUpstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"synthetic LR unavailable"}}`))}}
			f.s.httpUpstream = failedUpstream
			var failed *OpenAIForwardResult
			if protocol == "chat" {
				failed, err = f.s.ForwardAsChatCompletions(ctx, f.c, f.account, f.body, "", "")
			} else {
				failed, err = f.s.Forward(ctx, f.c, f.account, f.body)
			}
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Nil(t, failed)
			require.Len(t, failedUpstream.requests, 1)
			require.False(t, f.c.Writer.Written())
			NoteCacheTraceFailover(ctx, 1)
			// Simulate another account becoming available between attempts. The same
			// scheduler, request context and trace are retained; no elapsed-time sleep.
			recovered := newLegacySchedulerDecisionTestAccounts(group, true)[1:]
			scheduler.accountRepo = openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: recovered}}
			second, _, err := scheduler.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", map[int64]struct{}{first.Account.ID: {}}, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			require.NoError(t, err)
			require.NotNil(t, second)
			releaseLegacySchedulerDecisionSelection(second)
			require.NotEqual(t, first.Account.ID, second.Account.ID)
			require.Zero(t, trace.routing.lastResort, "old per-selection candidate marker was cleared")
			require.Equal(t, 1, trace.routing.failoverN)
			require.Equal(t, "last_resort", trace.data.RouteReason, "confirmed request fact cannot be downgraded")
			f.account.ID = second.Account.ID
			result := f.forward(t, protocol, false)
			require.NotNil(t, result.CacheTrace)
			require.Equal(t, "last_resort", result.CacheTrace.data.RouteReason)
			require.Equal(t, second.Account.ID, result.CacheTrace.data.AccountID)
			awaitCacheTrace(t, f.s, f.c, result)
			t3SettlementWorkerDrained(t, f.s)
			logs := t3SettlementCapture(t)
			f.s.settleCacheTrace(f.c.Request.Context(), result, f.account)
			events := t3SettlementEvents(t, logs, "cache_trace")
			require.Len(t, events, 1)
			require.Equal(t, "last_resort", events[0]["route_reason"])
			for _, secret := range []string{"ctc-private", "synthetic LR unavailable"} {
				require.NotContains(t, logs.String(), secret)
			}
		})
	}
}
