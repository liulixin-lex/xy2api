//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type selectionRuntimeRepo struct {
	schedulerTestOpenAIAccountRepo
	lists  int
	onList func(int) ([]Account, error)
}

func (r *selectionRuntimeRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *selectionRuntimeRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	r.lists++
	if r.onList != nil {
		return r.onList(r.lists)
	}
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group, platform)
}
func (r *selectionRuntimeRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type selectionRuntimeCache struct {
	store ResilienceStore
	schedulerTestGatewayCache
	remaining        time.Duration
	ttlKeys, casKeys []string
}

func (c *selectionRuntimeCache) GetSessionAccountIDWithTTL(ctx context.Context, group int64, key string) (int64, time.Duration, error) {
	c.ttlKeys = append(c.ttlKeys, key)
	id, err := c.GetSessionAccountID(ctx, group, key)
	return id, c.remaining, err
}
func (c *selectionRuntimeCache) CommitSessionBinding(ctx context.Context, group int64, key string, expected StickySessionBinding, next int64, ttl time.Duration, _ BindingCommitOptions) (BindingCommitResult, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "commit", next, ttl)
		defer finish()
	}

	c.casKeys = append(c.casKeys, key)
	if c.sessionBindings[key] != expected.AccountID || expected.AccountID == 0 && expected.LegacyKey != "" && c.sessionBindings[expected.LegacyKey] != 0 {
		return BindingCommitResult{Status: BindingChanged, ObservedGen: expected.Generation}, nil
	}
	c.sessionBindings[key] = next
	if expected.LegacyKey != "" && c.sessionBindings[expected.LegacyKey] != 0 {
		c.sessionBindings[expected.LegacyKey] = next
	}
	return BindingCommitResult{Status: BindingCommitted, ObservedGen: expected.Generation, NewGen: expected.Generation + "-next"}, nil
}

func (c *selectionRuntimeCache) ResolveSessionBinding(ctx context.Context, group int64, primary, legacy string, fallback bool) (StickySessionBinding, error) {
	if c.sessionBindings[primary] == 0 && fallback {
		c.sessionBindings[primary] = c.sessionBindings[legacy]
	}
	binding, err := c.GetSessionBinding(ctx, group, primary)
	binding.BothKeysMissing = err == ErrStickySessionNotFound && c.sessionBindings[legacy] == 0
	binding.LegacyKey = legacy
	if c.sessionBindings[legacy] != 0 && binding.AccountID != 0 {
		c.sessionBindings[legacy] = binding.AccountID
	}
	return binding, err
}

func (c *selectionRuntimeCache) SyncSessionBindingAlias(ctx context.Context, _ int64, primary, legacy string, owner int64, _ time.Duration, create bool) error {
	current := c.sessionBindings[primary]
	if current > 0 && (owner == 0 || current == owner) && (create || c.sessionBindings[legacy] != 0) {
		c.sessionBindings[legacy] = current
	}
	return nil
}

func (c *selectionRuntimeCache) DeleteSessionBindingAliases(ctx context.Context, group int64, primary, legacy string, owner int64) (bool, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "delete", owner, 0)
		defer finish()
	}

	current := c.sessionBindings[primary]
	if current == 0 {
		current = c.sessionBindings[legacy]
	}
	if owner != 0 && current != owner {
		return false, nil
	}
	delete(c.sessionBindings, primary)
	delete(c.sessionBindings, legacy)
	return current != 0, nil
}

func (c *selectionRuntimeCache) GetSessionBinding(ctx context.Context, group int64, key string) (StickySessionBinding, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "read", 0, 0)
		defer finish()
	}

	id, err := c.GetSessionAccountID(ctx, group, key)
	return StickySessionBinding{AccountID: id, Generation: "g1"}, err
}

type selectionRuntimeLoads struct {
	schedulerTestConcurrencyCache
	batches int
	after   func(int64)
}

func (c *selectionRuntimeLoads) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.batches++
	return c.schedulerTestConcurrencyCache.GetAccountsLoadBatch(ctx, accounts)
}
func (c *selectionRuntimeLoads) AcquireAccountSlot(ctx context.Context, id int64, limit int, key string) (bool, error) {
	acquired, err := c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, id, limit, key)
	if c.after != nil {
		c.after(id)
	}
	return acquired, err
}

type selectionRuntimeFixture struct {
	svc                *OpenAIGatewayService
	repo               *selectionRuntimeRepo
	cache              *selectionRuntimeCache
	loads              *selectionRuntimeLoads
	view               *fakeSchedView
	attempts, releases []int64
}

func newSelectionRuntimeFixture(t *testing.T, accounts ...Account) *selectionRuntimeFixture {
	t.Helper()
	f := &selectionRuntimeFixture{repo: &selectionRuntimeRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}, cache: &selectionRuntimeCache{schedulerTestGatewayCache: schedulerTestGatewayCache{sessionBindings: map[string]int64{}}, remaining: 29 * time.Minute}}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = true
	f.loads = &selectionRuntimeLoads{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{acquiredIDs: &f.attempts, releasedIDs: &f.releases}}
	f.svc = &OpenAIGatewayService{cfg: cfg, cache: f.cache, accountRepo: f.repo, resilience: NewResilienceService(nil, newMemResilienceStore()), concurrencyService: NewConcurrencyService(f.loads)}
	f.cache.store = f.svc.resilience.store
	now := time.Now()
	f.view = selectionTestView(now)
	board := NewOpenAISelectionBoard(f.svc, func() schedquality.View { return f.view })
	board.now = func() time.Time { return now }
	return f
}
func (f *selectionRuntimeFixture) selectNow(t *testing.T, ctx context.Context, session string, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
	result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", excluded, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, result)
	CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
	t.Cleanup(func() {
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	})
	return result
}

func TestOpenAISelectionRuntimeStandingPriorityRateAndBoard(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint("load_unavailable=", unavailable), func(t *testing.T) {
			a, b, c, d := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 2), selectionFixtureAccount(4, 3)
			a.RateMultiplier, b.RateMultiplier, c.RateMultiplier, d.RateMultiplier = floatPtr(0), floatPtr(.8), floatPtr(.2), floatPtr(0)
			f := newSelectionRuntimeFixture(t, a, b, c, d)
			f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
			f.loads.acquireResults = map[int64]bool{1: true, 2: false, 3: false, 4: false}
			f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 90}, 2: {LoadRate: 0}, 4: {LoadRate: 0}}
			if unavailable {
				f.loads.loadBatchErr = fmt.Errorf("load unavailable")
			}
			bindings := maps.Clone(f.cache.sessionBindings)
			rows, _, err := f.svc.selectionBoard.Board(context.Background(), 7, "m")
			require.NoError(t, err)
			require.Empty(t, f.attempts)
			require.Empty(t, f.svc.selectionFullMarks.at)
			require.Equal(t, bindings, f.cache.sessionBindings)
			var ids []int64
			for _, row := range rows {
				ids = append(ids, row.AccountID)
			}
			require.Equal(t, []int64{3, 2, 4, 1}, ids)
			result := f.selectNow(t, context.Background(), "", nil)
			require.Equal(t, int64(1), result.Account.ID)
			require.Equal(t, ids, f.attempts, "D11 board and actual slot attempt order")
		})
	}
	t.Run("all back still selects", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
		for _, id := range []int64{1, 2} {
			f.view.States[fakeSchedViewKey{7, id, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
		}
		require.Equal(t, int64(1), f.selectNow(t, context.Background(), "", nil).Account.ID)
	})
}

func TestOpenAIOrdinaryNewSessionUsesSortedPickAndBinds(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1))
	selected := f.selectNow(t, context.Background(), "ordinary-new-session", nil)
	require.Equal(t, int64(1), selected.Account.ID)
	require.Equal(t, int64(1), f.cache.sessionBindings["openai:ordinary-new-session"])
	require.Equal(t, []int64{1}, f.attempts)
	require.NotContains(t, logs.String(), "sched_quality.new_pick")
	require.Len(t, selectionNewSessionEvents(t, logs.String()), 1)
}

func selectionNewSessionEvents(t *testing.T, output string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &fields))
		if fields["msg"] == "sched_quality.new_session" {
			events = append(events, fields)
		}
	}
	return events
}

func captureSelectionNewSessionLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func selectOrdinaryNewSessionForTest(t *testing.T, f *selectionRuntimeFixture, ctx context.Context, session string, excluded map[int64]struct{}, useUpstreamTokenCost bool) (*AccountSelectionResult, error) {
	t.Helper()
	if resilience.StateFrom(ctx) == nil {
		ctx = resilience.WithRequestState(ctx, resilience.NewRequestState())
	}
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", excluded,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, useUpstreamTokenCost, PlatformOpenAI)
	return selected, err
}

func TestOpenAINewSessionSelectionLogUsesObservedCandidates(t *testing.T) {
	t.Run("cheapest downgraded account and next price", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		rear, front := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
		rear.RateMultiplier, front.RateMultiplier = floatPtr(.2), floatPtr(.8)
		pinned := selectionFixtureAccount(3, 1)
		pinned.AccountGroups = []AccountGroup{{GroupID: 7, Pinned: true}}
		pinned.RateMultiplier = floatPtr(.1)
		f := newSelectionRuntimeFixture(t, rear, front, pinned)
		f.view.States[fakeSchedViewKey{7, pinned.ID, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
		f.view.States[fakeSchedViewKey{7, rear.ID, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, true)
		require.NoError(t, err)
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Nil(t, selected.selectionContext.request.newSessionOutcome)
		require.Equal(t, resilience.StateFrom(ctx).GatewayRequestID(), event[0]["gateway_request_id"])
		require.Equal(t, float64(2), event[0]["rear_count"])
		require.Equal(t, float64(front.ID), event[0]["selected_account_id"])
		require.Equal(t, "front", event[0]["selected_quality_position"])
		require.Equal(t, "account_stored", event[0]["selected_sort_rate_source"])
		require.Equal(t, float64(rear.ID), event[0]["cheapest_downgraded_account_id"])
		require.Equal(t, .2, event[0]["cheapest_downgraded_rate_multiplier"])
		require.Equal(t, []any{"error"}, event[0]["cheapest_downgraded_reason"])
		require.Equal(t, .8, event[0]["next_higher_rate_multiplier"])
		require.Equal(t, false, event[0]["next_higher_is_downgraded"])
		require.Equal(t, []newSessionCandidateLog{
			{AccountID: 2, Placement: "front", Rank: 1},
			{AccountID: 3, Placement: "rear", Rank: 2, Lines: map[string]string{"error": "rear"}},
			{AccountID: 1, Placement: "rear", Rank: 3, Lines: map[string]string{"error": "rear"}},
		}, requireNewSessionLists(t, event[0], 2))
	})

	t.Run("observed load and slot skips", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1), selectionFixtureAccount(3, 1))
		f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100}}
		f.loads.acquireResults = map[int64]bool{2: false, 3: true}
		ctx := WithOpenAITierInput(context.Background(), nil, APIProtocolAnthropic, "")
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, true)
		require.NoError(t, err)
		defer selected.ReleaseFunc()
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, "messages", event[0]["entry"])
		require.Equal(t, []int64{2, 3}, f.attempts)
		require.Equal(t, float64(1), event[0]["load_full_skipped_count"])
		require.Equal(t, float64(1), event[0]["slot_unavailable_skipped_count"])
		require.Equal(t, []newSessionCandidateLog{
			{AccountID: 3, Placement: "front", Rank: 1},
			{AccountID: 2, Placement: "front", Rank: 2, Skip: []string{"slot_unavailable"}},
			{AccountID: 1, Placement: "front", Rank: 3, Skip: []string{"load_full"}},
		}, requireNewSessionLists(t, event[0], 3))
	})

	t.Run("trial full is counted", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		front, trial := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
		f := newSelectionRuntimeFixture(t, front, trial)
		f.view.States[fakeSchedViewKey{7, trial.ID, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}}
		leases := newSelectionTrialLeaseFake()
		leases.full[trial.ID] = true
		f.svc.selectionBoard.leases = leases
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, false)
		require.NoError(t, err)
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, float64(1), event[0]["trial_full_count"])
		require.Equal(t, float64(0), event[0]["trial_unknown_count"])
		require.Equal(t, newSessionCandidateLog{AccountID: 2, Placement: "trial_full", Rank: 2, Lines: map[string]string{"ttfo": "trial"}}, requireNewSessionLists(t, event[0], 1)[1])
	})

	t.Run("seat read failure is unknown", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		front, trial := selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 1)
		f := newSelectionRuntimeFixture(t, front, trial)
		f.view.States[fakeSchedViewKey{7, trial.ID, "m"}] = schedquality.StateView{TTFO: schedquality.LineView{Trial: true, TrialUntil: f.view.At().Add(schedquality.TrialDuration)}}
		leases := newSelectionTrialLeaseFake()
		leases.roomErr = fmt.Errorf("synthetic room failure")
		f.svc.selectionBoard.leases = leases
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, true)
		require.NoError(t, err)
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, float64(1), event[0]["trial_unknown_count"])
		require.Equal(t, float64(0), event[0]["trial_full_count"])
		require.Equal(t, []any{"unknown"}, event[0]["cheapest_downgraded_reason"])
		require.Equal(t, "trial_unknown", requireNewSessionLists(t, event[0], 1)[1].Placement)
	})

	t.Run("profit ceiling and empty candidate pool", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		account := selectionFixtureAccount(1, 1)
		account.RateMultiplier = floatPtr(.9)
		f := newSelectionRuntimeFixture(t, account)
		ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{
			ceilingOn: true, groupID: 7, platform: PlatformOpenAI, threshold: .5, sell: 1, pricingAt: time.Now(),
		})
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, true)
		require.Error(t, err)
		require.Nil(t, selected)
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, float64(1), event[0]["filtered_profit_ceiling_count"])
		require.Equal(t, "profit_ceiling", event[0]["no_candidate_reason"])
		require.Nil(t, event[0]["selected_account_id"])
		require.Empty(t, requireNewSessionLists(t, event[0], 0))
	})

	t.Run("no candidates is logged once", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		resilience.ProcessNegativeCache().InvalidateGroup(7)
		t.Cleanup(func() { resilience.ProcessNegativeCache().InvalidateGroup(7) })
		f := newSelectionRuntimeFixture(t)
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "ordinary", nil, false)
		require.Error(t, err)
		require.Nil(t, selected)
		event := selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, "no_schedulable_candidate", event[0]["no_candidate_reason"])
		require.Equal(t, float64(0), event[0]["candidate_pool_count"])
		// A different request can return from the negative cache before visiting the pool.
		logs.Reset()
		_, err = selectOrdinaryNewSessionForTest(t, f, context.Background(), "uncached-session", nil, false)
		require.ErrorIs(t, err, errNegativeCached)
		event = selectionNewSessionEvents(t, logs.String())
		require.Len(t, event, 1)
		require.Equal(t, "cached_no_candidate", event[0]["no_candidate_reason"])
		require.Equal(t, float64(0), event[0]["pool_rounds"])
		require.Nil(t, event[0]["candidates"])
		require.Nil(t, event[0]["excluded"])
	})
}

func TestOpenAINewSessionSelectionLogSkipsRetryAndBoundSessions(t *testing.T) {
	t.Run("retry candidate", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		ctx := WithOpenAISelectionCandidateConstraint(context.Background(), 1, "m", 99)
		selected, err := selectOrdinaryNewSessionForTest(t, f, ctx, "ordinary", nil, false)
		require.NoError(t, err)
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		require.Empty(t, selectionNewSessionEvents(t, logs.String()))
	})

	t.Run("same request without an exclusion list", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		for range 2 {
			result, err := selectOrdinaryNewSessionForTest(t, f, ctx, "", nil, false)
			require.NoError(t, err)
			result.ReleaseFunc()
		}
		events := selectionNewSessionEvents(t, logs.String())
		require.Len(t, events, 1)
		require.Equal(t, "not_used", events[0]["selected_sort_rate_source"])
	})

	t.Run("existing sticky owner", func(t *testing.T) {
		logs := captureSelectionNewSessionLog(t)
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
		key := f.svc.openAISessionCacheKey("bound")
		f.cache.sessionBindings[key] = 1
		selected, err := selectOrdinaryNewSessionForTest(t, f, context.Background(), "bound", nil, false)
		require.NoError(t, err)
		require.NotNil(t, selected)
		defer selected.ReleaseFunc()
		require.Empty(t, selectionNewSessionEvents(t, logs.String()))
	})
}

func TestOpenAISelectionCandidateConstraintRechecksAfterSlot(t *testing.T) {
	original := selectionFixtureAccount(10, 1)
	wrongPriority := selectionFixtureAccount(5, 2)
	wrongModel := selectionFixtureAccount(3, 1)
	wrongModel.Credentials = map[string]any{"model_mapping": map[string]any{"m": "other-model"}}
	f := newSelectionRuntimeFixture(t, original, wrongPriority, wrongModel, selectionFixtureAccount(2, 1), selectionFixtureAccount(4, 1))
	f.loads.after = func(id int64) {
		if id == 2 {
			for i := range f.repo.accounts {
				if f.repo.accounts[i].ID == id {
					f.repo.accounts[i].Priority = 2
				}
			}
		}
	}

	ctx := WithOpenAISelectionCandidateConstraint(
		resilience.WithRequestState(context.Background(), resilience.NewRequestState()), 1, "m", original.ID)
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "m", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	require.Equal(t, int64(4), selected.Account.ID)
	require.Equal(t, []int64{2, 4}, f.attempts, "wrong priority/model and original account must be filtered before slot acquisition")
	require.Equal(t, []int64{2}, f.releases, "the slot that fails the post-acquisition constraint is released")
}

func TestOpenAISelectionCandidateFilterRechecksAfterSlot(t *testing.T) {
	original := selectionFixtureAccount(10, 1)
	postSlot := selectionFixtureAccount(2, 1)
	postSlot.Name = "allowed"
	filtered := selectionFixtureAccount(3, 1)
	filtered.Name = "blocked"
	valid := selectionFixtureAccount(4, 1)
	valid.Name = "allowed"
	f := newSelectionRuntimeFixture(t, original, postSlot, filtered, valid)
	f.loads.after = func(id int64) {
		if id == postSlot.ID {
			for i := range f.repo.accounts {
				if f.repo.accounts[i].ID == id {
					f.repo.accounts[i].Name = "blocked"
				}
			}
		}
	}

	ctx := WithOpenAISelectionCandidateConstraint(
		resilience.WithRequestState(context.Background(), resilience.NewRequestState()), 1, "m", original.ID,
		func(account *Account) bool { return account.Name == "allowed" },
	)
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", "", "m", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selected)
	defer selected.ReleaseFunc()
	require.Equal(t, valid.ID, selected.Account.ID)
	require.Equal(t, []int64{postSlot.ID, valid.ID}, f.attempts, "the predicate filters candidates and is repeated after slot acquisition")
	require.Equal(t, []int64{postSlot.ID}, f.releases, "the slot that fails the post-acquisition predicate is released")
}

func TestOpenAISelectionSlotGuardReleasesOnFailureAndTransfersOnSuccess(t *testing.T) {
	t.Run("panic releases once", func(t *testing.T) {
		releases := 0
		require.PanicsWithValue(t, "injected", func() {
			_, _ = withOpenAISelectionSlotGuard(func() { releases++ }, func(func()) (*AccountSelectionResult, error) {
				panic("injected")
			})
		})
		require.Equal(t, 1, releases)
	})

	t.Run("successful result transfers release ownership", func(t *testing.T) {
		releases := 0
		selection, err := withOpenAISelectionSlotGuard(func() { releases++ }, func(release func()) (*AccountSelectionResult, error) {
			return &AccountSelectionResult{Acquired: true, ReleaseFunc: release}, nil
		})
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Zero(t, releases)
		selection.ReleaseFunc()
		selection.ReleaseFunc()
		require.Equal(t, 1, releases)
	})
}

func TestOpenAISelectionRuntimeSessionCacheAndOwnerSlotOrder(t *testing.T) {
	for _, mode := range []string{"immediate", "idle", "migration"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", mode, legacy), func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
				key := "openai:session"
				ctx := context.Background()
				if legacy {
					key = "openai:old"
					ctx = withOpenAILegacySessionHash(ctx, "old")
				}
				f.cache.sessionBindings[key] = 10
				if mode == "idle" {
					f.view.Takeovers[7] = fakeSchedTakeover{Enabled: true, Idle: time.Minute}
					f.cache.remaining = f.svc.openAIStickyBindingTTL(ptrInt64Selection(7)) - 2*time.Minute
				}
				if mode == "migration" {
					f.view.States[fakeSchedViewKey{7, 10, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
				}
				logs := captureSelectionNewSessionLog(t)
				result := f.selectNow(t, ctx, "session", nil)
				require.Empty(t, selectionNewSessionEvents(t, logs.String()))
				require.Equal(t, int64(1), result.Account.ID)
				require.Equal(t, int64(1), f.cache.sessionBindings[key])
				require.Equal(t, []int64{1}, f.attempts, "migration runs before taking the owner slot")
				if legacy {
					require.EqualValues(t, 1, f.cache.sessionBindings["openai:session"])
				}
				for _, actualKey := range append(f.cache.ttlKeys, f.cache.casKeys...) {
					require.Equal(t, "openai:session", actualKey, "all requests use the canonical primary binding")
				}
			})
		}
	}
}

func TestOpenAISelectionRuntimeCandidatePoolAndLatestView(t *testing.T) {
	t.Run("no better member avoids admission pool and loads", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
		f.cache.sessionBindings["openai:session"] = 10
		require.Equal(t, int64(10), f.selectNow(t, context.Background(), "session", nil).Account.ID)
		require.Zero(t, f.loads.batches)
		require.Equal(t, 1, f.repo.lists, "membership is read without building a load pool")
	})
	t.Run("exclude and hard admission and full observation", func(t *testing.T) {
		bad := selectionFixtureAccount(2, 1)
		bad.Schedulable = false
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 0), bad, selectionFixtureAccount(3, 2), selectionFixtureAccount(4, 3))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.loadMap = map[int64]*AccountLoadInfo{3: {LoadRate: 100, CurrentConcurrency: 10}}
		require.Equal(t, int64(4), f.selectNow(t, context.Background(), "session", map[int64]struct{}{1: {}}).Account.ID)
		require.Equal(t, []int64{4}, f.attempts)
		require.True(t, f.svc.selectionFullMarks.Recently(3, f.svc.selectionNow()))
	})
	t.Run("latest view rejects after slot acquisition", func(t *testing.T) {
		f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 5), selectionFixtureAccount(1, 1))
		f.cache.sessionBindings["openai:session"] = 10
		f.loads.after = func(id int64) {
			if id == 1 {
				f.view.States[fakeSchedViewKey{7, 1, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
			}
		}
		require.Equal(t, int64(10), f.selectNow(t, context.Background(), "session", nil).Account.ID)
		require.Equal(t, []int64{1}, f.releases)
		require.Empty(t, f.cache.casKeys)
	})
}

func TestOpenAISelectionRuntimeFullOwnerRetryBorrowQueue(t *testing.T) {
	for _, mode := range []string{"short retry", "borrow", "queue"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newSelectionRuntimeFixture(t, selectionFixtureAccount(10, 1), selectionFixtureAccount(1, 2))
				f.cache.sessionBindings["openai:session"] = 10
				f.loads.acquireResults = map[int64]bool{10: false, 1: mode != "queue"}
				f.loads.after = func(id int64) {
					if mode == "short retry" && id == 10 {
						f.loads.acquireResults[10] = true
					}
				}
				logs := captureSelectionNewSessionLog(t)
				started := time.Now()
				result := f.selectNow(t, context.Background(), "session", nil)
				require.Equal(t, int64(10), f.cache.sessionBindings["openai:session"])
				require.Empty(t, f.cache.casKeys)
				require.Empty(t, selectionNewSessionEvents(t, logs.String()))
				switch mode {
				case "short retry":
					require.True(t, result.Acquired)
					require.Equal(t, int64(10), result.Account.ID)
					require.Equal(t, []int64{10, 10}, f.attempts)
					require.Less(t, time.Since(started), openAIStickyCapacityRetryDuration)
				case "borrow":
					require.True(t, result.Acquired)
					require.True(t, result.StickyCapacitySpillover)
					require.Equal(t, int64(1), result.Account.ID)
					require.GreaterOrEqual(t, time.Since(started), openAIStickyCapacityRetryDuration)
				case "queue":
					require.False(t, result.Acquired)
					require.NotNil(t, result.WaitPlan)
					require.Equal(t, int64(10), result.WaitPlan.AccountID)
					require.GreaterOrEqual(t, time.Since(started), openAIStickyCapacityRetryDuration)
				}
			})
		})
	}
}

func (c *selectionRuntimeCache) RecordSessionBindingFailure(ctx context.Context, group int64, key, session string, expected StickySessionBinding, target int64, ttl time.Duration, resetAt ...time.Time) (int64, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "read", 0, 0)
		defer finish()
	}

	if c.sessionBindings[key] != expected.AccountID {
		return 0, nil
	}
	return time.Now().Add(ttl).UnixMilli(), c.store.AddSessionAvoid(ctx, group, session, target, ttl, resetAt...)
}

func TestSelectionDualWriteNewSession(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			logs := captureSelectionNewSessionLog(t)
			f := newSelectionRuntimeFixture(t, selectionFixtureAccount(1, 1))
			f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = false
			f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = true
			if old {
				f.cache.sessionBindings["openai:alias"] = 1
			}
			r, err := selectOrdinaryNewSessionForTest(t, f, withOpenAILegacySessionHash(selectionReviewContext(), "alias"), "fresh", nil, true)
			require.NoError(t, err)
			r.ReleaseFunc()
			want := 1
			if old {
				want = 0
			}
			require.Len(t, selectionNewSessionEvents(t, logs.String()), want)
		})
	}
}
