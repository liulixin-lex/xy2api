//go:build unit

package service

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// groupListingRepo adds ListByGroup (active accounts, ignoring cooldowns) to
// the platform mock, like the production repository.
type groupListingRepo struct {
	*mockAccountRepoForPlatform
}

func (r groupListingRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	var out []Account
	for _, a := range r.accounts {
		if a.Status == StatusActive {
			out = append(out, a)
		}
	}
	return out, nil
}

func newLastResortGatewayFixture(t *testing.T, accounts []Account) (*GatewayService, *mockGatewayCacheForPlatform) {
	t.Helper()
	withResilienceSettings(t, nil)
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cache := &mockGatewayCacheForPlatform{}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &GatewayService{
		accountRepo: groupListingRepo{repo},
		groupRepo: &mockGroupRepoForGateway{groups: map[int64]*Group{
			7: {ID: 7, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		}},
		cache:      cache,
		cfg:        cfg,
		resilience: NewResilienceService(nil, newMemResilienceStore()),
	}
	return svc, cache
}

func TestGatewayLastResortSingleAccountTransient(t *testing.T) {
	// Spec: single-account Grok group with a transient error.
	until := time.Now().Add(30 * time.Second)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 11, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "transient:TRANSIENT HTTP 502",
	}})
	groupID := int64(7)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(11), result.Account.ID)
	require.Equal(t, int64(11), st.LastResort())
	require.Nil(t, result.Account.TempUnschedulableUntil, "the served copy carries no soft cooldown")

	// Last resort switched off: the 0.3.0 "no available account".
	withResilienceSettings(t, func(s *resilience.Settings) { s.LastResort.Enabled = false })
	_, err = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGatewayLastResortNeverUsesHardStates(t *testing.T) {
	// Spec: hard state with no alternative.
	until := time.Now().Add(time.Hour)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 12, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: 403 insufficient balance",
	}})
	groupID := int64(7)
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGatewayLastResortPrefersHighestTierThenSoonest(t *testing.T) {
	now := time.Now()
	soon, late := now.Add(10*time.Second), now.Add(50*time.Second)
	svc, _ := newLastResortGatewayFixture(t, []Account{
		{ID: 21, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 1, OverloadUntil: &soon},
		{ID: 22, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, OverloadUntil: &late},
		{ID: 23, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &soon},
	})
	groupID := int64(7)
	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(23), result.Account.ID, "priority 1 first, then the soonest end of the penalty")
}

func TestLastResortTakesAFreeSlotBeforeWaiting(t *testing.T) {
	now := time.Now()
	soon, late := now.Add(10*time.Second), now.Add(50*time.Second)
	svc, _ := newLastResortGatewayFixture(t, []Account{
		{ID: 41, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, OverloadUntil: &soon},
		{ID: 42, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1, OverloadUntil: &late},
	})
	busy := map[int64]bool{41: false}
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: busy})
	groupID := int64(7)
	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(42), result.Account.ID, "the best candidate is busy, the next one is free")
	require.True(t, result.Acquired)

	busy[42] = false
	result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(41), result.Account.ID, "all busy: wait for the best one")
	require.False(t, result.Acquired)
	require.NotNil(t, result.WaitPlan)

	// The OpenAI scheduler does the same.
	withResilienceSettings(t, nil)
	accounts := []Account{
		{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &soon},
		{ID: 82, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &late},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	openaiBusy := map[int64]bool{81: false}
	oa := &OpenAIGatewayService{
		accountRepo:        openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: openaiBusy}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	openaiGroup := int64(9)
	sel, _, err := oa.SelectAccountWithScheduler(context.Background(), &openaiGroup, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(82), sel.Account.ID)
	require.True(t, sel.Acquired)
	openaiBusy[82] = false
	sel, _, err = oa.SelectAccountWithScheduler(context.Background(), &openaiGroup, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(81), sel.Account.ID)
	require.NotNil(t, sel.WaitPlan)
}

func TestGatewayLastResortSkipsAccountsFailedInThisRequest(t *testing.T) {
	until := time.Now().Add(30 * time.Second)
	svc, _ := newLastResortGatewayFixture(t, []Account{{
		ID: 13, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "transient:TRANSIENT HTTP 502",
	}})
	groupID := int64(7)
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "claude-sonnet-4", map[int64]struct{}{13: {}}, "", 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGatewaySoftBoundAccountKeepsBinding(t *testing.T) {
	// Spec: original scheduler during a cooldown keeps the binding.
	cooldown := time.Now().Add(45 * time.Second)
	svc, cache := newLastResortGatewayFixture(t, []Account{
		{ID: 31, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2, RateLimitResetAt: &cooldown},
		{ID: 32, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
	})
	cache.sessionBindings = map[string]int64{"sess": 31}
	groupID := int64(7)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "sess", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(32), result.Account.ID)
	require.Equal(t, int64(31), cache.sessionBindings["sess"], "binding untouched")
	require.Zero(t, cache.deletedSessions["sess"])

	// The handler's bind after admission must not move the session.
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &groupID, "sess", 32))
	require.Equal(t, int64(31), cache.sessionBindings["sess"])
}

func TestGatewayHardBoundAccountRebinds(t *testing.T) {
	// Spec: balance exhaustion moves the session at once.
	until := time.Now().Add(time.Hour)
	svc, cache := newLastResortGatewayFixture(t, []Account{
		{ID: 41, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2,
			TempUnschedulableUntil: &until, TempUnschedulableReason: "balance:exhausted: x"},
		{ID: 42, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
	})
	cache.sessionBindings = map[string]int64{"sess": 41}
	groupID := int64(7)
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "sess", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(42), result.Account.ID)
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &groupID, "sess", 42))
	require.Equal(t, int64(42), cache.sessionBindings["sess"])
}

func TestGatewayStableSessionAnchorDrivesResilienceAcrossTurns(t *testing.T) {
	groupID := int64(7)
	ctx := &SessionContext{ClientIP: "192.0.2.40", UserAgent: "claude-cli/1.0", APIKeyID: 90}
	parseRound := func(t *testing.T, messages []any) *ParsedRequest {
		t.Helper()
		parsed := mustParseSessionHashRequest(t, anthropicSessionBody("You are a coding assistant.", messages, ""), ctx)
		parsed.GroupID = &groupID
		return parsed
	}
	turn1 := parseRound(t, []any{msg("user", "Write a function")})
	turn2 := parseRound(t, []any{msg("user", "Write a function"), msg("assistant", "func answer() {}"), msg("user", "Add error handling")})
	svcForHash := &GatewayService{}
	firstHash := svcForHash.GenerateSessionHash(turn1)
	secondHash := svcForHash.GenerateSessionHash(turn2)
	require.NotEmpty(t, firstHash)
	require.Equal(t, firstHash, secondHash, "different rounds must enter the same selector and failure state")

	t.Run("second round hits original owner", func(t *testing.T) {
		svc, cache := newLastResortGatewayFixture(t, []Account{
			{ID: 101, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
			{ID: 102, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 2},
		})
		cache.sessionBindings = map[string]int64{firstHash: 101}
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, secondHash, "claude-sonnet-4", nil, "", 0)
		require.NoError(t, err)
		require.Equal(t, int64(101), result.Account.ID)
	})

	t.Run("soft penalty serves substitute and keeps owner binding", func(t *testing.T) {
		cooldown := time.Now().Add(45 * time.Second)
		svc, cache := newLastResortGatewayFixture(t, []Account{
			{ID: 201, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2, RateLimitResetAt: &cooldown},
			{ID: 202, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
		})
		cache.sessionBindings = map[string]int64{firstHash: 201}
		ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, secondHash, "claude-sonnet-4", nil, "", 0)
		require.NoError(t, err)
		require.Equal(t, int64(202), result.Account.ID)
		require.Equal(t, int64(201), cache.sessionBindings[firstHash], "soft penalty leaves the original owner binding intact")
		require.Zero(t, cache.deletedSessions[firstHash])
	})

	t.Run("failure streak removes old owner and binds failover", func(t *testing.T) {
		withResilienceSettings(t, nil)
		svc, cache := newLastResortGatewayFixture(t, []Account{
			{ID: 301, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
			{ID: 302, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
		})
		cache.sessionBindings = map[string]int64{firstHash: 301}
		recorder := svc.gatewaySessionRecorder()
		recorder.Failure(context.Background(), groupID, firstHash, 301, resilience.CategoryRateLimit)
		recorder.Failure(context.Background(), groupID, secondHash, 301, resilience.CategoryTransient)
		_, stillBound := cache.sessionBindings[firstHash]
		require.False(t, stillBound, "the threshold failure removes the old binding")
		require.GreaterOrEqual(t, cache.deletedSessions[firstHash], 1)
		require.Contains(t, svc.resilience.sessionAvoid(context.Background(), &groupID, secondHash), int64(301))

		requestCtx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
		result, err := svc.SelectAccountWithLoadAwareness(requestCtx, &groupID, secondHash, "claude-sonnet-4", nil, "", 0)
		require.NoError(t, err)
		require.Equal(t, int64(302), result.Account.ID)
		require.NoError(t, svc.BindStickySessionAfterProfitAdmission(requestCtx, &groupID, secondHash, result.Account.ID))
		require.Equal(t, int64(302), cache.sessionBindings[secondHash], "the failover owner receives the stable key")
	})
}

func TestSessionFailureStreakRebindsAndAvoids(t *testing.T) {
	// Spec: one busy failure keeps the session; the second moves it.
	withResilienceSettings(t, nil)
	store := newMemResilienceStore()
	svc := &GatewayService{cache: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 51}}, resilience: NewResilienceService(nil, store)}
	cache := svc.cache.(*mockGatewayCacheForPlatform)
	rec := svc.gatewaySessionRecorder()
	ctx := context.Background()

	rec.Failure(ctx, 7, "sess", 51, resilience.CategoryRateLimit)
	require.Equal(t, int64(51), cache.sessionBindings["sess"], "one counted failure keeps the binding")

	rec.Failure(ctx, 7, "sess", 51, resilience.CategoryTransient)
	_, bound := cache.sessionBindings["sess"]
	require.False(t, bound, "second failure deletes the binding")
	gid := int64(7)
	avoid := svc.resilience.sessionAvoid(ctx, &gid, "sess")
	require.Contains(t, avoid, int64(51))

	// Uncounted categories never count.
	cache.sessionBindings["s2"] = 52
	for i := 0; i < 3; i++ {
		rec.Failure(ctx, 7, "s2", 52, resilience.CategoryClientError)
		rec.Failure(ctx, 7, "s2", 52, resilience.CategoryCapacityShed)
	}
	require.Equal(t, int64(52), cache.sessionBindings["s2"])

	// A success resets the count.
	cache.sessionBindings["s3"] = 53
	rec.Failure(ctx, 7, "s3", 53, resilience.CategoryRateLimit)
	rec.Success(ctx, 7, "s3", 53)
	rec.Failure(ctx, 7, "s3", 53, resilience.CategoryRateLimit)
	require.Equal(t, int64(53), cache.sessionBindings["s3"])

	// Session handling off: no streaks.
	withResilienceSettings(t, func(s *resilience.Settings) { s.Session.Enabled = false })
	cache.sessionBindings["s4"] = 54
	rec.Failure(ctx, 7, "s4", 54, resilience.CategoryRateLimit)
	rec.Failure(ctx, 7, "s4", 54, resilience.CategoryRateLimit)
	require.Equal(t, int64(54), cache.sessionBindings["s4"])
}

func TestAvoidedAccountExcludedButUsableAsLastResort(t *testing.T) {
	svc, _ := newLastResortGatewayFixture(t, []Account{
		{ID: 61, Platform: PlatformAnthropic, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 2},
		{ID: 62, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 2},
	})
	store := svc.resilience.store
	groupID := int64(7)
	require.NoError(t, store.AddSessionAvoid(context.Background(), 7, "sess", 61, time.Minute))
	svc.resilience.avoidingSessions.Store(sessionMarkKey(7, "sess"), time.Now().Add(time.Minute))

	result, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "sess", "claude-sonnet-4", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(62), result.Account.ID, "the avoided account is skipped while another exists")

	// With the only other account failed in this request, the avoided one serves.
	result, err = svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "sess", "claude-sonnet-4", map[int64]struct{}{62: {}}, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(61), result.Account.ID)
}

func TestOpenAILastResortSingleAccount(t *testing.T) {
	withResilienceSettings(t, nil)
	until := time.Now().Add(45 * time.Second)
	accounts := []Account{{
		ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		RateLimitResetAt: &until,
	}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		resilience:         NewResilienceService(nil, newMemResilienceStore()),
	}
	groupID := int64(9)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	sel, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(71), sel.Account.ID)
	require.Equal(t, int64(71), st.LastResort())

	// Continuations owned by an account keep failing as before.
	_, _, err = svc.SelectAccountWithScheduler(ctx, &groupID, "resp_prev", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.True(t, err == nil || errors.Is(err, ErrNoAvailableAccounts))
}

type openAIGroupListingRepo struct {
	schedulerTestOpenAIAccountRepo
}

func (r openAIGroupListingRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func TestResiliencePenaltyOfTiers(t *testing.T) {
	now := time.Now()
	in := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		name string
		acc  Account
		want resilience.PenaltyTier
	}{
		{"healthy", Account{Status: StatusActive, Schedulable: true}, resilience.TierNone},
		{"admin disabled", Account{Status: StatusActive, Schedulable: false}, resilience.TierHard},
		{"error", Account{Status: StatusError, Schedulable: true}, resilience.TierHard},
		{"429 45s", Account{Status: StatusActive, Schedulable: true, RateLimitResetAt: in(45 * time.Second)}, resilience.TierSoft},
		{"429 10m", Account{Status: StatusActive, Schedulable: true, RateLimitResetAt: in(10 * time.Minute)}, resilience.TierHard},
		{"overload", Account{Status: StatusActive, Schedulable: true, OverloadUntil: in(2 * time.Minute)}, resilience.TierSoft},
		{"transient", Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: in(30 * time.Second), TempUnschedulableReason: "transient:x"}, resilience.TierSoft},
		{"403 cooldown", Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: in(10 * time.Minute), TempUnschedulableReason: "auth:403_cooldown (1/3): x"}, resilience.TierSoft},
		{"oauth 401", Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: in(10 * time.Minute), TempUnschedulableReason: "OAuth 401: x"}, resilience.TierHard},
		{"balance pause", Account{Status: StatusActive, Schedulable: true, TempUnschedulableUntil: in(5 * time.Minute), TempUnschedulableReason: resilience.BalancePauseReason("HTTP 402: x")}, resilience.TierHard},
		{"stale balance record", Account{Status: StatusActive, Schedulable: true, Extra: map[string]any{"resilience_balance": map[string]any{"state": "EXHAUSTED"}}}, resilience.TierNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier, _, _ := resiliencePenaltyOf(&tc.acc, "", now)
			require.Equal(t, tc.want, tier)
		})
	}
}

func TestSessionMarksAreSwept(t *testing.T) {
	r := NewResilienceService(nil, newMemResilienceStore())
	now := time.Now()
	r.markSessionFailures(7, "gone", -time.Second)
	r.markSessionFailures(7, "live", time.Minute)
	r.avoidingSessions.Store(sessionMarkKey(7, "gone"), now.Add(-time.Second))
	r.avoidingSessions.Store(sessionMarkKey(7, "live"), now.Add(time.Minute))
	r.sweepSessionMarks(now)
	for _, m := range []*sync.Map{&r.failingSessions, &r.avoidingSessions} {
		_, gone := m.Load(sessionMarkKey(7, "gone"))
		_, live := m.Load(sessionMarkKey(7, "live"))
		require.False(t, gone, "a session that never returns does not stay in memory")
		require.True(t, live)
	}
}

func TestStreakOwnerLosesItsBinding(t *testing.T) {
	// The owner was soft-penalised at selection, so the binding was kept for
	// it; the failure streak in this request moves the session, and the
	// account that served the request may now take the binding.
	withResilienceSettings(t, nil)
	svc := &GatewayService{cache: &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"sess": 51}}, resilience: NewResilienceService(nil, newMemResilienceStore())}
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.NoteStickyOwner(51)
	st.KeepSticky(51)
	require.False(t, resilienceBindAllowed(ctx, 52))

	rec := svc.gatewaySessionRecorder()
	rec.Failure(ctx, 7, "sess", 51, resilience.CategoryTransient)
	rec.Failure(ctx, 7, "sess", 51, resilience.CategoryTransient)
	require.True(t, st.Avoided(51))
	require.True(t, resilienceBindAllowed(ctx, 52))
	require.True(t, svc.resilienceGatewayBindAllowed(ctx, 52))
}

func TestLastResortGroupSnapshotsDropExpiredEntries(t *testing.T) {
	expiredEntry := lastResortGroupEntry{accounts: []Account{healthyAccount(1)}, expires: time.Now().Add(-time.Second)}
	freshEntry := lastResortGroupEntry{accounts: []Account{healthyAccount(2)}, expires: time.Now().Add(time.Minute)}

	t.Run("write", func(t *testing.T) {
		r := NewResilienceService(nil, newMemResilienceStore())
		r.lastResortGroups.entries = map[int64]lastResortGroupEntry{1: expiredEntry, 2: freshEntry}
		repo := &groupAccountsRepo{byGroup: map[int64][]Account{3: {healthyAccount(3)}}}
		_, err := r.groupAccountsForLastResort(context.Background(), repo, 3)
		require.NoError(t, err)
		require.Equal(t, []int64{2, 3}, lastResortGroupIDs(r), "an expired snapshot must not stay resident")
	})

	t.Run("sweep", func(t *testing.T) {
		r := NewResilienceService(nil, newMemResilienceStore())
		r.lastResortGroups.entries = map[int64]lastResortGroupEntry{1: expiredEntry, 2: freshEntry}
		r.sweepSessionMarks(time.Now())
		require.Equal(t, []int64{2}, lastResortGroupIDs(r), "the periodic sweep must drop groups that are never read again")
	})
}

func lastResortGroupIDs(r *ResilienceService) []int64 {
	r.lastResortGroups.mu.Lock()
	defer r.lastResortGroups.mu.Unlock()
	ids := make([]int64, 0, len(r.lastResortGroups.entries))
	for id := range r.lastResortGroups.entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
