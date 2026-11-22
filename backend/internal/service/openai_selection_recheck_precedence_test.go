//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewPublicSelectionReplacesReplayedConstraint(t *testing.T) {
	for _, mode := range []string{"fresh-explicit", "fresh-default"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1))
			ctx := selectionReviewContext()
			first, e := selectOrdinaryNewSessionForTest(t, f, WithOpenAISelectionCandidateConstraint(ctx, 1, "m", 99, func(a *Account) bool { return a.ID == 2 }), "", nil, true)
			require.NoError(t, e)
			require.EqualValues(t, 2, first.Account.ID)
			first.ReleaseFunc()
			next := ContextWithSelectionProfitGate(ctx, first)
			if mode == "fresh-explicit" {
				next = WithOpenAISelectionCandidateConstraint(next, 1, "m", 2, func(a *Account) bool { return a.ID == 1 })
			}
			r, e := f.svc.SelectAccountWithLoadAwareness(next, ptrInt64Selection(7), "", "m", nil)
			require.NoError(t, e)
			defer r.ReleaseFunc()
			t.Logf("DIRECT %s selected=%d attempts=%v", mode, r.Account.ID, f.attempts)
			require.EqualValues(t, 1, r.Account.ID)
		})
	}
}
