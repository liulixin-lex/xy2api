//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionTrialLeaseFake struct {
	held                                      map[string]bool
	calls                                     []string
	roomErr, acquireErr, touchErr, releaseErr error
	full                                      map[int64]bool
	lose                                      map[int64]bool
}

func newSelectionTrialLeaseFake() *selectionTrialLeaseFake {
	return &selectionTrialLeaseFake{held: map[string]bool{}, full: map[int64]bool{}, lose: map[int64]bool{}}
}
func trialTestKey(a int64, m string, g int64, s string) string {
	return fmt.Sprintf("%d/%s/%d/%s", a, m, g, s)
}
func (f *selectionTrialLeaseFake) Room(_ context.Context, a int64, m string, g int64, s string, _ time.Time) (bool, error) {
	f.calls = append(f.calls, "room:"+trialTestKey(a, m, g, s))
	return !f.full[a], f.roomErr
}
func (f *selectionTrialLeaseFake) PeekRoom(_ context.Context, a int64, _ string) (bool, error) {
	f.calls = append(f.calls, "peek_room")
	return !f.full[a], f.roomErr
}
func (f *selectionTrialLeaseFake) LastFull(context.Context, int64, string, int64) (time.Time, error) {
	return time.Time{}, nil
}
func (f *selectionTrialLeaseFake) Acquire(_ context.Context, a int64, m string, g int64, s string, _ time.Time) (bool, string, error) {
	k := trialTestKey(a, m, g, s)
	f.calls = append(f.calls, "acquire:"+k)
	if f.acquireErr != nil {
		return false, "", f.acquireErr
	}
	if f.full[a] || f.lose[a] {
		return false, "", nil
	}
	f.held[k] = true
	return true, "test-generation", nil
}
func (f *selectionTrialLeaseFake) Touch(_ context.Context, a int64, m string, g int64, s string, _ time.Time) error {
	f.calls = append(f.calls, "touch:"+trialTestKey(a, m, g, s))
	return f.touchErr
}
func (f *selectionTrialLeaseFake) Release(_ context.Context, a int64, m string, g int64, s string) error {
	k := trialTestKey(a, m, g, s)
	f.calls = append(f.calls, "release:"+k)
	delete(f.held, k)
	return f.releaseErr
}
func (f *selectionTrialLeaseFake) ReleaseGroup(context.Context, int64, string, int64) error {
	return nil
}
func selectionSetTrial(f *selectionRuntimeFixture, id int64, leases *selectionTrialLeaseFake) {
	f.view.States[fakeSchedViewKey{7, id, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}}
	f.svc.selectionBoard.leases = leases
}
func selectTrialRuntime(t *testing.T, f *selectionRuntimeFixture, ctx context.Context, session string) *AccountSelectionResult {
	t.Helper()
	result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func TestOpenAISelectionTrialTiersAndRace(t *testing.T) {
	for _, mode := range []string{"room", "full", "room_error", "race", "acquire_error", "full_without_front", "race_without_front"} {
		t.Run(mode, func(t *testing.T) {
			a, b := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 9)
			f := newSelectionRuntimeFixture(t, a, b)
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			switch mode {
			case "full", "full_without_front":
				leases.full[1] = true
			case "room_error":
				leases.roomErr = errors.New("room unavailable")
			case "race", "race_without_front":
				leases.lose[1] = true
			case "acquire_error":
				leases.acquireErr = errors.New("acquire unavailable")
			}
			if mode == "full_without_front" || mode == "race_without_front" {
				f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
			}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			result := selectTrialRuntime(t, f, ctx, "")
			expected := int64(2)
			if mode == "room" || mode == "full_without_front" || mode == "race_without_front" {
				expected = 1
			}
			require.Equal(t, expected, result.Account.ID)
			if mode == "room" {
				require.True(t, leases.held[trialTestKey(1, "m", 7, TrialLeaseRequestMember(state.GatewayRequestID()))])
			}
			result.ReleaseFunc()
			require.Empty(t, leases.held)
			if mode == "race" || mode == "acquire_error" {
				require.Equal(t, []int64{1, 2}, f.attempts)
				require.Equal(t, []int64{1, 2}, f.releases)
			}
		})
	}
}

func TestOpenAISelectionTrialExistingAndOtherGroup(t *testing.T) {
	t.Run("idle_owner_touch_only", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		leases := newSelectionTrialLeaseFake()
		selectionSetTrial(f, 1, leases)
		leases.full[1] = true
		leases.touchErr = errors.New("touch unavailable")
		f.cache.sessionBindings["openai:raw-session"] = 1
		result := f.selectNow(t, context.Background(), "raw-session", nil)
		require.Equal(t, int64(1), result.Account.ID)
		require.Equal(t, []string{"touch:1/m/7/s:raw-session"}, leases.calls)
		require.Empty(t, leases.held, "touch must not revive expired leases")
	})
	t.Run("trial_elsewhere_has_no_quota", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		leases := newSelectionTrialLeaseFake()
		f.svc.selectionBoard.leases = leases
		f.view.States[fakeSchedViewKey{8, 1, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}}
		f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityPass}}
		require.Equal(t, int64(1), f.selectNow(t, context.Background(), "new", nil).Account.ID)
		require.Empty(t, leases.calls)
	})

}

func TestOpenAISelectionTrialSessionLifecycle(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}, TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}}
			leases.held[trialTestKey(10, "m", 7, "s:raw-session")] = true
			f.cache.sessionBindings["openai:raw-session"] = 10
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			result := selectTrialRuntime(t, f, ctx, "raw-session")
			require.Equal(t, int64(1), result.Account.ID)
			require.True(t, leases.held[trialTestKey(1, "m", 7, TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID()))])
			require.True(t, leases.held[trialTestKey(10, "m", 7, "s:raw-session")])
			CompleteOpenAIStickyChange(ctx, 1, success, false)
			result.ReleaseFunc()
			require.Equal(t, success, leases.held[trialTestKey(1, "m", 7, "s:raw-session")])
			require.Equal(t, !success, leases.held[trialTestKey(10, "m", 7, "s:raw-session")])
			require.Equal(t, "quality_migration_error", resilience.StateFrom(ctx).StickyBypassReason())
		})
	}
}

type selectionSlowView struct {
	*fakeSchedView
	slow bool
	seen []string
}

func (v *selectionSlowView) SessionSlowTwice(g, a int64, m, s string) bool {
	v.seen = append(v.seen, fmt.Sprintf("%d/%d/%s/%s", g, a, m, s))
	return v.slow
}
func TestOpenAISelectionMigrationLines(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      schedquality.StateView
		slow, want bool
	}{
		{"error", schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}, false, true},
		{"ttfo_one", schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityRear}}, false, false},
		{"ttfo_two", schedquality.StateView{TTFO: schedquality.LineView{Identity: schedquality.IdentityRear}}, true, true},
		{"cache", schedquality.StateView{Cache: schedquality.LineView{Identity: schedquality.IdentityRear}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			f.view.Takeovers[7] = fakeSchedTakeover{} // Isolate migration from the independent idle takeover rule.
			f.state(10, tc.state)
			v := &selectionSlowView{fakeSchedView: f.view, slow: tc.slow}
			f.session.view = func() schedquality.View { return v }
			result := f.selectNow(context.Background())
			require.Equal(t, tc.want, result != nil)
			require.Contains(t, v.seen, "7/10/m/session")
			if result != nil {
				result.ReleaseFunc()
			}
		})
	}
}

func TestOpenAISelectionTrialMigrationTargetAndPinnedTakeover(t *testing.T) {
	for _, mode := range []string{"trial_room", "trial_full", "trial_race", "takeover_trial", "pinned_same_priority"} {
		t.Run(mode, func(t *testing.T) {
			f := selectionNewSessionFixture()
			f.candidates = f.candidates[:1]
			leases := newSelectionTrialLeaseFake()
			f.session.leases = leases
			req := selectionRequest{Room: func(int64, string) bool { return mode != "trial_full" }}
			if mode != "pinned_same_priority" {
				f.state(1, schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}})
			}
			if mode == "trial_race" {
				leases.lose[1] = true
			}
			if mode == "pinned_same_priority" {
				f.candidates[0].Priority = f.owner.Priority
				f.candidates[0].AccountGroups = []AccountGroup{{GroupID: 7, Pinned: true}}
			}
			if mode != "takeover_trial" && mode != "pinned_same_priority" {
				f.state(10, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			result := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, req)
			require.Equal(t, mode == "trial_room", result != nil)
			if result != nil {
				CompleteOpenAIStickyChange(ctx, result.Account.ID, false, false)
				result.ReleaseFunc()
			}
			require.Zero(t, f.slots.held)
			require.Empty(t, leases.held)
		})
	}
}

func TestOpenAISelectionTrialBoardAndPinnedOrder(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 9), selectionFixtureAccount(3, 0))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	selectionSetTrial(f, 3, leases)
	leases.full[3] = true
	f.repo.accounts[1].AccountGroups = []AccountGroup{{GroupID: 7, Pinned: true}}
	rows, _, err := f.svc.selectionBoard.Board(context.Background(), 7, "m")
	require.NoError(t, err)
	require.Equal(t, []int64{2, 1, 3}, []int64{rows[0].AccountID, rows[1].AccountID, rows[2].AccountID})
	require.True(t, rows[0].Pinned)
	require.Equal(t, "trial", rows[1].Layer)
	require.Equal(t, "trial_full", rows[2].Layer)
	require.Equal(t, map[string]string{"ttfo": "trial", "error": "unknown", "cache": "unknown"}, rows[1].Lines)
	require.Empty(t, leases.held)
}

func TestOpenAISelectionTrialWaitPlanReservation(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	f.loads.acquireResults = map[int64]bool{1: false}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	result := selectTrialRuntime(t, f, ctx, "")
	require.NotNil(t, result.WaitPlan)
	require.Len(t, leases.held, 1)
	release := CombineSelectionRelease(result, nil)
	release()
	release()
	require.Empty(t, leases.held)
}

func TestOpenAISelectionSwitchLogTerminalStates(t *testing.T) {
	for _, kind := range []string{"create", "owner_released"} {
		for _, outcome := range []string{"success", "failure", "cancel"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				output := captureSchedObsLogs(t)
				old, target := selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1)
				old.Credentials = map[string]any{"model_mapping": map[string]any{"m": "from-alias"}}
				target.Credentials = map[string]any{"model_mapping": map[string]any{"m": "to-alias"}}
				f := newSelectionRuntimeFixture(t, old, target)
				if kind == "owner_released" {
					f.cache.sessionBindings["openai:session"] = 10
					f.repo.accounts[0].GroupIDs = []int64{8}
				}
				ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
				result := selectTrialRuntime(t, f, ctx, "session")
				defer result.ReleaseFunc()
				CompleteOpenAIStickyChange(ctx, result.Account.ID, outcome == "success", outcome == "cancel")
				CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
				var events []map[string]any
				for _, row := range schedObsLogRecords(t, output) {
					if row["msg"] == "sched_quality.session_switch" {
						events = append(events, row)
					}
				}
				require.Len(t, events, 1)
				require.Equal(t, kind, events[0]["kind"])
				require.Equal(t, "to-alias", events[0]["to_model"])
				if kind == "owner_released" {
					require.Equal(t, "from-alias", events[0]["from_model"])
				} else {
					require.Nil(t, events[0]["from_model"])
				}
				require.Equal(t, outcome == "success", events[0]["committed"])
				require.Equal(t, resilience.StateFrom(ctx).GatewayRequestID(), events[0]["gateway_request_id"])
				require.Equal(t, float64(1), events[0]["to"])
			})
		}
	}
}

type selectionCommitBarrier struct {
	*selectionBindingFake
	entered, finish chan struct{}
}

func (b *selectionCommitBarrier) CommitSessionBinding(ctx context.Context, g int64, key string, expected StickySessionBinding, target int64, ttl time.Duration, scopes BindingCommitOptions) (BindingCommitResult, error) {
	close(b.entered)
	<-b.finish
	return b.selectionBindingFake.CommitSessionBinding(ctx, g, key, expected, target, ttl, scopes)
}

func TestOwnerLeaseLookupAfterSlowCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := selectionNewSessionFixture()
		barrier := &selectionCommitBarrier{selectionBindingFake: f.binding, entered: make(chan struct{}), finish: make(chan struct{})}
		go func() {
			<-barrier.entered
			time.Sleep(2800 * time.Millisecond)
			close(barrier.finish)
		}()

		const session = "session"
		const oldModel = "owner-model"
		f.owner.Credentials = map[string]any{"model_mapping": map[string]any{"m": oldModel}}
		leases := newSelectionTrialLeaseFake()
		key := trialTestKey(f.owner.ID, oldModel, 7, TrialLeaseSessionMember(session))
		leases.held[key] = true
		var lookupBudget time.Duration
		change := &openAIStickyChange{
			kind: "owner_released", groupID: 7, session: session, key: f.binding.key, model: "m", target: 1, toModel: "target-model",
			expected: StickySessionBinding{AccountID: f.owner.ID, Generation: f.binding.generation}, cache: barrier, ttl: f.session.bindingTTL, leases: leases,
			ownerLookup: func(lookupCtx context.Context, id int64) *Account {
				require.Equal(t, f.owner.ID, id)
				deadline, ok := lookupCtx.Deadline()
				require.True(t, ok)
				lookupBudget = time.Until(deadline)
				select {
				case <-lookupCtx.Done():
					return nil
				case <-time.After(2500 * time.Millisecond):
					return f.owner
				}
			},
		}
		state := resilience.NewRequestState()
		state.SetStickyChange(change)
		logs := captureSchedObsLogs(t)
		CompleteOpenAIStickyChange(resilience.WithRequestState(t.Context(), state), change.target, true, false)

		require.Equal(t, change.target, f.binding.owner)
		require.Equal(t, openAIAccountStateUpdateTimeout, lookupBudget)
		require.NotContains(t, leases.held, key)
		rows := schedObsLogRecords(t, logs)
		require.Len(t, rows, 1)
		require.Equal(t, "committed", rows[0]["commit_result"])
		require.Contains(t, rows[0], "from_model")
		require.Nil(t, rows[0]["from_model"])
		require.Equal(t, change.toModel, rows[0]["to_model"])
	})
}

func TestOpenAISelectionTrialCommitBeforeConcurrentRelease(t *testing.T) {
	f := selectionNewSessionFixture()
	f.candidates = f.candidates[:1]
	f.state(10, schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}})
	f.state(1, schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}})
	leases := newSelectionTrialLeaseFake()
	f.session.leases = leases
	barrier := &selectionCommitBarrier{selectionBindingFake: f.binding, entered: make(chan struct{}), finish: make(chan struct{})}
	f.session.cache = barrier
	// Isolate the ordering assertion from the fixture's slot event slice.
	slotReleased := make(chan struct{})
	f.session.acquireSlot = func(context.Context, *Account) (*AcquireResult, error) {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() { close(slotReleased) }}, nil
	}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	result := f.session.selectOwner(ctx, 7, "session", "m", f.owner, f.candidates, selectionRequest{Room: func(int64, string) bool { return true }})
	require.NotNil(t, result)
	committed, released := make(chan struct{}), make(chan struct{})
	go func() { defer close(committed); CompleteOpenAIStickyChange(ctx, 1, true, false) }()
	<-barrier.entered
	go func() { defer close(released); result.ReleaseFunc() }()
	<-slotReleased
	close(barrier.finish)
	<-committed
	<-released
	require.True(t, leases.held[trialTestKey(1, "m", 7, "s:session")])
	require.Equal(t, int64(1), f.binding.owner)
}

func TestOpenAISelectionTrialConcurrentSessionFailureKeepsCommittedLease(t *testing.T) {
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	firstCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	secondCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	first := selectTrialRuntime(t, f, firstCtx, "session")
	second := selectTrialRuntime(t, f, secondCtx, "session")
	CompleteOpenAIStickyChange(firstCtx, 1, true, false)
	CompleteOpenAIStickyChange(secondCtx, 1, false, false)
	first.ReleaseFunc()
	second.ReleaseFunc()
	require.Equal(t, int64(1), f.cache.sessionBindings["openai:session"])
	require.True(t, leases.held[trialTestKey(1, "m", 7, "s:session")])
}

func (f *selectionTrialLeaseFake) Promote(_ context.Context, a int64, m string, g int64, request, session, generation string, _ time.Time) error {
	f.calls = append(f.calls, "promote:"+trialTestKey(a, m, g, request)+"->"+session)
	delete(f.held, trialTestKey(a, m, g, request))
	f.held[trialTestKey(a, m, g, session)] = true
	return nil
}

func (v *fakeSchedView) ReadTrialRound(_ context.Context, account int64, model string, group int64) (schedquality.StateView, error) {
	return v.StateOf(group, account, model), nil
}

type trialRoundStoreProbe struct {
	*fakeSchedView
	current schedquality.StateView
	err     error
	reads   int
}

func (p *trialRoundStoreProbe) ReadTrialRound(context.Context, int64, string, int64) (schedquality.StateView, error) {
	p.reads++
	return p.current, p.err
}

// Selection keeps its pre-reset snapshot while the authoritative store advances.
// The binding may commit sixteen minutes later, before another group's new trial.
func TestTrialPromotionChecksAuthoritativeRoundAfterBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		promote bool
	}{
		{"reset_at_plus_16_minutes", false}, {"different_round", false}, {"only_new_line", false}, {"one_original_line_remains", true},
		{"rear_on_other_line", false}, {"read_failure", false}, {"same_round_source_expired", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			leases := newSelectionTrialLeaseFake()
			selectionSetTrial(f, 1, leases)
			key := fakeSchedViewKey{7, 1, "m"}
			old := f.view.States[key]
			old.Error = old.TTFO
			f.view.States[key] = old
			db := &trialRoundStoreProbe{fakeSchedView: f.view, current: old}
			f.svc.selectionBoard.view = func() schedquality.View { return db }
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			selected := selectTrialRuntime(t, f, ctx, "session")
			member := TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID())
			request := trialTestKey(1, "m", 7, member)
			require.True(t, leases.held[request])
			require.Zero(t, db.reads, "checking the DB belongs to promotion only")
			// Move the physical completion, not the pre-reset selection snapshot.
			f.svc.selectionBoard.now = func() time.Time { return f.view.At().Add(16 * time.Minute) }
			later := schedquality.LineView{Trial: true, TrialUntil: old.TTFO.TrialUntil.Add(time.Minute)}
			switch tc.name {
			case "reset_at_plus_16_minutes":
				db.current = schedquality.StateView{}
			case "different_round":
				db.current = schedquality.StateView{TTFO: later}
			case "only_new_line":
				db.current = schedquality.StateView{Cache: later}
			case "one_original_line_remains":
				db.current = schedquality.StateView{Error: old.Error, Cache: later}
			case "rear_on_other_line":
				db.current.Cache.Identity = schedquality.IdentityRear
			case "read_failure":
				db.err = errors.New("read failed")
			case "same_round_source_expired":
				delete(leases.held, request)
			}
			CompleteOpenAIStickyChange(ctx, 1, true, false)
			selected.ReleaseFunc()
			require.Equal(t, 1, db.reads)
			require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"])
			require.False(t, leases.held[request])
			require.Equal(t, tc.promote, leases.held[trialTestKey(1, "m", 7, "s:session")])
		})
	}
}

type delayedTrialView struct {
	*fakeSchedView
	afterRead func()
}

func (v delayedTrialView) StateOf(g, a int64, m string) schedquality.StateView {
	state := v.fakeSchedView.StateOf(g, a, m)
	v.afterRead()
	return state
}

type observedTrialAcquire struct {
	*selectionTrialLeaseFake
	at time.Time
}

func (l *observedTrialAcquire) Acquire(ctx context.Context, a int64, m string, g int64, member string, at time.Time) (bool, string, error) {
	l.at = at
	return l.selectionTrialLeaseFake.Acquire(ctx, a, m, g, member, at)
}
func TestTrialReservationUsesDecisionTimeWhenSelectionPauses(t *testing.T) {
	a := selectionFixtureAccount(1, 1)
	f := newSelectionRuntimeFixture(t, a)
	leases := &observedTrialAcquire{selectionTrialLeaseFake: newSelectionTrialLeaseFake()}
	selectionSetTrial(f, 1, leases.selectionTrialLeaseFake)
	f.svc.selectionBoard.leases = leases
	observed := f.view.At()
	now := observed
	f.svc.selectionBoard.now = func() time.Time { return now }
	f.svc.selectionBoard.view = func() schedquality.View {
		return delayedTrialView{f.view, func() { now = observed.Add(16 * time.Minute) }}
	}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	trial := f.svc.newSelectionTrial(ctx, OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), RequestedModel: "m"}, 0)
	_, _ = trial.reserve(ctx, &a, false)
	require.True(t, now.After(observed))
	require.Equal(t, observed, leases.at)
}
