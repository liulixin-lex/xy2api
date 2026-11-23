//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collabTrialEffectLease struct {
	*selectionTrialLeaseFake
	roomCalls, acquireCalls int
}

func (f *collabTrialEffectLease) Room(ctx context.Context, a int64, m string, g int64, s string, at time.Time) (bool, error) {
	f.roomCalls++
	return f.selectionTrialLeaseFake.Room(ctx, a, m, g, s, at)
}
func (f *collabTrialEffectLease) Acquire(ctx context.Context, a int64, m string, g int64, s string, at time.Time) (bool, string, error) {
	f.acquireCalls++
	return f.selectionTrialLeaseFake.Acquire(ctx, a, m, g, s, at)
}

func TestCollabOptionalTrialFullReachability(t *testing.T) {
	for _, mode := range []string{"room_full", "acquire_race"} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional_%t", mode, optional), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 9))
				base := newSelectionTrialLeaseFake()
				selectionSetTrial(f, 1, base)
				if mode == "room_full" {
					base.full[1] = true
				} else {
					base.lose[1] = true
				}
				leases := &collabTrialEffectLease{selectionTrialLeaseFake: base}
				f.svc.selectionBoard.leases = leases
				ctx := resilience.WithCollabAttempt(resilience.WithRequestState(t.Context(), resilience.NewCollabChildState(resilience.NewRequestState())), resilience.CollabAttempt{Optional: optional})
				selected, err := f.svc.SelectCollabAccount(ctx, ptrInt64Selection(7), "m", OpenAIEndpointCapabilityResponses, 0, nil)
				require.NoError(t, err)
				require.NotNil(t, selected)
				CombineSelectionRelease(selected, nil)()
				t.Logf("room=%d acquire=%d selected=%d retained_leases=%d", leases.roomCalls, leases.acquireCalls, selected.Account.ID, len(base.held))
				assert.Positive(t, leases.roomCalls)
				if mode == "acquire_race" {
					assert.Positive(t, leases.acquireCalls)
				}
				assert.Empty(t, base.held)
			})
		}
	}
}
