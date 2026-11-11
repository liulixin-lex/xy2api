//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
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
	byGroup   map[int64][]Account
	listedAll bool
}

func (r *groupAccountsRepo) ListAllWithFilters(_ context.Context, _, _, _, _ string, groupID int64, _ string) ([]Account, error) {
	r.listedAll = true
	return r.byGroup[groupID], nil
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

// R4 (fallback-billing design §3): once the primary group recovers, a key
// that was never moved to the fallback group is served by the primary at
// once, while a moved key waits for return_stable_seconds.
func TestFallbackReturnNeverMovedKeyServesTheRecoveredPrimaryAtOnce(t *testing.T) {
	r, _, accounts, moved := newFallbackFixture(t)
	ctx := context.Background()
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, moved), "the primary has no account: key 77 moves")

	accounts.byGroup[10] = []Account{healthyAccount(1)}
	r.lastResortGroups.entries = nil
	never := *moved
	never.ID = 78
	require.Nil(t, r.AvailabilityFallbackGroup(ctx, &never), "a key that never moved returns at once")
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, moved), "the moved key waits for stability")
}

// R4: a moved key stays in the fallback group until the primary has been
// healthy for return_stable_seconds (120 s by default), then returns and is
// forgotten.
func TestFallbackReturnMovedKeyReturnsAfterThePrimaryIsStable(t *testing.T) {
	r, store, accounts, moved := newFallbackFixture(t)
	ctx := context.Background()
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, moved))

	accounts.byGroup[10] = []Account{healthyAccount(1)}
	r.lastResortGroups.entries = nil
	stableFor := func(d time.Duration) {
		store.mu.Lock()
		store.stable[10] = time.Now().Add(-d)
		store.mu.Unlock()
	}
	stableFor(119 * time.Second)
	require.NotNil(t, r.AvailabilityFallbackGroup(ctx, moved), "healthy for 119 s: stay")

	stableFor(121 * time.Second)
	require.Nil(t, r.AvailabilityFallbackGroup(ctx, moved), "healthy for 120 s: return")
	remembered, _ := store.FallbackKey(ctx, 10, moved.ID)
	require.False(t, remembered, "the key is forgotten")
	require.Nil(t, r.AvailabilityFallbackGroup(ctx, moved), "and stays in the primary")
}

// B-stage fallback, operations and profit-preview decisions share the same
// synthetic member rows; no model filter participates in the fallback decision.
func TestAvailabilityFallbackServableMembers(t *testing.T) {
	for _, tc := range []struct {
		name                                             string
		originCost, servingCost                          float64
		originEjected, servingEjected, pinned, guardHeld bool
		originSoft, unsupportedModel, composite          bool
		mixedPlatform                                    string
		wantMove                                         bool
		wantOriginAvailable, wantServingAvailable        int64
	}{
		{name: "profit excludes the origin", originCost: 1, servingCost: 0.7, wantMove: true, wantServingAvailable: 1},
		{name: "quality does not exclude origin", originCost: 0.3, servingCost: 0.7, originEjected: true, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "cheap account soft penalized", originCost: 0.3, servingCost: 0.7, originSoft: true, wantMove: true, wantServingAvailable: 1},
		{name: "origin supports a different model", originCost: 0.3, servingCost: 0.7, unsupportedModel: true, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "serving profit ceiling rejects", originCost: 1, servingCost: 1.6},
		{name: "serving quality remains available", originCost: 1, servingCost: 0.7, servingEjected: true, wantMove: true, wantServingAvailable: 1},
		{name: "pinned but failing origin is servable", originCost: 0.3, servingCost: 0.7, originEjected: true, pinned: true, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "guard held origin is servable", originCost: 0.3, servingCost: 0.7, originEjected: true, guardHeld: true, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "composite preserves cross-platform availability with gates off", originCost: 0.3, servingCost: 0.7, composite: true, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "healthy mixed account keeps anthropic origin", originCost: 0.3, servingCost: 0.7, mixedPlatform: PlatformAnthropic, wantOriginAvailable: 1, wantServingAvailable: 1},
		{name: "healthy mixed account keeps gemini origin", originCost: 0.3, servingCost: 0.7, mixedPlatform: PlatformGemini, wantOriginAvailable: 1, wantServingAvailable: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, repo, key := newFallbackFixture(t)
			origin := key.Group
			serving := r.fallbackGroups.(*fallbackGroupRepo).groups[20]
			origin.RateMultiplier, origin.ProfitMinMargin, origin.ProfitControlEnabled = 1, 0.5, true
			serving.RateMultiplier, serving.ProfitMinMargin, serving.ProfitControlEnabled = 2, 0.25, true
			first, second := healthyAccount(1), healthyAccount(2)
			first.RateMultiplier, second.RateMultiplier = &tc.originCost, &tc.servingCost
			first.Type, second.Type = AccountTypeAPIKey, AccountTypeAPIKey
			if tc.originSoft {
				first.TempUnschedulableUntil = softAccount(1).TempUnschedulableUntil
				first.TempUnschedulableReason = "transient:TRANSIENT HTTP 502"
			}
			if tc.unsupportedModel {
				first.Credentials = map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}}
				require.False(t, first.IsModelSupported("requested-model"))
			}
			if tc.composite {
				origin.Platform, serving.Platform = PlatformComposite, PlatformComposite
				origin.ProfitControlEnabled, serving.ProfitControlEnabled = false, false
			}
			if tc.mixedPlatform != "" {
				origin.Platform, serving.Platform = tc.mixedPlatform, tc.mixedPlatform
				first.Platform, second.Platform = PlatformAntigravity, tc.mixedPlatform
				first.Extra = map[string]any{"mixed_scheduling": true}
			}

			repo.byGroup[10], repo.byGroup[20] = []Account{first}, []Account{second}
			got := r.AvailabilityFallbackGroup(context.Background(), key)
			if tc.wantMove {
				require.NotNil(t, got)
				require.Equal(t, int64(20), got.ID)
				ctx := context.WithValue(context.Background(), ctxkey.Group, got)
				gate := (&OpenAIGatewayService{}).resolveOpenAIProfitControlGate(ctx, &got.ID)
				require.NotNil(t, gate)
				require.InDelta(t, 1.5, gate.threshold, 1e-12, "fallback uses its own price and margin")
				require.True(t, groupMemberServable(got, &second, time.Now(), nil))
			} else {
				require.Nil(t, got)
			}
			explained, err := r.Explain(resilience.WithCapabilityRequest(context.Background(), resilience.CapabilityProtocolResponses, false), 10, "", "")
			require.NoError(t, err)
			require.Equal(t, tc.wantMove, explained.FallbackGroup.WouldMove, "a request-mode explanation uses the fallback member decision")
			first.Groups, second.Groups = []*Group{origin}, []*Group{serving}
			require.Equal(t, tc.composite || tc.originCost <= 0.5, groupMemberUsable(origin, &first, time.Now()), "guard denominator uses scheduler platform eligibility")
			ops := &OpsService{accountRepo: &fallbackOpsAccounts{rows: []Account{first, second}}}
			_, stats, _, _, err := ops.GetAccountAvailabilityStats(context.Background(), "", nil)
			require.NoError(t, err)
			require.Equal(t, tc.wantOriginAvailable, stats[10].AvailableCount)
			require.Equal(t, tc.wantServingAvailable, stats[20].AvailableCount)
		})
	}
}

type fallbackOpsAccounts struct {
	AccountRepository
	rows []Account
}

func (r *fallbackOpsAccounts) ListOpsAccountsForStats(context.Context, string, *int64) ([]Account, error) {
	return r.rows, nil
}

func TestFallbackProfitControlPreview(t *testing.T) {
	for _, tc := range []struct {
		name          string
		margin        float64
		wantPassing   int
		wantCeiling   float64
		probeAge      time.Duration
		mixedPlatform string
	}{
		{"request margin with zero buffer", 0.25, 3, 0.75, 0, ""},
		{"zero margin", 0, 4, 1, 0, ""},
		{"stricter margin", 0.9, 1, 0.1, 0, ""},
		{"fresh probe changes passing count", 0.25, 4, 0.75, time.Minute, ""},
		{"stale probe keeps stored rate", 0.25, 3, 0.75, 3 * time.Hour, ""},
		{"mixed anthropic members", 0.25, 3, 0.75, 0, PlatformAnthropic},
		{"mixed gemini members", 0.25, 3, 0.75, 0, PlatformGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := &Group{ID: 10, Platform: PlatformOpenAI, RateMultiplier: 1, ProfitMinMargin: 0.5, ProfitSafetyBuffer: 0.4}
			rows := make([]Account, 0, 7)
			for i, rate := range []float64{0, 0.3, 0.75, 0.9, -1} {
				a := healthyAccount(int64(i + 1))
				a.RateMultiplier = floatPtr(rate)
				rows = append(rows, a)
			}
			if tc.probeAge > 0 {
				probe := upstreamCostTestAccount(4, UpstreamBillingProbeStatusOK, 0.1, time.Now().Add(-tc.probeAge), 30*time.Minute)
				rows[3].Type, rows[3].Extra = AccountTypeAPIKey, probe.Extra
			}
			disabled := healthyAccount(6)
			disabled.Schedulable = false
			inactive := healthyAccount(8)
			inactive.Status = StatusDisabled
			rows = append(rows, disabled, softAccount(7), inactive)
			if tc.mixedPlatform != "" {
				group.Platform = tc.mixedPlatform
				for i := range rows {
					rows[i].Platform = PlatformAntigravity
					rows[i].Extra = map[string]any{"mixed_scheduling": true}
				}
			}
			repo := &groupAccountsRepo{byGroup: map[int64][]Account{10: rows}}
			svc := NewGroupQualityGateService(nil, repo, &fallbackGroupRepo{groups: map[int64]*Group{10: group}})
			got, err := svc.ProfitControlPreview(context.Background(), 10, &tc.margin)
			require.NoError(t, err)
			require.True(t, repo.listedAll, "members query must include inactive accounts")
			require.Equal(t, 8, got.Members)
			require.Equal(t, 5, got.Schedulable)
			require.Equal(t, tc.wantPassing, got.Passing)
			require.InDelta(t, tc.wantCeiling, got.Ceiling, 1e-12)
			require.False(t, group.ProfitControlEnabled, "preview does not save the group")
			require.Equal(t, 0.4, group.ProfitSafetyBuffer)
		})
	}
}
