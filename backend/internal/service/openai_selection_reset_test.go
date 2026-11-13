//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionPolicyView struct {
	schedquality.View
	disabled bool
	reset    map[int64]time.Time
}

func (v *selectionPolicyView) GroupEnabled(int64) bool    { return !v.disabled }
func (v *selectionPolicyView) ResetAt(id int64) time.Time { return v.reset[id] }

func TestSelectionResetReadsLatestSnapshot(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, scope := range []string{"session", "sticky_change:session"} {
		t.Run(scope, func(t *testing.T) {
			owner := selectionFixtureAccount(10, 5)
			owner.Schedulable = false
			f := newSelectionRuntimeFixture(t, owner, selectionFixtureAccount(1, 1))
			f.cache.sessionBindings["openai:session"] = 10
			store := f.svc.resilience.store.(*memResilienceStore)
			now := time.Now()
			key := memSessionKey(7, scope)
			store.avoid[key] = map[int64]time.Time{1: now.Add(time.Hour)}
			store.avoidWritten = map[string]map[int64]time.Time{key: {1: now.Add(-time.Minute)}}
			v := &selectionPolicyView{View: f.view, reset: map[int64]time.Time{}}
			f.svc.selectionBoard.view = func() schedquality.View { return v }
			read := func() map[int64]struct{} {
				if scope == "session" {
					return f.svc.resilience.sessionAvoid(context.Background(), ptrInt64Selection(7), "session")
				}
				return f.svc.stickyChangeAvoid(context.Background(), ptrInt64Selection(7), "session")
			}
			require.Contains(t, read(), int64(1))
			// Replace the published snapshot after construction: the callback must
			// not retain the old ResetAt value or change the sticky binding.
			v = &selectionPolicyView{View: f.view, reset: map[int64]time.Time{1: now.Add(-time.Second)}}
			require.Empty(t, read())
			require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
			require.Equal(t, int64(1), f.selectNow(t, context.Background(), "session", nil).Account.ID)
			require.NoError(t, store.AddSessionAvoid(context.Background(), 7, scope, 1, time.Minute, v.ResetAt(1)))
			require.Contains(t, read(), int64(1), "a new failure after reset is effective")
		})
	}
}

func TestSelectionInFlightCompletionAfterResetIsNewFact(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "success"}[success], func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
			f.cache.sessionBindings["openai:session"] = 10
			v := &selectionPolicyView{View: f.view, reset: map[int64]time.Time{}}
			f.svc.selectionBoard.view = func() schedquality.View { return v }
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.Equal(t, int64(1), selected.Account.ID)
			defer selected.ReleaseFunc()
			v.reset[1] = time.Now().Add(-time.Millisecond)
			CompleteOpenAIStickyChange(ctx, 1, success, false)
			avoided := f.svc.stickyChangeAvoid(context.Background(), ptrInt64Selection(7), "session")
			if success {
				require.Equal(t, int64(1), f.cache.sessionBindings["openai:session"])
				require.Empty(t, avoided)
			} else {
				require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
				require.Contains(t, avoided, int64(1))
			}
		})
	}
}
