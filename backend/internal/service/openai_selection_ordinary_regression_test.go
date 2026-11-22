//go:build unit

package service

import (
	"context"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOrdinarySelectionCancelsAfterFirstSlot(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			accounts := []Account{selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)}
			f := newSelectionRuntimeFixture(t, accounts[:n]...)
			ctx, cancel := context.WithCancel(selectionReviewContext())
			defer cancel()
			f.loads.after = func(int64) { cancel() }
			r, err := selectOrdinaryNewSessionForTest(t, f, ctx, "session", nil, true)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, r)
			require.Equal(t, []int64{1}, f.releases)
		})
	}
}

type reviewSlotError struct {
	schedulerTestConcurrencyCache
	probes []int64
}

func (c *reviewSlotError) AcquireAccountSlot(ctx context.Context, id int64, limit int, key string) (bool, error) {
	c.probes = append(c.probes, id)
	if id == 2 && limit == 20 {
		return false, fmt.Errorf("synthetic replacement slot read failed")
	}
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, id, limit, key)
}
func TestStickyBorrowStopsOnReplacementSlotError(t *testing.T) {
	owner, b, c := selectionFixtureAccount(10, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(4, 3)
	f := newSelectionRuntimeFixture(t, owner, b, c)
	f.repo.accounts[1].Concurrency = 20
	sc := &snapshotHydrationCache{snapshot: []*Account{&owner, &b, &c}, accounts: map[int64]*Account{10: &owner, 2: &b, 4: &c}}
	f.svc.schedulerSnapshot = NewSchedulerSnapshotService(sc, nil, nil, nil, nil)
	slots := &reviewSlotError{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{releasedIDs: &f.releases}}
	f.svc.concurrencyService = NewConcurrencyService(slots)
	req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), Platform: PlatformOpenAI, RequestedModel: "m", SessionHash: "bound", RequiredTransport: OpenAIUpstreamTransportHTTPSSE, RequiredCapability: OpenAIEndpointCapabilityResponses}
	r, err := f.svc.selectOpenAIStickyCapacityBorrow(resilience.WithRequestState(t.Context(), resilience.NewRequestState()), &owner, req)
	id := int64(0)
	if r != nil {
		id = r.Account.ID
		r.ReleaseFunc()
	}
	t.Logf("BORROW replacement-cache-error selected=%d probes=%v releases=%v err=%v", id, slots.probes, f.releases, err)
	require.NoError(t, err)
	require.Nil(t, r)
	require.Equal(t, []int64{2, 2}, slots.probes)
}

func TestOrdinarySelectionPanicWhileFirstSlotHeld(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1))
	lastSlot := int64(0)
	f.loads.after = func(id int64) { lastSlot = id }
	f.svc.selectionBoard.now = func() time.Time {
		if lastSlot == 1 {
			panic("interrupted selection")
		}
		return f.view.At()
	}
	require.PanicsWithValue(t, "interrupted selection", func() { selectTrialRuntime(t, f, selectionReviewContext(), "session") })
	require.Equal(t, []int64{1}, f.releases)
}

func TestOrdinarySelectionFailedForwardReleasesTrialAndSlot(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	ctx := selectionReviewContext()
	selected := selectTrialRuntime(t, f, ctx, "session")
	require.EqualValues(t, 1, selected.Account.ID)
	require.Len(t, leases.held, 1)
	CompleteOpenAIStickyChange(ctx, 1, false, false)
	selected.ReleaseFunc()
	require.Empty(t, leases.held)
	require.Equal(t, []int64{1}, f.releases)
}
