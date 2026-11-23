//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

var errCollabTestStore = errors.New("synthetic binding outage")

type collabUnavailableBindingCache struct{ *selectionRuntimeCache }

func (collabUnavailableBindingCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, errCollabTestStore
}
func (collabUnavailableBindingCache) GetSessionBinding(context.Context, int64, string) (StickySessionBinding, error) {
	return StickySessionBinding{}, errCollabTestStore
}
func (collabUnavailableBindingCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return errCollabTestStore
}
func (collabUnavailableBindingCache) CommitSessionBinding(context.Context, int64, string, StickySessionBinding, int64, time.Duration, BindingCommitOptions) (BindingCommitResult, error) {
	return BindingCommitResult{Status: BindingCommitError}, errCollabTestStore
}

func TestCollabThreadBindingStoreFailureParity(t *testing.T) {
	withResilienceSettings(t, nil)
	var outcomes []string
	for _, on := range []bool{false, true} {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
		f.svc.selectionBoard.leases = newSelectionTrialLeaseFake()
		local := resilience.NewCollabTreeAffinity(32, time.Hour)
		ctx, session := collabAtomicRequest(t, f, local, "unavailable", on, false)
		f.cache.sessionBindings["openai:"+session] = 2
		if on {
			b := openAICollabBindingFrom(ctx)
			require.NoError(t, b.write(ctx, 2, time.Hour))
			// A returning instance has a recent local owner; it must not use it
			// to authorize a shared claim or pretend an outage read succeeded.
			ctx = collabAtomicContext(t, f, local, "unavailable", true)
		}
		f.svc.cache = collabUnavailableBindingCache{f.cache}
		_, _, err := f.svc.lookupStickySessionAccount(ctx, ptrInt64Selection(7), session)
		require.ErrorIs(t, err, errCollabTestStore)
		require.ErrorIs(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), session, 1, time.Hour), errCollabTestStore)
		result := selectTrialRuntime(t, f, ctx, session)
		change := pendingOpenAIStickyChange(ctx)
		require.NotNil(t, change)
		require.True(t, change.skipCommit)
		CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
		result.ReleaseFunc()
		outcomes = append(outcomes, fmt.Sprintf("selected=%d wait=%v tree=%d", result.Account.ID, result.WaitPlan, f.cache.sessionBindings["openai:"+session]))
	}
	require.Equal(t, outcomes[0], outcomes[1])
}
