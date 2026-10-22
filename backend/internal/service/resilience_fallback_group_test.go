//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// fallbackGroupRepo serves GetByIDLite from a map.
type fallbackGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *fallbackGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if g, ok := r.groups[id]; ok {
		cp := *g
		return &cp, nil
	}
	return nil, ErrGroupNotFound
}

// groupAccountsRepo lists accounts per group.
type groupAccountsRepo struct {
	AccountRepository
	byGroup map[int64][]Account
}

func (r *groupAccountsRepo) ListByGroup(_ context.Context, groupID int64) ([]Account, error) {
	return r.byGroup[groupID], nil
}

func healthyAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
}

func softAccount(id int64) Account {
	until := time.Now().Add(time.Minute)
	return Account{ID: id, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
		TempUnschedulableUntil: &until, TempUnschedulableReason: "transient:TRANSIENT HTTP 502"}
}

func newFallbackFixture(t *testing.T) (*ResilienceService, *memResilienceStore, *groupAccountsRepo, *APIKey) {
	t.Helper()
	withResilienceSettings(t, nil)
	store := newMemResilienceStore()
	r := NewResilienceService(nil, store)
	fb := int64(20)
	groups := &fallbackGroupRepo{groups: map[int64]*Group{
		10: {ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, AvailabilityFallbackGroupID: &fb},
		20: {ID: 20, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: 1.5},
	}}
	accounts := &groupAccountsRepo{byGroup: map[int64][]Account{10: {softAccount(1)}, 20: {healthyAccount(2)}}}
	r.ConfigureFallbackGroups(accounts, groups)
	primaryID := int64(10)
	key := &APIKey{ID: 77, GroupID: &primaryID, Group: groups.groups[10]}
	return r, store, accounts, key
}

func TestAvailabilityFallbackOnlySoftPenalties(t *testing.T) {
	// Spec: primary has only soft-penalised accounts -> served by fallback.
	r, store, _, key := newFallbackFixture(t)
	fb := r.AvailabilityFallbackGroup(context.Background(), key)
	require.NotNil(t, fb)
	require.Equal(t, int64(20), fb.ID)
	remembered, _ := store.FallbackKey(context.Background(), 10, 77)
	require.True(t, remembered)

	// The fallback group itself unavailable: stay in the primary group.
	r2, _, accounts, key2 := newFallbackFixture(t)
	accounts.byGroup[20] = []Account{softAccount(3)}
	require.Nil(t, r2.AvailabilityFallbackGroup(context.Background(), key2))
}

func TestAvailabilityFallbackReturnAndFlapping(t *testing.T) {
	r, store, accounts, key := newFallbackFixture(t)
	ctx := context.Background()
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, key))

	// Primary recovers: a remembered key stays until it is stable.
	accounts.byGroup[10] = []Account{healthyAccount(1)}
	r.lastResortGroups.entries = nil
	fb := r.AvailabilityFallbackGroup(ctx, key)
	require.NotNil(t, fb, "recovered 0 s ago: stay")
	store.mu.Lock()
	store.stable[10] = time.Now().Add(-30 * time.Second)
	store.mu.Unlock()
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, key), "flapping: healthy for 30 s only")

	// Primary fails again: the stability marker is cleared.
	accounts.byGroup[10] = []Account{softAccount(1)}
	r.lastResortGroups.entries = nil
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, key))
	store.mu.Lock()
	_, stable := store.stable[10]
	store.mu.Unlock()
	require.False(t, stable)

	// Healthy for 120 s: fallback_return, the key is forgotten.
	accounts.byGroup[10] = []Account{healthyAccount(1)}
	r.lastResortGroups.entries = nil
	store.mu.Lock()
	store.stable[10] = time.Now().Add(-121 * time.Second)
	store.mu.Unlock()
	require.Nil(t, r.AvailabilityFallbackGroup(ctx, key))
	remembered, _ := store.FallbackKey(ctx, 10, 77)
	require.False(t, remembered)

	// A key never moved stays in a healthy primary.
	other := *key
	other.ID = 78
	require.Nil(t, r.AvailabilityFallbackGroup(ctx, &other))
}

func TestHealthyGroupWithoutMovedKeysSkipsTheStore(t *testing.T) {
	r, store, accounts, key := newFallbackFixture(t)
	accounts.byGroup[10] = []Account{healthyAccount(1)}
	require.Nil(t, r.AvailabilityFallbackGroup(context.Background(), key))
	store.mu.Lock()
	_, marked := store.stable[10]
	store.mu.Unlock()
	require.False(t, marked, "no stability marker without moved keys")

	// Moved within the binding TTL: the store decides again.
	accounts.byGroup[10] = []Account{softAccount(1)}
	r.lastResortGroups.entries = nil
	require.NotNil(t, r.AvailabilityFallbackGroup(context.Background(), key))
	accounts.byGroup[10] = []Account{healthyAccount(1)}
	r.lastResortGroups.entries = nil
	require.NotNil(t, r.AvailabilityFallbackGroup(context.Background(), key), "recovered 0 s ago: stay")
	store.mu.Lock()
	_, marked = store.stable[10]
	store.mu.Unlock()
	require.True(t, marked)
}

func TestAvailabilityFallbackSwitchOffAndSubscription(t *testing.T) {
	r, _, _, key := newFallbackFixture(t)
	withResilienceSettings(t, func(s *resilience.Settings) { s.FallbackGroup.Enabled = false })
	require.Nil(t, r.AvailabilityFallbackGroup(context.Background(), key))

	withResilienceSettings(t, nil)
	sub := *key.Group
	sub.SubscriptionType = SubscriptionTypeSubscription
	key.Group = &sub
	require.Nil(t, r.AvailabilityFallbackGroup(context.Background(), key))
}

func TestValidateAvailabilityFallbackGroup(t *testing.T) {
	b, c := int64(2), int64(1)
	repo := &fallbackGroupRepo{groups: map[int64]*Group{
		1: {ID: 1, Platform: PlatformOpenAI, Status: StatusActive},
		2: {ID: 2, Platform: PlatformOpenAI, Status: StatusActive, AvailabilityFallbackGroupID: &c},
		3: {ID: 3, Platform: PlatformAnthropic, Status: StatusActive},
		4: {ID: 4, Platform: PlatformOpenAI, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription},
		5: {ID: 5, Platform: PlatformOpenAI, Status: StatusActive, AvailabilityFallbackGroupID: &b},
	}}
	svc := &adminServiceImpl{groupRepo: repo}
	ctx := context.Background()
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 1), "own availability fallback")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 2), "cycle", "1 -> 2 -> 1")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 5), "cycle", "1 -> 5 -> 2 -> 1")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 3), "same platform")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 4), "subscription")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, SubscriptionTypeSubscription, 2), "subscription")
	require.ErrorContains(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 99), "not found")
	require.NoError(t, svc.validateAvailabilityFallbackGroup(ctx, 0, PlatformOpenAI, "", 2), "new group: no cycle possible")
	require.NoError(t, svc.validateAvailabilityFallbackGroup(ctx, 3, PlatformOpenAI, "", 1))
}

func TestUsageRecordsOriginGroup(t *testing.T) {
	st := resilience.NewRequestState()
	require.True(t, st.MarkFallback(10))
	require.False(t, st.MarkFallback(11), "at most once")
	usageLog := &UsageLog{}
	resilienceAnnotateUsage(resilience.WithRequestState(context.Background(), st), usageLog)
	require.Equal(t, int64(10), *usageLog.OriginGroupID)
	plain := &UsageLog{}
	resilienceAnnotateUsage(context.Background(), plain)
	require.Nil(t, plain.OriginGroupID)
}

func TestPublishedInstance(t *testing.T) {
	r, _, _, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	require.Same(t, r, Resilience())
}

func TestSoleCandidateIsLastCandidate(t *testing.T) {
	// Staging KA-6: the only account of a group must not be cut at the
	// per-attempt tier; there is nobody to switch to.
	r, _, accounts, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetGroup(20)

	accounts.byGroup[20] = []Account{healthyAccount(2)}
	require.True(t, resilienceLastCandidate(ctx, 2), "single account")

	hard := healthyAccount(3)
	hard.Schedulable = false
	accounts.byGroup[20] = []Account{healthyAccount(2), hard}
	r.lastResortGroups.entries = nil
	require.True(t, resilienceLastCandidate(ctx, 2), "the other account is unusable")

	accounts.byGroup[20] = []Account{healthyAccount(2), softAccount(4)}
	r.lastResortGroups.entries = nil
	require.False(t, resilienceLastCandidate(ctx, 2), "a soft-penalised account may still take the request")

	other := healthyAccount(5)
	other.Platform = PlatformAnthropic
	accounts.byGroup[20] = []Account{healthyAccount(2), other}
	r.lastResortGroups.entries = nil
	require.True(t, resilienceLastCandidate(ctx, 2), "other platforms do not count")
}

func TestAvailabilityFallbackIndependentOfClaudeCodeFallback(t *testing.T) {
	// Spec: availability_fallback_group_id is independent of the
	// Claude-Code-only fallback_group_id.
	ctx := context.Background()
	cc, back := int64(6), int64(1)
	repo := &fallbackGroupRepo{groups: map[int64]*Group{
		1: {ID: 1, Platform: PlatformOpenAI, Status: StatusActive, ClaudeCodeOnly: true, FallbackGroupID: &cc},
		6: {ID: 6, Platform: PlatformOpenAI, Status: StatusActive, FallbackGroupID: &back},
	}}
	svc := &adminServiceImpl{groupRepo: repo}
	require.NoError(t, svc.validateAvailabilityFallbackGroup(ctx, 1, PlatformOpenAI, "", 6),
		"Claude Code fallbacks are not part of the availability chain")

	// At run time the key moves to the availability fallback group, never to
	// the Claude Code one, and the group keeps its Claude Code fallback.
	r, _, _, key := newFallbackFixture(t)
	key.Group.ClaudeCodeOnly, key.Group.FallbackGroupID = true, &cc
	fb := r.AvailabilityFallbackGroup(ctx, key)
	require.NotNil(t, fb)
	require.Equal(t, int64(20), fb.ID)
	moved := CloneAPIKeyWithGroup(key, fb)
	require.Equal(t, int64(20), *moved.GroupID)
	require.Equal(t, int64(6), *key.Group.FallbackGroupID)
	require.True(t, key.Group.ClaudeCodeOnly)

	// A healthy group stays; the Claude Code routing is left to its own path.
	r2, _, accounts2, key2 := newFallbackFixture(t)
	accounts2.byGroup[10] = []Account{healthyAccount(1)}
	key2.Group.ClaudeCodeOnly, key2.Group.FallbackGroupID = true, &cc
	require.Nil(t, r2.AvailabilityFallbackGroup(ctx, key2))
}

func TestSoleCandidateCountsOnlyAccountsThatCanServeTheRequest(t *testing.T) {
	// Review R07: the check used the group's account count, so an account
	// that serves only another model, or already failed in this request,
	// still counted as an alternative and the only real candidate was cut
	// at the per-attempt tier instead of using the whole budget.
	r, _, accounts, _ := newFallbackFixture(t)
	SetResilienceInstance(r)
	t.Cleanup(func() { SetResilienceInstance(nil) })
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	st.SetGroup(20)
	st.NoteTurn(20, "model-a", "")

	onlyB := healthyAccount(6)
	onlyB.Credentials = map[string]any{"model_mapping": map[string]any{"model-b": "model-b"}}
	accounts.byGroup[20] = []Account{healthyAccount(2), onlyB}
	r.lastResortGroups.entries = nil
	require.True(t, resilienceLastCandidate(ctx, 2), "the other account does not serve model-a")

	accounts.byGroup[20] = []Account{healthyAccount(2), healthyAccount(7)}
	r.lastResortGroups.entries = nil
	require.False(t, resilienceLastCandidate(ctx, 2), "an account serving model-a is an alternative")
	st.MarkAccountFailed(7)
	require.True(t, resilienceLastCandidate(ctx, 2), "an account that already failed in this request is not")
}
