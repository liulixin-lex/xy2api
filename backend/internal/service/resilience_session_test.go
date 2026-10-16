//go:build unit

package service

import (
	"context"
	"errors"
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
		{"balance record", Account{Status: StatusActive, Schedulable: true, Extra: resilience.BalanceRecord{State: resilience.BalanceExhausted, Since: now}.Extra()}, resilience.TierHard},
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
