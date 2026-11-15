//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSelectionRetryAfterFailedChange(t *testing.T) {
	for _, kind := range []string{"migrate", "takeover"} {
		t.Run(kind, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
			f.cache.sessionBindings["openai:session"] = 10
			if kind == "migrate" {
				f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{Failing: true}
			}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			selectNext := func(excluded map[int64]struct{}) *AccountSelectionResult {
				t.Helper()
				result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
				require.NoError(t, err)
				require.NotNil(t, result)
				t.Cleanup(result.ReleaseFunc)
				return result
			}
			first := selectNext(nil)
			require.EqualValues(t, 1, first.Account.ID)
			require.Equal(t, kind, pendingOpenAIStickyChange(ctx).kind)
			CompleteOpenAIStickyChange(ctx, first.Account.ID, false, false)
			require.EqualValues(t, 10, f.cache.sessionBindings["openai:session"])
			if kind == "takeover" {
				// The original owner becomes hard unavailable after the failed takeover.
				f.repo.accounts[0].Schedulable = false
			}
			next := selectNext(map[int64]struct{}{first.Account.ID: {}})
			require.EqualValues(t, 2, next.Account.ID, "a failed migration must not strand this request on the below-standard owner")
			require.Equal(t, "migrate", pendingOpenAIStickyChange(ctx).kind)
			fallback, takeover := state.SchedSelectionFlags(next.Account.ID)
			require.False(t, fallback)
			require.False(t, takeover, "a hard-unavailable replacement is not the earlier takeover")
			require.Empty(t, f.cache.casKeys)
			CompleteOpenAIStickyChange(ctx, next.Account.ID, true, false)
			require.EqualValues(t, 2, f.cache.sessionBindings["openai:session"])
		})
	}
}

func TestSelectionActiveMigrationKeepsItsPendingTarget(t *testing.T) {
	f := selectionNewSessionFixture()
	f.state(10, schedquality.StateView{Failing: true})
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	first := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{})
	require.NotNil(t, first)
	defer first.ReleaseFunc()
	pending := pendingOpenAIStickyChange(ctx)
	require.Nil(t, f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{}))
	require.Same(t, pending, pendingOpenAIStickyChange(ctx))
	require.Empty(t, f.binding.casKeys)
}
