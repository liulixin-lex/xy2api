//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestSelectionAttemptFlagsFollowSelectedAccount(t *testing.T) {
	for _, mode := range []string{"normal", "fallback", "takeover"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
			if mode == "takeover" {
				f.cache.sessionBindings["openai:session"] = 10
			}
			if mode == "fallback" {
				for _, id := range []int64{10, 1} {
					f.view.States[fakeSchedViewKey{7, id, "m"}] = schedquality.StateView{State: schedquality.StateCooling}
				}
			}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			fallback, takeover := state.SchedSelectionFlags(selected.Account.ID)
			require.Equal(t, mode == "fallback", fallback)
			require.Equal(t, mode == "takeover", takeover)
			fallback, takeover = state.SchedSelectionFlags(99)
			require.False(t, fallback, "a different attempt cannot inherit the previous account's flags")
			require.False(t, takeover)
			f.svc.recordOpenAISelectionFlags(ctx, ptrInt64Selection(7), "m", nil)
			fallback, takeover = state.SchedSelectionFlags(selected.Account.ID)
			require.False(t, fallback)
			require.False(t, takeover)
		})
	}
}
