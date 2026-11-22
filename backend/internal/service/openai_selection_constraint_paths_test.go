//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
	"testing"
)

func selectionReviewContext() context.Context {
	return resilience.WithRequestState(context.Background(), resilience.NewRequestState())
}
func TestSelectionConstraintOwnerAndLegacy(t *testing.T) {
	for _, mode := range []string{"busy-owner", "hot-owner", "direct", "parent-context"} {
		t.Run(mode, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(4, 1))
			base := selectionReviewContext()
			allowed := true
			ctx := WithOpenAISelectionCandidateConstraint(base, 1, "m", 99, func(a *Account) bool { return a.ID == 10 && allowed })
			var r *AccountSelectionResult
			var err error
			if mode == "direct" {
				r, err = f.svc.SelectAccountWithLoadAwareness(ctx, ptrInt64Selection(7), "", "m", nil)
			} else if mode == "parent-context" {
				first, e := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, true)
				require.NoError(t, e)
				require.EqualValues(t, 10, first.Account.ID)
				first.ReleaseFunc()
				r, err = selectOrdinaryNewSessionForTest(t, f, base, "", nil, true)
			} else {
				f.cache.sessionBindings["openai:bound"] = 10
				if mode == "busy-owner" {
					f.loads.acquireResults = map[int64]bool{10: false}
				} else {
					f.loads.after = func(id int64) {
						if id == 10 {
							allowed = false
						}
					}
				}
				r, err = selectOrdinaryNewSessionForTest(t, f, ctx, "bound", nil, true)
			}
			if r != nil {
				t.Logf("selected=%d attempts=%v", r.Account.ID, f.attempts)
				r.ReleaseFunc()
			}
			if mode == "parent-context" {
				require.NoError(t, err)
				require.EqualValues(t, 4, r.Account.ID)
				return
			}
			if mode == "direct" {
				require.NoError(t, err)
				require.EqualValues(t, 10, r.Account.ID)
				return
			}
			require.Nil(t, r)
			require.Error(t, err)
		})
	}
}

type reviewDetachedRepo struct {
	*selectionRuntimeRepo
	reads int
}

func (s *reviewDetachedRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	s.reads++
	a, err := s.selectionRuntimeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	b := *a
	return &b, nil
}
func TestSelectionConstraintUsesDetachedPostSlotRow(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(2, 1), selectionFixtureAccount(4, 1))
	repo := &reviewDetachedRepo{selectionRuntimeRepo: f.repo}
	f.svc.accountRepo = repo
	f.loads.after = func(id int64) {
		if id == 2 {
			f.repo.accounts[0].Priority = 2
		}
	}
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99)
	r, err := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, true)
	require.NoError(t, err)
	defer r.ReleaseFunc()
	t.Logf("reads=%d attempts=%v releases=%v selected=%d priority_at_return=%d stored=%d", repo.reads, f.attempts, f.releases, r.Account.ID, r.Account.Priority, f.repo.accounts[0].Priority)
	require.EqualValues(t, 4, r.Account.ID)
	require.Equal(t, []int64{2}, f.releases)
}

func TestSelectionConstraintNeverQueues(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	f.loads.acquireResults = map[int64]bool{1: false}
	ctx := WithOpenAISelectionCandidateConstraint(selectionReviewContext(), 1, "m", 99)
	result, err := selectOrdinaryNewSessionForTest(t, f, ctx, "session", nil, true)
	require.Error(t, err)
	require.Nil(t, result)
	require.Empty(t, leases.held)
}
