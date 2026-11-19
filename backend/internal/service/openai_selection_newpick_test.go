//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type newPickTestSnapshot struct {
	at       time.Time
	complete bool
	scores   map[int64]int64
	recent   SchedPerfRecent
	first15  int
}

func (s *newPickTestSnapshot) Usable(now time.Time) bool {
	return s.complete && now.Sub(s.at) <= 10*time.Minute
}
func (s *newPickTestSnapshot) ScoreMicros(id int64, model string) (int64, bool) {
	score, ok := s.scores[id]
	return score, ok && model == "m"
}
func (s *newPickTestSnapshot) Recent(group, _ int64, model string) SchedPerfRecent {
	if group == 7 && model == "m" {
		return s.recent
	}
	return SchedPerfRecent{}
}
func (s *newPickTestSnapshot) First15(int64) int { return s.first15 }

func newPickFixture(t *testing.T) (*selectionRuntimeFixture, *newPickTestSnapshot) {
	t.Helper()
	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 1))
	s := &newPickTestSnapshot{at: f.view.At(), complete: true, scores: map[int64]int64{1: 30_000, 2: 0}, recent: SchedPerfRecent{First10: 1, First30: 3}, first15: 4}
	f.svc.selectionBoard.performance = func() (newPickSnapshot, time.Time) { return s, s.at }
	return f, s
}

func TestNewPickBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*selectionRuntimeFixture, *newPickTestSnapshot)
		want   int64
	}{
		{"equal thresholds", nil, 2},
		{"gap 29999", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.scores[1]-- }, 1},
		{"no base score", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { delete(s.scores, 1) }, 1},
		{"no target score", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { delete(s.scores, 2) }, 1},
		{"first30 two", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.recent.First30 = 2 }, 1},
		{"first10 zero", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.recent.First10 = 0 }, 1},
		{"first15 three", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.first15 = 3 }, 1},
		{"lowest score", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.scores[3] = -1 }, 3},
		{"tie stable", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.scores[3] = 0 }, 2},
		{"best capped", func(f *selectionRuntimeFixture, s *newPickTestSnapshot) {
			s.scores[3] = 0
			l := &f.svc.selectionBoard.newPickLimit
			l.advance(s.at.UTC().Truncate(15 * time.Minute))
			l.counts[2] = newPickCount{picked: 1}
		}, 3},
		{"target priority", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) { f.repo.accounts[1].Priority++ }, 1},
		{"target recent full", func(f *selectionRuntimeFixture, s *newPickTestSnapshot) { f.svc.selectionFullMarks.Mark(2, s.at) }, 1},
		{"outside available", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) {
			f.loads.loadMap = map[int64]*AccountLoadInfo{2: {LoadRate: 100}}
		}, 1},
		{"snapshot incomplete", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.complete = false }, 1},
		{"snapshot ten minutes", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) { s.at = s.at.Add(-10 * time.Minute) }, 2},
		{"snapshot ten minutes one second", func(_ *selectionRuntimeFixture, s *newPickTestSnapshot) {
			s.at = s.at.Add(-10*time.Minute - time.Second)
		}, 1},
		{"global off", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) { f.view.Enabled = false }, 1},
		{"group off", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) {
			f.svc.selectionBoard.view = func() schedquality.View { return newPickGroupOff{f.view} }
		}, 1},
		{"target trial", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) {
			selectionSetTrial(f, 2, newSelectionTrialLeaseFake())
		}, 1},
		{"target rear", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) {
			f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
		}, 1},
		{"base exempt", func(f *selectionRuntimeFixture, _ *newPickTestSnapshot) {
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{AllOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt}}
		}, 1},
		{"target exempt", func(f *selectionRuntimeFixture, s *newPickTestSnapshot) {
			f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt, Expires: s.at.Add(time.Second)}}
		}, 1},
		{"target exemption expired", func(f *selectionRuntimeFixture, s *newPickTestSnapshot) {
			f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{GroupOverride: schedquality.OverrideRef{Mode: schedquality.OverrideExempt, Expires: s.at}}
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := newPickFixture(t)
			if tc.change != nil {
				tc.change(f, s)
			}
			require.Equal(t, tc.want, f.selectNow(t, context.Background(), "session", nil).Account.ID)
		})
	}
}

type newPickGroupOff struct{ schedquality.View }

func (newPickGroupOff) GroupEnabled(int64) bool { return false }

func TestNewPickSameStandingModelAndPrice(t *testing.T) {
	for _, tc := range []struct {
		name         string
		base, target selectionKey
		model        string
		want         bool
	}{
		{"pinned", selectionKey{Pinned: true}, selectionKey{}, "m", false},
		{"actual model", selectionKey{}, selectionKey{}, "mapped", false},
		{"more expensive", selectionKey{RateKnown: true, Rate: 1}, selectionKey{RateKnown: true, Rate: 1.01}, "m", false},
		{"unknown target", selectionKey{RateKnown: true}, selectionKey{}, "m", false},
		{"equal", selectionKey{RateKnown: true, Rate: 1}, selectionKey{RateKnown: true, Rate: 1}, "m", true},
		{"cheaper", selectionKey{RateKnown: true, Rate: 1}, selectionKey{RateKnown: true, Rate: .99}, "m", true},
		{"unknown base", selectionKey{}, selectionKey{RateKnown: true, Rate: 2}, "m", true},
		{"both unknown", selectionKey{}, selectionKey{}, "m", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newPickAccount{&Account{ID: 1}, tc.base, "m"}
			target := newPickAccount{&Account{ID: 2}, tc.target, tc.model}
			require.Equal(t, tc.want, b.permits(target))
		})
	}
}

func TestNewPickSlotFallbackAndBinding(t *testing.T) {
	for _, tc := range []struct {
		result                    string
		want                      int64
		attempts, early, released []int64
	}{
		{"target_recheck_failed", 1, []int64{1}, nil, []int64{1}},
		{"target_slot_busy", 1, []int64{1, 2}, nil, []int64{1}},
		{"target_vetoed", 1, []int64{1, 2}, []int64{2}, []int64{2, 1}},
		{"picked", 2, []int64{1, 2}, []int64{1}, []int64{1, 2}},
	} {
		t.Run(tc.result, func(t *testing.T) {
			logs := captureSchedObsLogs(t)
			f, s := newPickFixture(t)
			s.scores[3] = 0 // Never try another alternative after choosing 2.
			switch tc.result {
			case "target_recheck_failed":
				f.loads.after = func(id int64) {
					if id == 1 {
						f.repo.accounts[1].Schedulable = false
					}
				}
			case "target_slot_busy":
				f.loads.acquireResults = map[int64]bool{2: false}
			case "target_vetoed":
				f.loads.after = func(id int64) {
					if id == 2 {
						f.repo.accounts[1].Schedulable = false
					}
				}
			}
			leases := newSelectionTrialLeaseFake()
			f.svc.selectionBoard.leases = leases
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			result := selectTrialRuntime(t, f, ctx, "test-session")
			require.Equal(t, tc.want, result.Account.ID)
			require.Equal(t, tc.attempts, f.attempts)
			require.Equal(t, tc.early, f.releases)
			require.Zero(t, f.cache.sessionBindings["openai:test-session"])
			CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
			result.ReleaseFunc()
			require.Equal(t, tc.released, f.releases)
			require.Equal(t, tc.want, f.cache.sessionBindings["openai:test-session"])
			require.Empty(t, leases.held)
			count := f.svc.selectionBoard.newPickLimit.counts[2]
			require.Zero(t, count.pending)
			require.Equal(t, tc.result == "picked", count.picked == 1)
			var events int
			for _, entry := range schedObsLogRecords(t, logs) {
				if entry["msg"] != "sched_quality.new_pick" {
					continue
				}
				events++
				require.Equal(t, tc.result, entry["result"])
				require.Equal(t, resilience.StateFrom(ctx).GatewayRequestID(), entry["gateway_request_id"])
				require.Equal(t, "sched_quality", entry["component"])
				for _, field := range []string{"group_id", "model", "from", "to", "from_score", "to_score", "snapshot_at", "first15", "used"} {
					require.Contains(t, entry, field)
				}
				require.NotContains(t, entry, "session")
			}
			require.Equal(t, 1, events)
			if tc.result == "picked" {
				next := f.selectNow(t, context.Background(), "test-session", nil)
				require.True(t, next.stickySessionHit)
				require.EqualValues(t, 2, next.Account.ID)
			}
		})
	}
}

func TestNewPickTrialReservation(t *testing.T) {
	f, _ := newPickFixture(t)
	leases := newSelectionTrialLeaseFake()
	selectionSetTrial(f, 1, leases)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	selected := selectTrialRuntime(t, f, ctx, "session")
	require.EqualValues(t, 1, selected.Account.ID)
	require.Len(t, leases.held, 1)
	CompleteOpenAIStickyChange(ctx, 1, false, false)
	selected.ReleaseFunc()
	require.Empty(t, leases.held)
	require.Equal(t, []int64{1}, f.releases)
}

func TestNewPickCancellation(t *testing.T) {
	f, _ := newPickFixture(t)
	ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
	defer cancel()
	f.loads.after = func(id int64) {
		if id == 2 {
			cancel()
		}
	}
	selection, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "session", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, selection)
	require.ElementsMatch(t, []int64{1, 2}, f.releases)
	require.Equal(t, newPickCount{}, f.svc.selectionBoard.newPickLimit.counts[2])
}

func TestNewPickRequestGuards(t *testing.T) {
	for _, mode := range []string{"bound", "no session", "excluded", "protected", "previous", "websocket", "compact", "manual", "exempt", "failed", "queue", "non-text", "borrow", "quality", "primary error", "legacy error", "legacy owner", "disabled fallback legacy owner", "disabled fallback legacy absent"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newPickFixture(t)
			session := "s"
			previous := ""
			transport := OpenAIUpstreamTransportHTTPSSE
			var excluded map[int64]struct{}
			state := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), state)
			switch mode {
			case "bound":
				f.cache.sessionBindings["openai:s"] = 1
			case "no session":
				session = ""
			case "excluded":
				excluded = map[int64]struct{}{99: {}}
			case "protected":
				ctx = WithOpenAITransferProtected(ctx)
			case "previous":
				previous = "response-id"
			case "websocket":
				ctx = withOpenAIStickyMigrationGuard(ctx, "", OpenAIUpstreamTransportResponsesWebsocketV2)
			case "manual":
				ctx = WithOpenAISelectionManualOnly(ctx)
			case "exempt":
				ctx = withOpenAISelectionExempt(ctx)
			case "queue":
				ctx = WithOpenAISelectionNoWait(ctx)
			case "non-text":
				state.EnableGatewayRequest(time.Now(), resilience.GatewayRequestScopeImage)
			case "borrow":
				state.KeepSticky(99)
			case "quality":
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, &openAIQualityRequest{group: 7, session: session, loaded: true, state: OpenAIQualityState{Generation: 1}})
			case "failed":
				state.MarkAccountFailed(99)
			case "primary error", "legacy error", "legacy owner", "disabled fallback legacy owner", "disabled fallback legacy absent":
				cache := &newPickReadCache{schedulerTestGatewayCache: f.cache.schedulerTestGatewayCache, OpenAIStickyVersionedCache: f.cache}
				switch mode {
				case "primary error":
					cache.fail = "openai:s"
				case "legacy error":
					cache.fail = "openai:old"
				case "legacy owner", "disabled fallback legacy owner":
					cache.sessionBindings["openai:old"] = 1
				}
				f.svc.cache = cache
				f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = mode != "disabled fallback legacy owner" && mode != "disabled fallback legacy absent"
				ctx = withOpenAILegacySessionHash(ctx, "old")
			}
			result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), previous, session, "m", excluded, transport, OpenAIEndpointCapabilityResponses, mode == "compact", false, true, PlatformOpenAI)
			require.NoError(t, err)
			want := int64(1)
			if mode == "disabled fallback legacy absent" {
				want = 2
			}
			require.Equal(t, want, result.Account.ID)
			result.ReleaseFunc()
		})
	}
}

type newPickReadCache struct {
	OpenAIStickyVersionedCache
	schedulerTestGatewayCache
	fail string
}

func (c *newPickReadCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	id, err := c.GetSessionAccountID(ctx, group, key)
	return StickySessionBinding{AccountID: id}, err
}

func (c *newPickReadCache) GetSessionAccountID(ctx context.Context, group int64, key string) (int64, error) {
	if key == c.fail {
		return 0, errors.New("read failed")
	}
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, group, key)
}

func TestNewPickWindowAndCappedEvent(t *testing.T) {
	logs := captureSchedObsLogs(t)
	f, s := newPickFixture(t)
	now := s.at.UTC().Truncate(15 * time.Minute).Add(14*time.Minute + 59*time.Second)
	f.svc.selectionBoard.now = func() time.Time { return now }
	s.at = now
	f.view.ViewAt = now
	for i, want := range []int64{2, 1, 1, 2} {
		if i == 3 {
			now = now.Add(time.Second)
		}
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		selection := selectTrialRuntime(t, f, ctx, "session")
		require.Equal(t, want, selection.Account.ID)
		// No binding is committed. The next logical request is independently eligible.
		CompleteOpenAIStickyChange(ctx, want, false, false)
		selection.ReleaseFunc()
		retry := selectTrialRuntime(t, f, ctx, "session")
		require.EqualValues(t, 1, retry.Account.ID)
		retry.ReleaseFunc()
	}
	var capped int
	for _, entry := range schedObsLogRecords(t, logs) {
		if entry["msg"] == "sched_quality.new_pick" && entry["result"] == "capped" {
			capped++
			require.EqualValues(t, 1, entry["used"])
		}
	}
	require.Equal(t, 2, capped)
}

func TestNewPickConcurrentSessions(t *testing.T) {
	f, _ := newPickFixture(t)
	start := make(chan struct{})
	results := make(chan int64, 2)
	var wg sync.WaitGroup
	for _, session := range []string{"session-a", "session-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), Platform: PlatformOpenAI, RequestedModel: "m", RequiredCapability: OpenAIEndpointCapabilityResponses, SessionHash: session}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			ctx = context.WithValue(ctx, openAISelectionRequestKey{}, req)
			ctx = beginNewPickSelection(ctx)
			resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{kind: "create", session: session, groupID: 7, newPickMissing: true})
			base := &f.repo.accounts[0]
			<-start
			result := f.svc.newSessionPick(ctx, req, base, []*Account{&f.repo.accounts[1]}, nil)
			if result == nil {
				results <- base.ID
			} else {
				pass := newPickPassFrom(ctx)
				defer pass.release()
				if err := pass.deliver(ctx, result); err != nil {
					t.Error(err)
					return
				}
				results <- result.Account.ID
				result.ReleaseFunc()
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var ids []int64
	for id := range results {
		ids = append(ids, id)
	}
	require.ElementsMatch(t, []int64{1, 2}, ids)
	require.Equal(t, newPickCount{picked: 1}, f.svc.selectionBoard.newPickLimit.counts[2])
}

func TestNewPickPublishedSnapshot(t *testing.T) {
	f, _ := newPickFixture(t)
	at := f.view.At()
	engine := NewSchedQualityEngineService(nil, nil, nil, func() time.Time { return at }, nil)
	engine.snapshot.Store(&SchedQualitySnapshot{View: f.view})
	board := ProvideOpenAISelectionBoard(f.svc, engine)
	board.now = func() time.Time { return at }
	require.EqualValues(t, 1, f.selectNow(t, context.Background(), "before-publication", nil).Account.ID)

	facts := map[string]SchedPerfFact{}
	for account := int64(1); account <= 2; account++ {
		for i := 0; i < 20; i++ {
			key := strconv.Itoa(len(facts))
			fact := SchedPerfFact{ID: key, Session: "session-" + key, Account: account, Group: 7, Model: "m", First: true, Stream: true, TTFO: 1000, Start: at.Add(-time.Minute).UnixMicro(), Finish: at.Add(-time.Second).UnixMicro(), Outcome: schedquality.OutcomeOK}
			if account == 1 {
				fact.TTFO = 3000
			}
			facts[key] = fact
		}
	}
	snapshot := buildSchedPerf(facts, nil, map[int64]SchedPolicyRecord{7: {Policy: schedquality.Policy{Enabled: true, TTFTSeconds: 2}}}, nil, at)
	engine.performance.Store(snapshot)
	from, ok := snapshot.ScoreMicros(1, "m")
	require.True(t, ok)
	to, ok := snapshot.ScoreMicros(2, "m")
	require.True(t, ok)
	logs := captureSchedObsLogs(t)
	require.EqualValues(t, 2, f.selectNow(t, context.Background(), "after-publication", nil).Account.ID)
	for _, e := range schedObsLogRecords(t, logs) {
		if e["msg"] == "sched_quality.new_pick" {
			require.EqualValues(t, from, e["from_score"])
			require.EqualValues(t, to, e["to_score"])
			require.Equal(t, at.Format(time.RFC3339Nano), e["snapshot_at"])
			return
		}
	}
	t.Fatal("missing new_pick for published snapshot")
}

func TestNewPickDoubleMiss(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fallback     bool
		primary, old error
		oldOwner     int64
		want         bool
	}{
		{"both missing", true, ErrStickySessionNotFound, ErrStickySessionNotFound, 0, true},
		{"primary ambiguous", true, nil, ErrStickySessionNotFound, 0, false},
		{"primary failed", true, errors.New("primary"), ErrStickySessionNotFound, 0, false},
		{"old ambiguous", true, ErrStickySessionNotFound, nil, 0, false},
		{"old failed", true, ErrStickySessionNotFound, errors.New("old"), 0, false},
		{"disabled old bound", false, ErrStickySessionNotFound, nil, 10, false},
		{"disabled double miss", false, ErrStickySessionNotFound, ErrStickySessionNotFound, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newPickFixture(t)
			cache := &newPickEvidenceCache{selectionRuntimeCache: f.cache, primary: tc.primary, old: tc.old, oldOwner: tc.oldOwner}
			f.svc.cache = cache
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = tc.fallback
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
			ctx := beginNewPickSelection(withOpenAILegacySessionHash(resilience.WithRequestState(context.Background(), resilience.NewRequestState()), "old"))
			ctx = context.WithValue(ctx, openAISelectionRequestKey{}, OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequiredCapability: OpenAIEndpointCapabilityResponses})
			f.svc.prepareOpenAIStickyChange(ctx, ptrInt64Selection(7), "session")
			change := pendingOpenAIStickyChange(ctx)
			require.NotNil(t, change)
			require.Equal(t, tc.want, change.newPickMissing)
			require.Equal(t, "openai:old", change.expected.LegacyKey)
		})
	}
}

type newPickEvidenceCache struct {
	*selectionRuntimeCache
	primary, old error
	oldOwner     int64
}

func (c *newPickEvidenceCache) GetSessionBinding(_ context.Context, _ int64, key string) (StickySessionBinding, error) {
	if key == "openai:old" {
		return StickySessionBinding{AccountID: c.oldOwner}, c.old
	}
	return StickySessionBinding{}, c.primary
}
func (c *newPickEvidenceCache) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	binding, err := c.GetSessionBinding(ctx, g, key)
	return binding.AccountID, err
}
func (c *newPickEvidenceCache) ResolveSessionBinding(ctx context.Context, g int64, primary, old string, fallback bool) (StickySessionBinding, error) {
	if fallback {
		return c.GetSessionBinding(ctx, g, old)
	}
	return StickySessionBinding{}, ErrStickySessionNotFound
}

func TestNewPickQualityBatchReadFailure(t *testing.T) {
	s := &OpenAIGatewayService{cache: &qualityFailureStore{}}
	ctx := context.WithValue(withOpenAIQualityRouting(context.Background()), openAIQualityContextKey{}, &openAIQualityRequest{group: 7, session: "session"})
	id, missing, err := s.lookupStickySessionAccount(ctx, ptrInt64Selection(7), "session")
	require.NoError(t, err) // preserve the ordinary fail-open lookup
	require.Zero(t, id)
	require.False(t, missing)
}

func TestNewPickRevalidatesStanding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*selectionRuntimeFixture)
	}{
		{"base priority changes", func(f *selectionRuntimeFixture) { f.repo.accounts[0].Priority++ }},
		{"target price increases", func(f *selectionRuntimeFixture) { f.repo.accounts[1].RateMultiplier = floatPtr(2) }},
		{"target moved to trial", func(f *selectionRuntimeFixture) { selectionSetTrial(f, 2, newSelectionTrialLeaseFake()) }},
		{"base excluded by policy", func(f *selectionRuntimeFixture) { f.view.Enabled = false }},
		{"target marked full", func(f *selectionRuntimeFixture) { f.svc.selectionFullMarks.Mark(2, f.svc.selectionNow()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newPickFixture(t)
			f.repo.accounts[0].RateMultiplier = floatPtr(1)
			f.repo.accounts[1].RateMultiplier = floatPtr(1)
			f.repo.accounts[2].RateMultiplier = floatPtr(1)
			f.loads.after = func(id int64) {
				if id == 2 {
					tc.mutate(f)
				}
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			r := selectTrialRuntime(t, f, ctx, "session")
			require.EqualValues(t, 1, r.Account.ID)
			require.Equal(t, []int64{1, 2}, f.attempts)
			require.Equal(t, []int64{2}, f.releases)
			r.ReleaseFunc()
			require.Equal(t, newPickCount{}, f.svc.selectionBoard.newPickLimit.counts[2])
		})
	}
}

func TestNewPickReservationAcrossWindow(t *testing.T) {
	c := newPickCandidate{target: newPickAccount{account: &Account{ID: 5}}, first15: 4, recent: SchedPerfRecent{First30: 3, First10: 1}}
	old := time.Unix(0, 0).UTC()
	next := old.Add(15 * time.Minute)
	for _, alreadyUsed := range []bool{false, true} {
		var limit newPickLimit
		reserve := func(window time.Time) *newPickReservation {
			t.Helper()
			ctx := beginNewPickSelection(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
			_, r, used := limit.reserve(newPickOpportunity(ctx), []newPickCandidate{c}, 30_000, window)
			require.NotNil(t, r)
			require.Equal(t, 1, used)
			return r
		}
		r := reserve(old)
		if alreadyUsed {
			require.NoError(t, reserve(next).commit(context.Background()))
		}
		ok := r.confirm(context.Background(), next, 4)
		require.Equal(t, !alreadyUsed, ok)
		if ok {
			require.NoError(t, r.commit(context.Background()))
		} else {
			r.release()
		}
		require.Equal(t, newPickCount{picked: 1}, limit.counts[5])
	}
}

type newPickHydrationCache struct {
	snapshotHydrationCache
	targetReads int
}

func (c *newPickHydrationCache) GetAccount(ctx context.Context, id int64) (*Account, error) {
	row, err := c.snapshotHydrationCache.GetAccount(ctx, id)
	if id == 2 {
		c.targetReads++
		if c.targetReads > 1 {
			other := *row
			other.GroupIDs = []int64{99}
			return &other, nil
		}
	}
	return row, err
}

func TestNewPickUsesAdmittedTargetRow(t *testing.T) {
	f, _ := newPickFixture(t)
	base, target := &f.repo.accounts[0], &f.repo.accounts[1]
	cache := &newPickHydrationCache{snapshotHydrationCache: snapshotHydrationCache{accounts: map[int64]*Account{1: base, 2: target}}}
	f.svc.accountRepo = nil
	f.svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, stubOpenAIAccountRepo{}, nil, nil)
	req := OpenAIAccountScheduleRequest{GroupID: ptrInt64Selection(7), Platform: PlatformOpenAI, SessionHash: "session", RequiredCapability: OpenAIEndpointCapabilityResponses, RequestedModel: "m"}
	ctx := context.WithValue(resilience.WithRequestState(context.Background(), resilience.NewRequestState()), openAISelectionRequestKey{}, req)
	resilience.StateFrom(ctx).SetStickyChange(&openAIStickyChange{groupID: 7, session: "session", kind: "create", newPickMissing: true})
	ctx = beginNewPickSelection(ctx)
	r := f.svc.newSessionPick(ctx, req, base, []*Account{target}, nil)
	require.NotNil(t, r)
	pass := newPickPassFrom(ctx)
	defer pass.release()
	require.NoError(t, pass.deliver(ctx, r))
	require.Same(t, target, r.Account)
	require.Equal(t, []int64{7}, r.Account.GroupIDs)
	require.Equal(t, 1, cache.targetReads)
	r.ReleaseFunc()
	require.Equal(t, []int64{2}, f.releases)
}
