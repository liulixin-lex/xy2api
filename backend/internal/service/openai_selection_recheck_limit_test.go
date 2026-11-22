//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type selectionLimitRows struct {
	*selectionRuntimeRepo
	reads      map[int64]int
	afterFirst func(int64)
}

func (s *selectionLimitRows) GetByID(ctx context.Context, id int64) (*Account, error) {
	a, e := s.selectionRuntimeRepo.GetByID(ctx, id)
	if e != nil {
		return nil, e
	}
	s.reads[id]++
	out := *a
	if s.reads[id] == 1 && s.afterFirst != nil {
		s.afterFirst(id)
	}
	return &out, nil
}
func TestConstrainedSlotRequiresMatchingLimit(t *testing.T) {
	a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
	a.Concurrency = 0
	f := newSelectionRuntimeFixture(t, a, b)
	repo := &selectionLimitRows{selectionRuntimeRepo: f.repo, reads: map[int64]int{}}
	repo.afterFirst = func(id int64) {
		if id == 1 {
			f.repo.accounts[0].Concurrency = 1
		}
	}
	f.svc.accountRepo = repo
	f.loads.acquireResults = map[int64]bool{1: false, 2: true}
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99)
	r, e := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, true)
	require.NoError(t, e)
	require.NotNil(t, r)
	r.ReleaseFunc()
	t.Logf("LIMIT returned=%d concurrency=%d acquired=%t real_slot_attempts=%v releases=%v repo_reads=%v", r.Account.ID, r.Account.Concurrency, r.Acquired, f.attempts, f.releases, repo.reads)
	require.EqualValues(t, 2, r.Account.ID)
	require.Equal(t, []int64{2}, f.attempts)
	require.Equal(t, []int64{2}, f.releases)
}
func TestImmediateSelectionAuthorityReadCount(t *testing.T) {
	for _, mode := range []string{"ordinary", "constrained"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			repo := &selectionLimitRows{selectionRuntimeRepo: f.repo, reads: map[int64]int{}}
			f.svc.accountRepo = repo
			ctx := selectionReviewContext()
			if mode == "constrained" {
				ctx = WithOpenAISelectionCandidateConstraint(ctx, 1, "m", 99)
			}
			r, e := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, true)
			require.NoError(t, e)
			defer r.ReleaseFunc()
			_, veto, _ := f.svc.SelectionAdmissionForResult(ContextWithSelectionProfitGate(ctx, r), r, false)
			require.False(t, veto)
			want := 1
			if mode == "constrained" {
				want = 2
			}
			t.Logf("AUTHORITY %s GetByID=%d", mode, repo.reads[1])
			require.Equal(t, want, repo.reads[1])
		})
	}
}

// The busy owner's limit can change after the slot service already granted it.
// Re-entering the ordinary pool must not reclaim that same account on this pass.
func TestConstrainedHotOwnerSkipsChangedLimit(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1))
	f.cache.sessionBindings["openai:turn"] = 1
	f.loads.after = func(id int64) {
		if id == 1 {
			f.repo.accounts[0].Concurrency++
		}
	}
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99)
	selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "turn", nil, true)
	require.NoError(t, err)
	defer selected.ReleaseFunc()
	require.EqualValues(t, 2, selected.Account.ID)
	require.Equal(t, []int64{1, 2}, f.attempts)
	require.Equal(t, []int64{1}, f.releases)
}
