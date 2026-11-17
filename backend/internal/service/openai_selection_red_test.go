//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	q "github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionReleaseBarrier struct {
	*selectionTrialLeaseFake
	entered, proceed chan struct{}
	blocked          string
}

func (l *selectionReleaseBarrier) Release(ctx context.Context, a int64, m string, g int64, member string) error {
	if member == l.blocked {
		close(l.entered)
		<-l.proceed
	}
	return l.selectionTrialLeaseFake.Release(ctx, a, m, g, member)
}
func TestSelectionConcurrentCandidateFailureCannotDeleteOtherReservation(t *testing.T) {
	for _, order := range []string{"cancel_before_commit", "release_after_commit"} {
		t.Run(order, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			c1 := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			c2 := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			m1, m2 := TrialLeaseRequestMember(resilience.StateFrom(c1).GatewayRequestID()), TrialLeaseRequestMember(resilience.StateFrom(c2).GatewayRequestID())
			barrier := &selectionReleaseBarrier{selectionTrialLeaseFake: leases, blocked: m1, entered: make(chan struct{}), proceed: make(chan struct{})}
			f.svc.selectionBoard.leases = barrier
			first, second := selectTrialRuntime(t, f, c1, "session"), selectTrialRuntime(t, f, c2, "session")
			require.True(t, leases.held[trialTestKey(1, "m", 7, m1)])
			require.True(t, leases.held[trialTestKey(1, "m", 7, m2)])
			done := make(chan struct{})
			go func() { defer close(done); CompleteOpenAIStickyChange(c1, 1, false, true) }()
			<-barrier.entered
			if order == "cancel_before_commit" {
				close(barrier.proceed)
				<-done
			}
			CompleteOpenAIStickyChange(c2, 1, true, false)
			if order == "release_after_commit" {
				close(barrier.proceed)
				<-done
			}
			first.ReleaseFunc()
			second.ReleaseFunc()
			require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"])
			require.Equal(t, map[string]bool{trialTestKey(1, "m", 7, "s:session"): true}, leases.held)
		})
	}
}

func TestSelectionLogicalRoundsDriveRealSnapshotMigration(t *testing.T) {
	for _, mode := range []string{"retry_one_round", "switched_first", "two_later_rounds", "final_retry_fast"} {
		t.Run(mode, func(t *testing.T) {
			f := newQualityFixture(t)
			n := 2
			if mode == "switched_first" {
				n = 3
			}
			if mode == "two_later_rounds" {
				n = 4
			}
			rows := engineSamples(n, f.now.Add(-time.Minute), true)
			for i := range rows {
				rows[i].SessionKey = "session"
				rows[i].StartedAt = rows[i].StartedAt.Add(time.Duration(i) * 4 * time.Second)
				rows[i].FinishedAt = rows[i].StartedAt.Add(3 * time.Second)
			}
			if mode == "retry_one_round" || mode == "final_retry_fast" {
				rows[1].RequestID = rows[0].RequestID
			}
			if mode == "final_retry_fast" {
				ms := 100
				rows[1].TTFOMs = &ms
			}
			if mode == "switched_first" || mode == "two_later_rounds" {
				rows[0].AccountID = 2
			}
			f.ledger.Rows = rows
			f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: f.now.Add(time.Hour), EvaluatedAt: f.now}}
			f.cycle()
			view := f.e.Snapshot().View
			require.Equal(t, mode == "two_later_rounds", view.SessionSlowTwice(10, 1, "synthetic-model", "session"))
			require.Equal(t, q.PlaceRear, view.StateOf(10, 1, "synthetic-model").Placement(true, f.now))
			session := selectionNewSessionFixture()
			session.owner = selectionTestAccount(1, 5)
			session.candidates = []*Account{selectionTestAccount(3, 1)}
			session.candidates[0].GroupIDs = []int64{10}
			session.binding.owner = 1
			session.session.now = func() time.Time { return f.now }
			session.session.view = func() q.View { return view }
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			result := session.session.selectOwner(ctx, 10, "session", "synthetic-model", session.owner, session.candidates, selectionRequest{})
			require.Equal(t, mode == "two_later_rounds", result != nil)
			if result != nil {
				CompleteOpenAIStickyChange(ctx, 3, true, false)
				result.ReleaseFunc()
			}
		})
	}
}

func TestSelectionTurnUsesResolvedModelAndRequestLease(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	f.svc.selectionBoard.leases = leases
	account := &f.repo.accounts[0]
	account.Credentials = map[string]any{"model_mapping": map[string]any{"channel-alias": "upstream", "upstream": "must-not-map-twice"}}
	f.view.States[fakeSchedViewKey{7, 1, "upstream"}] = q.StateView{TTFO: q.LineView{Trial: true}}
	st := resilience.NewRequestState()
	st.SetSession(7, "session", nil)
	ctx := resilience.WithRequestState(context.Background(), st)
	for turn := 0; turn < 2; turn++ {
		st.BeginGatewayTurn(time.Now())
		var released bool
		release := f.svc.PrepareOpenAISelectionTurn(ctx, ptrInt64Selection(7), account, "upstream", true, func() { released = true })
		member := TrialLeaseRequestMember(st.GatewayRequestID())
		require.True(t, leases.held[trialTestKey(1, "upstream", 7, member)])
		require.False(t, leases.held[trialTestKey(1, "upstream", 7, "s:session")])
		release()
		require.True(t, released)
		require.Empty(t, leases.held)
	}
	leases.held[trialTestKey(1, "upstream", 7, "s:session")] = true
	release := f.svc.PrepareOpenAISelectionTurn(ctx, ptrInt64Selection(7), account, "upstream", false, func() {})
	release()
	require.Contains(t, leases.calls, "touch:1/upstream/7/s:session")
	require.True(t, leases.held[trialTestKey(1, "upstream", 7, "s:session")])
}

type selectionExpiringLeases struct {
	*selectionTrialLeaseFake
	scores map[string]time.Time
}

func (l *selectionExpiringLeases) Touch(ctx context.Context, a int64, m string, g int64, s string, now time.Time) error {
	key := trialTestKey(a, m, g, s)
	if at, ok := l.scores[key]; ok && now.Sub(at) < TrialLeaseIdle {
		l.scores[key] = now
	} else {
		delete(l.scores, key)
	}
	return l.selectionTrialLeaseFake.Touch(ctx, a, m, g, s, now)
}
func TestSelectionActiveAliasedTurnsRenewBeyondIdleLimit(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	now := f.svc.selectionNow()
	account := &f.repo.accounts[0]
	account.Credentials = map[string]any{"model_mapping": map[string]any{"channel": "upstream", "upstream": "wrong"}}
	f.view.States[fakeSchedViewKey{7, 1, "upstream"}] = q.StateView{TTFO: q.LineView{Trial: true}}
	key := trialTestKey(1, "upstream", 7, "s:session")
	leases := &selectionExpiringLeases{newSelectionTrialLeaseFake(), map[string]time.Time{key: now}}
	f.svc.selectionBoard.leases = leases
	f.svc.selectionBoard.now = func() time.Time { return now }
	st := resilience.NewRequestState()
	st.SetSession(7, "session", nil)
	ctx := resilience.WithRequestState(context.Background(), st)
	for i := 0; i < 12; i++ {
		now = now.Add(time.Minute)
		f.view.ViewAt = now
		st.BeginGatewayTurn(now)
		release := f.svc.PrepareOpenAISelectionTurn(ctx, ptrInt64Selection(7), account, "upstream", false, func() {})
		release()
		require.Equal(t, now, leases.scores[key])
	}
	require.Len(t, leases.scores, 1)
	require.Len(t, leases.calls, 12)
}
