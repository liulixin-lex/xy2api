//go:build unit

package service

import (
	"context"
	"fmt"
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
			if mode == "switched_first" || mode == "final_retry_fast" {
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
			if mode == "retry_one_round" {
				rows[1].RequestID = rows[0].RequestID
			}
			if mode == "final_retry_fast" {
				// Independent slow round, then a slow first attempt whose final retry is fast.
				rows[2].RequestID = rows[1].RequestID
				ms := 100
				rows[2].TTFOMs = &ms
			}
			if mode == "switched_first" || mode == "two_later_rounds" {
				rows[0].AccountID = 2
			}
			f.ledger.Rows = rows
			f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: f.now.Add(time.Hour), EvaluatedAt: f.now}}
			f.cycle()
			view := f.e.Snapshot().View
			if mode == "final_retry_fast" {
				history := view.(*schedQualityView).sessions[schedSessionKey{10, "synthetic-model", "session"}]
				require.Len(t, history, 2)
				require.Equal(t, 100, *history[1].TTFOMs, "the final attempt determines this round")
			}
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

// Exercise the real published view and the owner-selection path together.
func selectionRequireSnapshotMigration(t *testing.T, f *qualityFixture, want bool) {
	t.Helper()
	view := f.e.Snapshot().View
	require.Equal(t, want, view.SessionSlowTwice(10, 1, "synthetic-model", "session"))
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
	require.Equal(t, want, result != nil)
	if result == nil {
		require.Empty(t, session.slots.attempts)
		return
	}
	CompleteOpenAIStickyChange(ctx, 3, true, false)
	result.ReleaseFunc()
	require.EqualValues(t, 3, session.binding.owner)
}

func TestSelectionContinuousFinishedRoundsAllowMigration(t *testing.T) {
	f := newQualityFixture(t)
	rows := engineSamples(7, f.now, true)
	for i := range rows {
		rows[i].SessionKey = "session"
		rows[i].StartedAt = rows[i].StartedAt.Add(time.Duration(i) * 4 * time.Second)
		rows[i].FinishedAt = rows[i].StartedAt.Add(3 * time.Second)
	}
	rows[0].AccountID = 2
	f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: f.now.Add(time.Hour), EvaluatedAt: f.now}}
	for i := 1; i < len(rows)-1; i++ {
		// Rn finishes, only Rn-1 reaches the ledger, and Rn+1 is already running.
		f.now = rows[i].FinishedAt.Add(time.Second)
		persisted := rows[i-1]
		persisted.InsertedAt = f.now
		f.ledger.Rows = append(f.ledger.Rows, persisted)
		begin := rows[i]
		begin.FinishedAt = time.Time{}
		f.e.Observe(begin)
		f.e.Observe(rows[i])
		next := rows[i+1]
		next.FinishedAt = time.Time{}
		f.e.Observe(next)
		f.cycle()
		require.Len(t, f.e.window.running, 2, "each cycle has a finished pending write and a live attempt")
		require.True(t, f.e.window.running[next.AttemptID].FinishedAt.IsZero())
		selectionRequireSnapshotMigration(t, f, i >= 3)
		if i >= 3 {
			tail := f.e.Snapshot().View.(*schedQualityView).sessions[schedSessionKey{10, "synthetic-model", "session"}]
			require.Equal(t, []string{rows[i-1].AttemptID, rows[i].AttemptID}, []string{tail[0].AttemptID, tail[1].AttemptID})
		}
	}
}

func TestSelectionMergedFinishedRoundsBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		durable, observed     []int
		tail                  []int
		firstOnOther, fastEnd bool
		retry, inFlight       bool
		want                  bool
	}{
		{name: "switch_two_unpersisted", durable: []int{0}, observed: []int{1, 2}, firstOnOther: true, tail: []int{2}},
		{name: "unknown_predecessor_two", observed: []int{1, 2}, tail: []int{2}},
		{name: "unknown_predecessor_then_two", observed: []int{1, 2, 3}, tail: []int{2, 3}, want: true},
		{name: "earlier_unknown_then_durable", durable: []int{2, 3}, observed: []int{1}, tail: []int{2, 3}, want: true},
		{name: "latest_unpersisted_fast", durable: []int{0, 1}, observed: []int{2}, fastEnd: true, tail: []int{1, 2}},
		{name: "merged_predecessor_overrides_ledger_switch", durable: []int{0, 2}, observed: []int{1, 3}, firstOnOther: true, tail: []int{2, 3}, want: true},
		{name: "unpersisted_final_retry_fast", durable: []int{0, 1}, observed: []int{2, 3}, fastEnd: true, retry: true, tail: []int{1, 3}},
		{name: "inflight_other_account_ignored", durable: []int{0, 1}, inFlight: true, tail: []int{0, 1}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQualityFixture(t)
			rows := engineSamples(4, f.now.Add(-time.Minute), true)
			for i := range rows {
				rows[i].SessionKey = "session"
				rows[i].StartedAt = rows[i].StartedAt.Add(time.Duration(i) * 4 * time.Second)
				rows[i].FinishedAt = rows[i].StartedAt.Add(3 * time.Second)
			}
			if tc.firstOnOther {
				rows[0].AccountID = 2
			}
			if tc.retry {
				rows[3].RequestID = rows[2].RequestID
			}
			if tc.fastEnd {
				ms := 100
				rows[tc.observed[len(tc.observed)-1]].TTFOMs = &ms
			}
			for _, i := range tc.durable {
				f.ledger.Rows = append(f.ledger.Rows, rows[i])
			}
			for _, i := range tc.observed {
				begin := rows[i]
				begin.FinishedAt = time.Time{}
				f.e.Observe(begin)
				f.e.Observe(rows[i])
			}
			if tc.inFlight {
				live := rows[2]
				live.AccountID = 2
				live.FinishedAt = time.Time{}
				f.e.Observe(live)
			}
			f.store.Rows = []q.LineState{{LineKey: q.LineKey{AccountID: 1, GroupID: 10, Model: "synthetic-model", Line: q.DimensionTTFO}, Identity: q.IdentityRear, Until: f.now.Add(time.Hour), EvaluatedAt: f.now}}
			f.cycle()
			tail := f.e.Snapshot().View.(*schedQualityView).sessions[schedSessionKey{10, "synthetic-model", "session"}]
			var got, want []string
			for _, r := range tail {
				got = append(got, r.AttemptID)
			}
			for _, i := range tc.tail {
				want = append(want, rows[i].AttemptID)
			}
			require.Equal(t, want, got)
			selectionRequireSnapshotMigration(t, f, tc.want)
			if tc.name == "merged_predecessor_overrides_ledger_switch" {
				require.True(t, f.e.window.samples[SchedAccountModel{1, "synthetic-model"}][0].Switched, "migration projection must not change the RuleB ledger sample")
			}
		})
	}
}

type selectionPromoteFailure struct {
	*selectionTrialLeaseFake
	applied bool
}

func (l *selectionPromoteFailure) Promote(ctx context.Context, a int64, m string, g int64, request, session string, now time.Time) error {
	if l.applied {
		_ = l.selectionTrialLeaseFake.Promote(ctx, a, m, g, request, session, now)
	}
	return fmt.Errorf("synthetic promotion transport failure")
}
func TestSelectionPromotionErrorReleasesOnlyOwnRequest(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprint(applied), func(t *testing.T) {
			output := captureSchedObsLogs(t)
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			f.svc.selectionBoard.leases = &selectionPromoteFailure{leases, applied}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected := selectTrialRuntime(t, f, ctx, "session")
			member := TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID())
			requestKey := trialTestKey(1, "m", 7, member)
			require.True(t, leases.held[requestKey])
			selected.ReleaseFunc() // Production HTTP order: slot first, commit afterward.
			CompleteOpenAIStickyChange(ctx, 1, true, false)
			selected.ReleaseFunc()
			require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"], "conversion failure must not undo a committed binding")
			require.False(t, leases.held[requestKey], "best-effort release must not leave the request reservation until expiry")
			require.Equal(t, applied, leases.held[trialTestKey(1, "m", 7, "s:session")], "lost replies must not cause the session member to be removed")
			require.Contains(t, leases.calls, "release:"+requestKey)
			logged := false
			for _, row := range schedObsLogRecords(t, output) {
				if row["msg"] == "sched_quality.trial_promote_failed" {
					logged = true
					require.Equal(t, "sched_quality", row["component"])
				}
			}
			require.True(t, logged)
		})
	}
}
