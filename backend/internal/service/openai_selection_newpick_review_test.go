//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type newPickReviewRepo struct {
	*selectionRuntimeRepo
	beforeRead func(int64)
}

func (r *newPickReviewRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.beforeRead(id)
	a, err := r.selectionRuntimeRepo.GetByID(ctx, id)
	if a == nil {
		return nil, err
	}
	row := *a // Independent rows, like two database queries.
	return &row, err
}

type newPickReviewSnapshot struct {
	*newPickTestSnapshot
	checks  int
	checked func()
}

func (s *newPickReviewSnapshot) Usable(now time.Time) bool {
	s.checks++
	if s.checks == 2 {
		s.checked()
	}
	return s.newPickTestSnapshot.Usable(now)
}

func TestNewPickCancelBeforeDecision(t *testing.T) {
	f, snap := newPickFixture(t)
	ctx, cancel := context.WithCancel(resilience.WithRequestState(context.Background(), resilience.NewRequestState()))
	defer cancel()
	armed := false
	wrapped := &newPickReviewSnapshot{snap, 0, func() { armed = true }}
	f.svc.selectionBoard.performance = func() (newPickSnapshot, time.Time) { return wrapped, snap.at }
	f.svc.selectionBoard.now = func() time.Time {
		if armed {
			cancel()
		}
		return snap.at
	}
	r, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "late-cancel", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	if r != nil {
		defer r.ReleaseFunc()
	}
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, r)
	require.ElementsMatch(t, []int64{1, 2}, f.releases)
	require.Equal(t, newPickCount{}, f.svc.selectionBoard.newPickLimit.counts[2])
}

type newPickReviewAliasCounter struct {
	*selectionRuntimeCache
	oldReads int
}

func (c *newPickReviewAliasCounter) GetSessionAccountID(ctx context.Context, group int64, key string) (int64, error) {
	if key == "openai:old" {
		c.oldReads++
	}
	return c.selectionRuntimeCache.GetSessionAccountID(ctx, group, key)
}

func TestNewPickDisabledGroupReads(t *testing.T) {
	for _, mode := range []string{"group off", "global off", "stale view"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newPickFixture(t)
			switch mode {
			case "group off":
				f.svc.selectionBoard.view = func() schedquality.View { return newPickGroupOff{f.view} }
			case "global off":
				f.view.Enabled = false
			case "stale view":
				f.view.ViewAt = f.view.ViewAt.Add(-schedquality.ViewMaxAge - time.Second)
			}
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = false
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
			cache := &newPickReviewAliasCounter{selectionRuntimeCache: f.cache}
			f.svc.cache = cache
			r := f.selectNow(t, withOpenAILegacySessionHash(context.Background(), "old"), "session", nil)
			require.EqualValues(t, 1, r.Account.ID)
			require.Zero(t, cache.oldReads)
		})
	}
}

func TestNewPickOnlyOneReservationAcrossTrialReorder(t *testing.T) {
	logs := captureSchedObsLogs(t)
	f, snap := newPickFixture(t)
	f.repo.accounts = append(f.repo.accounts, selectionFixtureAccount(4, 1))
	snap.scores = map[int64]int64{1: 60_000, 2: 0, 3: 50_000, 4: 5_000}
	leases := newSelectionTrialLeaseFake()
	leases.lose[1] = true
	f.svc.selectionBoard.leases = leases
	f.loads.after = func(id int64) {
		if id == 2 {
			f.repo.accounts[1].Schedulable = false
			selectionSetTrial(f, 1, leases)
		}
	}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	r := selectTrialRuntime(t, f, ctx, "one-pick")
	defer r.ReleaseFunc()
	var outcomes []string
	for _, e := range schedObsLogRecords(t, logs) {
		if e["msg"] == "sched_quality.new_pick" {
			outcomes = append(outcomes, e["result"].(string))
		}
	}
	require.Equal(t, []string{"target_vetoed"}, outcomes)
	require.EqualValues(t, 3, r.Account.ID)
	require.Equal(t, []int64{1, 2, 3}, f.attempts)
}

type newPickReviewAvoidError struct {
	ResilienceStore
	key   string
	calls int
}

func (s *newPickReviewAvoidError) SessionAvoidWrittenAt(ctx context.Context, g int64, key string) (map[int64]time.Time, error) {
	if key == s.key {
		s.calls++
		return nil, errors.New("review: avoid read unavailable")
	}
	return s.ResilienceStore.SessionAvoidWrittenAt(ctx, g, key)
}

func TestNewPickAvoidReadIndeterminate(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, key := range []string{"session", firstOutputAvoidSession("session"), stickyChangeAvoidSession("session")} {
		t.Run(key, func(t *testing.T) {
			f, _ := newPickFixture(t)
			store := f.svc.resilience.store
			require.NoError(t, store.AddSessionAvoid(context.Background(), 7, key, 2, time.Minute))
			got, err := store.SessionAvoidWrittenAt(context.Background(), 7, key)
			require.NoError(t, err)
			require.Contains(t, got, int64(2))
			wrapped := &newPickReviewAvoidError{ResilienceStore: store, key: key}
			f.svc.resilience.store = wrapped
			r := f.selectNow(t, context.Background(), "session", nil)
			require.Positive(t, wrapped.calls)
			require.EqualValues(t, 1, r.Account.ID)
		})
	}
}

type newPickReviewZeroBatch struct {
	*selectionRuntimeCache
	OpenAIQualityRoutingStore
}

func (c *newPickReviewZeroBatch) ReadOpenAIQualityRouting(_ context.Context, _ string, _ int64, keys []string) (*OpenAIQualityState, []int64, error) {
	// An owner scalar "0" arrives between the versioned read and MGET.
	// The repository returns this same slice for an absent Redis key.
	c.sessionBindings[keys[0]] = 0
	return nil, []int64{0}, nil
}

func (c *newPickReviewZeroBatch) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	if id, exists := c.sessionBindings[key]; exists {
		return id, nil
	}
	return c.selectionRuntimeCache.GetSessionAccountID(ctx, g, key)
}

func TestNewPickAmbiguousQualityZero(t *testing.T) {
	f, _ := newPickFixture(t)
	cache := &newPickReviewZeroBatch{selectionRuntimeCache: f.cache}
	f.svc.cache = cache
	ctx := context.WithValue(withOpenAIQualityRouting(context.Background()), openAIQualityContextKey{}, &openAIQualityRequest{group: 7, session: "session"})
	ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
	r := selectTrialRuntime(t, f, ctx, "session")
	defer r.ReleaseFunc()
	id, err := cache.GetSessionAccountID(ctx, 7, "openai:session")
	require.NoError(t, err)
	require.Zero(t, id)
	require.EqualValues(t, 1, r.Account.ID)
}

type newPickReviewConcurrencyChange struct {
	*selectionRuntimeRepo
	read bool
}

func (r *newPickReviewConcurrencyChange) GetByID(ctx context.Context, id int64) (*Account, error) {
	a, err := r.selectionRuntimeRepo.GetByID(ctx, id)
	if err != nil || id != 1 || r.read {
		return a, err
	}
	r.read = true
	first := *a
	first.Concurrency = 0 // Row before concurrent update. Cache already sees the new row at post-slot admission.
	first.UpdatedAt = a.UpdatedAt.Add(-time.Second)
	return &first, nil
}

func TestNewPickBaseNeverHadRealSlot(t *testing.T) {
	f, _ := newPickFixture(t)
	b, target := &f.repo.accounts[0], &f.repo.accounts[1]
	b.RateMultiplier, target.RateMultiplier = floatPtr(1), floatPtr(1)
	b.UpdatedAt = time.Now()
	cache := &snapshotHydrationCache{snapshot: []*Account{b, target}, accounts: map[int64]*Account{1: b, 2: target}}
	f.svc.schedulerSnapshot = NewSchedulerSnapshotService(cache, nil, stubOpenAIAccountRepo{}, nil, nil)
	repo := &newPickReviewConcurrencyChange{selectionRuntimeRepo: f.repo}
	f.svc.accountRepo = repo
	ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, ceilingOn: true, threshold: 10, pricingAt: time.Now()})
	r := f.selectNow(t, ctx, "base-no-slot", nil)
	require.True(t, repo.read)
	require.Empty(t, f.attempts)
	require.EqualValues(t, 1, r.Account.ID)
}

func TestNewPickPanicCleanup(t *testing.T) {
	for _, when := range []string{"base held", "both held", "settlement clock"} {
		t.Run(when, func(t *testing.T) {
			f, s := newPickFixture(t)
			lastSlot := int64(0)
			f.loads.after = func(id int64) { lastSlot = id }
			armed := false
			wrapped := &newPickReviewSnapshot{s, 0, func() { armed = true }}
			f.svc.selectionBoard.performance = func() (newPickSnapshot, time.Time) { return wrapped, s.at }
			f.svc.selectionBoard.now = func() time.Time {
				if when == "base held" && lastSlot == 1 || when == "both held" && lastSlot == 2 || when == "settlement clock" && armed {
					panic("interrupted selection")
				}
				return s.at
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			require.PanicsWithValue(t, "interrupted selection", func() { selectTrialRuntime(t, f, ctx, "session") })
			want := []int64{1, 2}
			if when == "base held" {
				want = []int64{1}
			}
			require.Equal(t, want, f.attempts)
			require.ElementsMatch(t, want, f.releases)
			require.Equal(t, newPickCount{}, f.svc.selectionBoard.newPickLimit.counts[2])
		})
	}
}

func TestNewPickReadsEachAccountOnce(t *testing.T) {
	f, _ := newPickFixture(t)
	var ids []int64
	f.svc.accountRepo = &newPickReviewRepo{f.repo, func(id int64) { ids = append(ids, id) }}
	f.loads.after = func(id int64) {
		if id == 2 {
			f.repo.accounts[0].Schedulable = false
		}
	}
	r := f.selectNow(t, context.Background(), "one-read", nil)
	require.EqualValues(t, 2, r.Account.ID)
	require.Equal(t, []int64{1, 2}, ids)
	require.Equal(t, []int64{1}, f.releases)
}

func TestNewPickCompactProtocols(t *testing.T) {
	for _, mode := range []string{"ordinary", "native v2", "legacy compact"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := newPickFixture(t)
			f.svc.cache = &newPickEvidenceCache{selectionRuntimeCache: f.cache, primary: ErrStickySessionNotFound, old: ErrStickySessionNotFound}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			body := []byte(`{"model":"m","stream":true,"prompt_cache_key":"compress-test","input":[{"type":"compaction_trigger"}]}`)
			if mode == "native v2" {
				require.True(t, HasCompactionTriggerInInput(body))
				MarkOpenAINativeCompactionV2(c)
			}
			session := f.svc.GenerateSessionHash(c, body)
			ctx := resilience.WithRequestState(c.Request.Context(), resilience.NewRequestState())
			r, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, mode == "legacy compact", false, true, PlatformOpenAI)
			require.NoError(t, err)
			defer r.ReleaseFunc()
			if mode == "ordinary" {
				require.EqualValues(t, 2, r.Account.ID)
			} else {
				require.EqualValues(t, 1, r.Account.ID)
				require.Equal(t, []int64{1}, f.attempts)
			}
		})
	}
}

func TestNewPickMechanismFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rate   float64
		known  bool
		logged any
	}{{"normal", 1, true, float64(1)}, {"zero", 0, true, float64(0)}, {"unknown", math.NaN(), false, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := newPickFixture(t)
			for i := range f.repo.accounts {
				f.repo.accounts[i].RateMultiplier = floatPtr(tc.rate)
			}
			logs := captureSchedObsLogs(t)
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			r := selectTrialRuntime(t, f, ctx, "fields")
			defer r.ReleaseFunc()
			require.EqualValues(t, 2, r.Account.ID)
			var events int
			for _, e := range schedObsLogRecords(t, logs) {
				if e["msg"] != "sched_quality.new_pick" {
					continue
				}
				events++
				for field, want := range map[string]any{
					"level": "INFO", "component": "sched_quality", "gateway_request_id": resilience.StateFrom(ctx).GatewayRequestID(),
					"group_id": float64(7), "model": "m", "from": float64(1), "to": float64(2), "from_score": float64(30_000), "to_score": float64(0), "result": "picked",
					"from_priority": float64(1), "to_priority": float64(1), "from_rate_known": tc.known, "to_rate_known": tc.known, "from_rate": tc.logged, "to_rate": tc.logged,
					"first10": float64(1), "first30": float64(3), "first15": float64(4), "used": float64(1), "window": s.at.UTC().Truncate(15 * time.Minute).Format(time.RFC3339Nano), "snapshot_at": s.at.Format(time.RFC3339Nano),
				} {
					require.Contains(t, e, field)
					require.Equal(t, want, e[field], field)
				}
			}
			require.Equal(t, 1, events)
		})
	}
}

type newPickResetView struct {
	schedquality.View
	id    int64
	reset time.Time
}

func (v *newPickResetView) ResetAt(id int64) time.Time {
	if id == v.id {
		return v.reset
	}
	return time.Time{}
}

func TestNewPickSnapshotReset(t *testing.T) {
	for _, id := range []int64{1, 2} {
		for _, stage := range []string{"equal", "after", "after acquisition"} {
			t.Run(fmt.Sprintf("%s/account_%d", stage, id), func(t *testing.T) {
				f, s := newPickFixture(t)
				s.at = s.at.Add(-time.Minute)
				view := &newPickResetView{View: f.view, id: id, reset: s.at}
				f.svc.selectionBoard.view = func() schedquality.View { return view }
				if stage == "after" {
					view.reset = s.at.Add(time.Nanosecond)
				}
				if stage == "after acquisition" {
					f.loads.after = func(acquired int64) {
						if acquired == 2 {
							view.reset = s.at.Add(time.Nanosecond)
						}
					}
				}
				r := f.selectNow(t, context.Background(), "reset", nil)
				want := int64(1)
				if stage == "equal" {
					want = 2
				}
				require.Equal(t, want, r.Account.ID)
				if stage == "after acquisition" {
					require.Equal(t, []int64{2}, f.releases)
				}
			})
		}
	}
}

func TestNewPickCrossWindowReleasesTarget(t *testing.T) {
	f, s := newPickFixture(t)
	now := s.at.UTC().Truncate(15 * time.Minute).Add(15*time.Minute - time.Second)
	s.at, f.view.ViewAt = now, now
	f.svc.selectionBoard.now = func() time.Time { return now }
	f.loads.after = func(id int64) {
		if id == 2 {
			now = now.Add(time.Second)
			l := &f.svc.selectionBoard.newPickLimit
			other := new(atomic.Bool)
			other.Store(true)
			c, _, capped := l.reserve(other, []newPickCandidate{{target: newPickAccount{account: &Account{ID: 2}}, first15: 4, recent: SchedPerfRecent{First10: 1, First30: 3}}}, 30_000, now)
			require.NotNil(t, c)
			require.False(t, capped)
			_, ok := l.finish(context.Background(), 2, 4, now, now, true)
			require.True(t, ok)
		}
	}
	r := selectTrialRuntime(t, f, resilience.WithRequestState(context.Background(), resilience.NewRequestState()), "cross")
	require.EqualValues(t, 1, r.Account.ID)
	require.Equal(t, []int64{2}, f.releases)
	r.ReleaseFunc()
	require.Equal(t, []int64{2, 1}, f.releases)
	require.Equal(t, newPickCount{picked: 1}, f.svc.selectionBoard.newPickLimit.counts[2])
}

func TestNewPickRetryWithoutPriorTarget(t *testing.T) {
	f, _ := newPickFixture(t)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	// The first candidate pool read fails before a selection can plan a target.
	members := f.repo.accounts
	f.repo.accounts = nil
	first, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "retry", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.Error(t, err)
	require.Nil(t, first)
	plan := pendingOpenAIStickyChange(ctx)
	require.NotNil(t, plan)
	require.Zero(t, plan.target)
	require.True(t, plan.newPickMissing)
	f.repo.accounts = members
	r := selectTrialRuntime(t, f, ctx, "retry")
	defer r.ReleaseFunc()
	require.EqualValues(t, 1, r.Account.ID)
	require.Equal(t, []int64{1}, f.attempts)
}
