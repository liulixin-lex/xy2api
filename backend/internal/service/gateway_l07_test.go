//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// T2.5 L-07 blocking tests (M4 kickoff §11.4). Account 1 is the sticky owner
// with one slot; account 2 (priority 1) and account 3 (priority 2) take the
// Layer2 spill. "busy" means the owner's slot is taken and its queue is full.

const (
	l07TestGroup = int64(31)
	l07TestModel = "claude-sonnet-4"
)

// l07TestCache records the sticky writes in order and models the L-07 Redis
// state. An owner-hit refresh records the same op with or without L-07.
type l07TestCache struct {
	mockGatewayCacheForPlatform
	mu       sync.Mutex
	ops      []string
	segments map[string]L07BusyState
	failures map[string]L07BusyState
	readErr  error
	writeErr error
	l07Calls int
}

func newL07TestCache(bindings map[string]int64) *l07TestCache {
	return &l07TestCache{mockGatewayCacheForPlatform: mockGatewayCacheForPlatform{sessionBindings: bindings},
		segments: map[string]L07BusyState{}, failures: map[string]L07BusyState{}}
}

func (c *l07TestCache) note(op string) {
	c.mu.Lock()
	c.ops = append(c.ops, op)
	c.mu.Unlock()
}

func (c *l07TestCache) takeOps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.ops
	c.ops = nil
	return out
}

func (c *l07TestCache) SetSessionAccountID(ctx context.Context, g int64, h string, id int64, ttl time.Duration) error {
	c.note(fmt.Sprintf("set %s=%d", h, id))
	return c.mockGatewayCacheForPlatform.SetSessionAccountID(ctx, g, h, id, ttl)
}

func (c *l07TestCache) RefreshSessionTTL(_ context.Context, _ int64, h string, _ time.Duration) error {
	c.note("refresh " + h)
	return nil
}

func (c *l07TestCache) DeleteSessionAccountID(ctx context.Context, g int64, h string) error {
	c.note("del " + h)
	return c.mockGatewayCacheForPlatform.DeleteSessionAccountID(ctx, g, h)
}

func (c *l07TestCache) L07ReadBusy(_ context.Context, _ int64, h string, _ int64) (L07BusyState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.l07Calls++
	if c.readErr != nil {
		return L07BusyState{}, c.readErr
	}
	seg, f := c.segments[h], c.failures[h]
	seg.FailCount, seg.FailEndMs = f.FailCount, f.FailEndMs
	return seg, nil
}

func (c *l07TestCache) L07WriteBusy(_ context.Context, _ int64, h string, owner, first, n int64, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.l07Calls++
	if c.writeErr != nil {
		return c.writeErr
	}
	c.segments[h] = L07BusyState{Owner: owner, FirstBusyMs: first, BusyN: n}
	return nil
}

func (c *l07TestCache) L07RefreshSessionTTLClear(_ context.Context, _ int64, h string, _ time.Duration) (bool, error) {
	c.note("refresh " + h)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.l07Calls++
	_, ok := c.segments[h]
	delete(c.segments, h)
	return ok, nil
}

func (c *l07TestCache) L07Clear(_ context.Context, _ int64, h string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.l07Calls++
	_, ok := c.segments[h]
	delete(c.segments, h)
	return ok, nil
}

type l07Fixture struct {
	svc      *GatewayService
	slots    *probeSlotCache
	cache    *l07TestCache
	limits   *resilienceGateLimits
	store    *memResilienceStore
	accounts []Account
	now      time.Time
}

func l07TestAccounts() []Account {
	mk := func(id int64, priority, concurrency int) Account {
		return Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Priority: priority, Concurrency: concurrency, GroupIDs: []int64{l07TestGroup},
			AccountGroups: []AccountGroup{{GroupID: l07TestGroup}}}
	}
	return []Account{mk(1, 1, 1), mk(2, 1, 4), mk(3, 2, 4)}
}

// newL07Fixture builds the load-aware Anthropic scheduler with the owner
// busy (one request in flight, a full queue of 3) and the session bound to it.
func newL07Fixture(t *testing.T, mutate func([]Account)) *l07Fixture {
	t.Helper()
	withResilienceSettings(t, nil)
	accounts := l07TestAccounts()
	if mutate != nil {
		mutate(accounts)
	}
	repo := &mockAccountRepoForPlatform{accounts: accounts, accountsByID: map[int64]*Account{}}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	cfg := testConfig()
	probeTestSchedulingConfig(cfg)
	slots := newProbeSlotCache(map[int64]int{1: 1})
	slots.waiting[1] = 3
	cache := newL07TestCache(map[string]int64{})
	store := newMemResilienceStore()
	limits := newResilienceGateLimits()
	f := &l07Fixture{slots: slots, cache: cache, limits: limits, store: store, accounts: repo.accounts, now: time.Now()}
	f.svc = &GatewayService{
		accountRepo:        groupListingRepo{repo},
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{l07TestGroup: {ID: l07TestGroup, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(slots),
		sessionLimitCache:  limits,
		resilience:         NewResilienceService(nil, store),
	}
	return f
}

const l07OnRouting = `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":100}}}`

// withPolicies installs a policy service with the given rows over an L-07
// build whose master switch is on. tune adjusts the effective L-07 values.
func (f *l07Fixture) withPolicies(t *testing.T, rows map[string]string, capable bool, kill bool, tune func(*CacheRoutingL07Policy)) *CachePolicyService {
	t.Helper()
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	store.rows["cache_routing_policy"] = l07OnRouting
	for k, v := range rows {
		store.rows[k] = v
	}
	cfg := policyTestConfig()
	cfg.Gateway.CacheFeatures.KillSwitch = kill
	s := NewCachePolicyService(store, cfg, WithCacheM4Policies(nil),
		WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L07: capable}),
		WithCachePolicyClock(func() time.Time { return f.now }))
	require.NoError(t, s.Refresh(context.Background()))
	if tune != nil {
		tune(&s.Snapshot().routingL07)
	}
	f.svc.cachePolicies = s
	return s
}

// padRequests adds in-scope requests to the group budget (A4) so tests of
// other limits are not cut short by it.
func (f *l07Fixture) padRequests(n int) {
	for i := 0; i < n; i++ {
		f.svc.cachePolicies.l07.noteRequest(l07TestGroup, f.now)
	}
}

func (f *l07Fixture) bind(session string, account int64) {
	f.cache.sessionBindings[session] = account
}

func (f *l07Fixture) bound(session string) int64 { return f.cache.sessionBindings[session] }

func newL07Ctx(t *testing.T) (context.Context, *resilience.RequestState) {
	t.Helper()
	st := resilience.NewRequestState()
	return resilience.WithRequestState(context.Background(), st), st
}

func (f *l07Fixture) selectWith(t *testing.T, ctx context.Context, session string, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	gid := l07TestGroup
	result, err := f.svc.SelectAccountWithLoadAwareness(ctx, &gid, session, l07TestModel, excluded, "", 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Account)
	return result
}

func l07Fields(ctx context.Context, skip string) L07TraceFields {
	return L07TraceFieldsFromContext(ctx, skip)
}

// l07SessionInCohort returns a session whose l07-cohort bucket is on the
// requested side of 50.
func l07SessionInCohort(t *testing.T, treatment bool) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		s := fmt.Sprintf("sess-%d", i)
		if (l07Cohort(s) < 50) == treatment {
			return s
		}
	}
	t.Fatal("no session")
	return ""
}

// ---- §11.4 (1) bind points and H3 ----

func TestL07KeepsBusyOwnerAndLayer2BindIsRefused(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx, st := newL07Ctx(t)

	result := f.selectWith(t, ctx, "sess", nil)
	require.Equal(t, int64(2), result.Account.ID, "Layer2 serves the attempt as in the baseline")
	require.True(t, result.Acquired)
	require.Equal(t, int64(1), f.bound("sess"), "Layer2 bind (:781) refused")
	require.Equal(t, int64(1), st.KeptSticky())
	require.Empty(t, f.cache.takeOps(), "no sticky write at all")
	require.Equal(t, L07TraceFields{Arm: "on", Kept: true, Abandon: "none", BusyN: 1, HoldMS: 0}, l07Fields(ctx, "busy_wait_full"))
	require.Equal(t, L07BusyState{Owner: 1, FirstBusyMs: f.now.UnixMilli(), BusyN: 1}, f.cache.segments["sess"])

	// Handler bind points (gateway_handler :489/:843/:1141, chat :277,
	// responses :276, gemini :494/:648) use these two functions.
	gid := l07TestGroup
	require.NoError(t, f.svc.BindStickySession(ctx, &gid, "sess", 2))
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "sess", 2))
	gated := context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{})
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(gated, &gid, "sess", 2))
	delete(f.cache.sessionBindings, "sess") // unreadable binding: the gate path skips the write
	require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(gated, &gid, "sess", 2))
	require.NoError(t, f.svc.BindStickySession(ctx, &gid, "sess", 2))
	require.Zero(t, f.bound("sess"))
	require.Empty(t, f.cache.takeOps())
	// The owner itself may still be bound (refresh of the same owner).
	require.NoError(t, f.svc.BindStickySession(ctx, &gid, "sess", 1))
	require.Equal(t, int64(1), f.bound("sess"))
}

func TestL07RoutedOwnerBusyRefusesRoutedBind(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.svc.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{l07TestGroup: {ID: l07TestGroup, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true,
		ModelRoutingEnabled: true, ModelRouting: map[string][]int64{l07TestModel: {1, 2, 3}}}}}
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx, _ := newL07Ctx(t)
	result := f.selectWith(t, ctx, "sess", nil)
	require.NotEqual(t, int64(1), result.Account.ID)
	require.Equal(t, int64(1), f.bound("sess"), "routed bind (:511) refused")
	require.Empty(t, f.cache.takeOps())
	require.True(t, l07Fields(ctx, "busy_wait_full").Kept)
}

func TestL07LegacyOrderBindIsRefused(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx, _ := newL07Ctx(t)
	// The owner is kept in Layer1.5; the Layer2 load batch then fails and the
	// legacy order (:832) takes a slot.
	f.svc.concurrencyService = NewConcurrencyService(&l07LoadBatchFailing{probeSlotCache: f.slots})
	result := f.selectWith(t, ctx, "sess", nil)
	require.NotEqual(t, int64(1), result.Account.ID)
	require.Equal(t, int64(1), f.bound("sess"), "legacy-order bind (:832) refused")
	require.Empty(t, f.cache.takeOps())
}

type l07LoadBatchFailing struct{ *probeSlotCache }

func (c *l07LoadBatchFailing) GetAccountsLoadBatch(context.Context, []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	return nil, errors.New("load batch down")
}

// The non-load-aware selectors never keep, but their bind points (:2047,
// :2172, :2313, :2439) go through the same refusal while a keep holds.
func TestL07ModelSelectorBindPointsHonorTheKeep(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, bound := range []bool{true, false} {
			t.Run(fmt.Sprint("mixed=", mixed, " bound=", bound), func(t *testing.T) {
				f := newL07Fixture(t, func(a []Account) { a[0].Schedulable = false })
				if bound {
					f.bind("sess", 1) // :2047/:2313 rebinding of an unusable sticky
				}
				ctx, st := newL07Ctx(t)
				require.True(t, st.KeepStickyForAttempt(1))
				gid := l07TestGroup
				var acc *Account
				var err error
				if mixed {
					acc, err = f.svc.selectAccountWithMixedScheduling(ctx, &gid, "sess", l07TestModel, nil, PlatformAnthropic)
				} else {
					acc, err = f.svc.selectAccountForModelWithPlatform(ctx, &gid, "sess", l07TestModel, nil, PlatformAnthropic)
				}
				require.NoError(t, err)
				require.NotEqual(t, int64(1), acc.ID)
				for _, op := range f.cache.takeOps() {
					require.False(t, strings.HasPrefix(op, "set "), "no bind while kept: %s", op)
				}
				st.ReleaseAttemptSticky()
				require.Zero(t, st.KeptSticky())
			})
		}
	}
}

// The handler layer binds only through BindStickySession and
// BindStickySessionAfterProfitAdmission (M4 kickoff §3): lock the inventory.
func TestL07HandlerBindPointInventory(t *testing.T) {
	// [BindStickySession, BindStickySessionAfterProfitAdmission] per file.
	anthropic := map[string][2]int{
		"gateway_handler.go":                  {1, 2},
		"gateway_handler_chat_completions.go": {0, 1},
		"gateway_handler_responses.go":        {0, 1},
		"gemini_v1beta_handler.go":            {1, 1},
	}
	// The OpenAI handler's gatewayService is *service.OpenAIGatewayService,
	// whose binds L-07 never arms.
	other := map[string][2]int{"openai_gateway_handler.go": {0, 4}}
	dir := filepath.Join("..", "handler")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	total := [2]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		src := string(raw)
		require.NotContains(t, src, "SetSessionAccountID(", "%s writes a binding directly", name)
		got := [2]int{strings.Count(src, ".BindStickySession("), strings.Count(src, ".BindStickySessionAfterProfitAdmission(")}
		if w, ok := anthropic[name]; ok {
			require.Equal(t, w, got, name)
			total[0] += got[0]
			total[1] += got[1]
		} else if w, ok := other[name]; ok {
			require.Equal(t, w, got, name)
			require.Contains(t, src, "gatewayService             *service.OpenAIGatewayService")
		} else {
			require.Equal(t, [2]int{}, got, "unexpected bind point in %s", name)
		}
	}
	require.Equal(t, [2]int{2, 5}, total, "7 Anthropic handler bind points")
}

func TestL07StillRebindsForNonBusyReasons(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]Account)
		setup  func(*l07Fixture)
		skip   string
		free   bool
	}{
		{name: "session quota full", skip: "busy_quota_full", free: true, mutate: func(a []Account) {
			a[0].Type, a[0].Extra = AccountTypeOAuth, map[string]any{"max_sessions": 1}
		}, setup: func(f *l07Fixture) { f.limits.full[1] = true }},
		{name: "model unsupported", skip: "model_unsupported", mutate: func(a []Account) {
			a[0].Credentials = map[string]any{"model_mapping": map[string]any{"claude-opus-4": "claude-opus-4"}}
		}},
		{name: "gate failure (quota exceeded)", skip: "gate_check", mutate: func(a []Account) {
			a[0].Extra = map[string]any{"quota_limit": 1.0, "quota_used": 2.0}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newL07Fixture(t, tc.mutate)
			if tc.setup != nil {
				tc.setup(f)
			}
			if tc.free {
				f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
			}
			f.withPolicies(t, nil, true, false, nil)
			f.bind("sess", 1)
			ctx, st := newL07Ctx(t)
			result := f.selectWith(t, ctx, "sess", nil)
			require.NotEqual(t, int64(1), result.Account.ID)
			require.Equal(t, result.Account.ID, f.bound("sess"), "baseline rebind")
			require.Zero(t, st.KeptSticky())
			fields := l07Fields(ctx, tc.skip)
			require.False(t, fields.Kept)
			require.Equal(t, "not_busy_reason", fields.Abandon)
			require.Zero(t, f.cache.l07Calls, "no state read off the busy path")
		})
	}
	// First-event timeout and capacity shed end the owner's attempt; the next
	// attempt excludes it and rebinds as in the baseline.
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	f.bind("sess", 1)
	ctx, st := newL07Ctx(t)
	require.Equal(t, int64(1), f.selectWith(t, ctx, "sess", nil).Account.ID)
	result := f.selectWith(t, ctx, "sess", map[int64]struct{}{1: {}})
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, int64(2), f.bound("sess"))
	require.Zero(t, st.KeptSticky())
}

// ---- §11.4 (2) A1 ----

func TestL07ConsecutiveBusyKeepsOnceThenRebinds(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)

	ctx1, _ := newL07Ctx(t)
	require.Equal(t, int64(2), f.selectWith(t, ctx1, "sess", nil).Account.ID)
	require.Equal(t, int64(1), f.bound("sess"), "1st busy: kept")

	f.now = f.now.Add(18 * time.Second)
	ctx2, st2 := newL07Ctx(t)
	result := f.selectWith(t, ctx2, "sess", nil)
	require.Equal(t, int64(2), result.Account.ID)
	require.Equal(t, int64(2), f.bound("sess"), "2nd busy: consecutive, baseline rebind")
	require.Zero(t, st2.KeptSticky())
	require.Equal(t, L07TraceFields{Arm: "on", Abandon: "consecutive", BusyN: 2, HoldMS: 18000}, l07Fields(ctx2, "busy_wait_full"))
	require.Equal(t, int64(1), f.svc.cachePolicies.L07Counters().AbandonTotal["consecutive"])
	require.Equal(t, int64(1), f.svc.cachePolicies.L07Counters().KeepTotal)
}

func TestL07OwnerSuccessResetsTheSegment(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx1, _ := newL07Ctx(t)
	f.selectWith(t, ctx1, "sess", nil)
	require.Contains(t, f.cache.segments, "sess")

	// The owner is free: the hit refreshes the binding and ends the segment
	// in the same pipeline.
	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	ctx2, _ := newL07Ctx(t)
	require.Equal(t, int64(1), f.selectWith(t, ctx2, "sess", nil).Account.ID)
	require.Equal(t, []string{"refresh sess"}, f.cache.takeOps())
	require.NotContains(t, f.cache.segments, "sess")

	// A new busy period starts from 1 again.
	f.padRequests(100)
	f.slots.inFlight[1], f.slots.waiting[1] = 1, 3
	ctx3, _ := newL07Ctx(t)
	f.selectWith(t, ctx3, "sess", nil)
	require.Equal(t, int64(1), f.bound("sess"))
	require.Equal(t, int64(1), l07Fields(ctx3, "busy_wait_full").BusyN)
}

// ---- §11.4 (3) A2, A3, A4 ----

func TestL07HoldTimeOwnerCapAndGroupBudget(t *testing.T) {
	t.Run("hold time", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.MaxConsecutiveBusy = 5 })
		f.bind("sess", 1)
		f.cache.segments["sess"] = L07BusyState{Owner: 1, FirstBusyMs: f.now.Add(-61 * time.Second).UnixMilli(), BusyN: 1}
		ctx, _ := newL07Ctx(t)
		f.selectWith(t, ctx, "sess", nil)
		require.Equal(t, int64(2), f.bound("sess"))
		require.Equal(t, "hold_time", l07Fields(ctx, "busy_wait_full").Abandon)
	})
	t.Run("owner cap", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) {
			p.OwnerBusySkipPerMinCap, p.GroupKeepRatioPct = 1, 20
		})
		for i := 0; i < 10; i++ { // budget denominator for A4
			f.svc.cachePolicies.l07.noteRequest(l07TestGroup, f.now)
		}
		f.bind("a", 1)
		f.bind("b", 1)
		ctxA, _ := newL07Ctx(t)
		f.selectWith(t, ctxA, "a", nil)
		require.Equal(t, int64(1), f.bound("a"))
		ctxB, _ := newL07Ctx(t)
		f.selectWith(t, ctxB, "b", nil)
		require.Equal(t, int64(2), f.bound("b"))
		require.Equal(t, "owner_cap", l07Fields(ctxB, "busy_wait_full").Abandon)
		// Keeps older than 60s no longer count.
		f.now = f.now.Add(61 * time.Second)
		f.bind("b", 1)
		ctxC, _ := newL07Ctx(t)
		f.selectWith(t, ctxC, "b", nil)
		require.Equal(t, int64(1), f.bound("b"))
	})
	t.Run("owner cap auto is max(4, 2×concurrency)", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.GroupKeepRatioPct = 20 })
		for i := 0; i < 100; i++ {
			f.svc.cachePolicies.l07.noteRequest(l07TestGroup, f.now)
		}
		for i := 0; i < 5; i++ {
			s := fmt.Sprintf("s%d", i)
			f.bind(s, 1)
			ctx, _ := newL07Ctx(t)
			r := f.selectWith(t, ctx, s, nil)
			if i < 4 {
				require.Equal(t, int64(1), f.bound(s), s)
			} else {
				require.Equal(t, r.Account.ID, f.bound(s), "baseline rebind to the Layer2 account")
				require.NotEqual(t, int64(1), f.bound(s))
				require.Equal(t, "owner_cap", l07Fields(ctx, "busy_wait_full").Abandon)
			}
		}
	})
	t.Run("group budget", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, nil) // 5%
		f.bind("a", 1)
		f.bind("b", 1)
		ctxA, _ := newL07Ctx(t)
		f.selectWith(t, ctxA, "a", nil)
		require.Equal(t, int64(1), f.bound("a"), "0 keeps of 1 request")
		ctxB, _ := newL07Ctx(t)
		f.selectWith(t, ctxB, "b", nil)
		require.Equal(t, int64(2), f.bound("b"), "1 keep of 2 requests is 50%")
		require.Equal(t, "group_budget", l07Fields(ctxB, "busy_wait_full").Abandon)
	})
}

// ---- §11.4 (4) H1 ----

func TestL07NeverKeepsAnUnhealthyOwner(t *testing.T) {
	soon := time.Now().Add(45 * time.Second)
	cases := map[string]func([]Account){
		"transient cooldown":  func(a []Account) { a[0].TempUnschedulableUntil, a[0].TempUnschedulableReason = &soon, "transient" },
		"rate limited":        func(a []Account) { a[0].RateLimitResetAt = &soon },
		"overloaded":          func(a []Account) { a[0].OverloadUntil = &soon },
		"temp unschedulable":  func(a []Account) { a[0].TempUnschedulableUntil = &soon },
		"iq check failed":     func(a []Account) { a[0].IQCheck = domain.IQCheck{Enabled: true, Status: "degraded"} },
		"recent failure":      nil,
		"recent failure only": nil,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newL07Fixture(t, mutate)
			f.withPolicies(t, nil, true, false, nil)
			f.bind("sess", 1)
			if strings.HasPrefix(name, "recent failure") {
				window := int64(resilience.Current().Session.StreakWindowSeconds) * 1000
				f.cache.failures["sess"] = L07BusyState{FailCount: 1, FailEndMs: f.now.Add(-30*time.Second).UnixMilli() + window}
			}
			ctx, _ := newL07Ctx(t)
			result := f.selectWith(t, ctx, "sess", nil)
			// A soft-penalised owner may keep its binding by the 0.4.x soft
			// keep; L-07 itself never keeps and never writes its segment.
			require.NotContains(t, f.cache.segments, "sess", "never kept by L-07")
			if f.bound("sess") != 1 {
				require.Equal(t, result.Account.ID, f.bound("sess"))
			}
			require.False(t, l07Fields(ctx, "").Kept)
			if strings.HasPrefix(name, "recent failure") {
				require.Equal(t, "unhealthy", l07Fields(ctx, "busy_wait_full").Abandon)
			}
		})
	}
	t.Run("failure older than the lookback", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.HealthLookbackSeconds = 30 })
		f.bind("sess", 1)
		window := int64(resilience.Current().Session.StreakWindowSeconds) * 1000
		f.cache.failures["sess"] = L07BusyState{FailCount: 1, FailEndMs: f.now.Add(-31*time.Second).UnixMilli() + window}
		ctx, st := newL07Ctx(t)
		f.selectWith(t, ctx, "sess", nil)
		require.Equal(t, int64(1), st.KeptSticky())
	})
	t.Run("penalty tier at the decision", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, nil)
		ctx, _ := newL07Ctx(t)
		gid := int64(l07TestGroup)
		rec := f.svc.l07Begin(ctx, &Group{ID: gid, Platform: PlatformAnthropic}, "sess", PlatformAnthropic, 0)
		owner := f.accounts[0]
		owner.OverloadUntil = &soon
		require.False(t, f.svc.l07OwnerBusy(ctx, rec, &owner, "sess", l07TestModel, &AcquireResult{}, nil, nil))
		require.Equal(t, "unhealthy", l07Fields(ctx, "busy_wait_full").Abandon)
		require.Zero(t, f.cache.l07Calls)
	})
}

// ---- §11.4 (5, 10) H2 and the per-attempt keep ----

func TestL07KeepIsPerAttempt(t *testing.T) {
	run := func(t *testing.T, on bool) ([]int64, []string, *l07Fixture, context.Context) {
		f := newL07Fixture(t, nil)
		if on {
			f.withPolicies(t, nil, true, false, nil)
		}
		f.bind("sess", 1)
		ctx, st := newL07Ctx(t)
		gid := l07TestGroup
		var served []int64
		// Attempt 1: kept, spills to 2, and 2 fails.
		r1 := f.selectWith(t, ctx, "sess", nil)
		served = append(served, r1.Account.ID)
		st.SessionFailure(ctx, r1.Account.ID, resilience.CategoryTransient)
		// Attempt 2: the owner is still busy; H2 never keeps a retry.
		r2 := f.selectWith(t, ctx, "sess", map[int64]struct{}{2: {}})
		served = append(served, r2.Account.ID)
		require.Zero(t, st.KeptSticky(), "released before attempt 2")
		// The handler's success bind (:1141) after attempt 2.
		require.NoError(t, f.svc.BindStickySession(ctx, &gid, "sess", r2.Account.ID))
		return served, f.cache.takeOps(), f, ctx
	}
	baseServed, baseOps, _, _ := run(t, false)
	served, ops, f, ctx := run(t, true)
	require.Equal(t, baseServed, served, "same accounts, attempts and exclusions as the baseline")
	require.Equal(t, []int64{2, 3}, served)
	require.Equal(t, []string{"set sess=2", "set sess=3", "set sess=3"}, baseOps)
	require.Equal(t, []string{"set sess=3", "set sess=3"}, ops, "attempt 1 kept; attempt 2 binds at selection and :1141")
	require.Equal(t, int64(3), f.bound("sess"))
	fields := l07Fields(ctx, "busy_wait_full")
	require.True(t, fields.Kept, "the request kept the owner on attempt 1")
	require.Equal(t, "none", fields.Abandon)
	require.Equal(t, int64(0), f.svc.cachePolicies.L07Counters().AbandonTotal["retry"], "a kept request records no later abandon")
}

func TestL07RetryNeverKeeps(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx, st := newL07Ctx(t)
	// Attempt 1 is a free owner hit; attempt 2 finds it busy.
	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	require.Equal(t, int64(1), f.selectWith(t, ctx, "sess", nil).Account.ID)
	f.slots.inFlight[1], f.slots.waiting[1] = 1, 3
	f.selectWith(t, ctx, "sess", map[int64]struct{}{3: {}})
	require.Zero(t, st.KeptSticky())
	require.Equal(t, int64(2), f.bound("sess"))
	require.Equal(t, "retry", l07Fields(ctx, "busy_wait_full").Abandon)
	// A first selection that already excludes accounts is a retry as well.
	f.bind("s2", 1)
	ctx2, st2 := newL07Ctx(t)
	f.selectWith(t, ctx2, "s2", map[int64]struct{}{3: {}})
	require.Zero(t, st2.KeptSticky())
	require.Equal(t, "retry", l07Fields(ctx2, "busy_wait_full").Abandon)
}

// ---- §11.4 (6) spill failures never delete the owner binding ----

func TestL07SpillFailuresAvoidSpillAndKeepOwner(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.MaxConsecutiveBusy, p.GroupKeepRatioPct = 5, 20 })
	for i := 0; i < 100; i++ {
		f.svc.cachePolicies.l07.noteRequest(l07TestGroup, f.now)
	}
	f.bind("sess", 1)
	for i := 0; i < 2; i++ {
		ctx, st := newL07Ctx(t)
		r := f.selectWith(t, ctx, "sess", nil)
		require.Equal(t, int64(2), r.Account.ID)
		require.Equal(t, int64(1), st.KeptSticky())
		st.SessionFailure(ctx, 2, resilience.CategoryTransient)
		require.Equal(t, int64(1), f.bound("sess"), "the failing spill account is not the owner: no delete")
	}
	gid := l07TestGroup
	require.Contains(t, f.svc.resilience.sessionAvoid(context.Background(), &gid, "sess"), int64(2), "the spill account is avoided")
	require.Zero(t, f.cache.deletedSessions["sess"])
	// The owner is idle again: the session returns to it.
	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	ctx, _ := newL07Ctx(t)
	require.Equal(t, int64(1), f.selectWith(t, ctx, "sess", nil).Account.ID)
}

// ---- §11.4 (7) state failures fall back to the baseline ----

func TestL07StateFailuresAbandon(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*l07Fixture)
	}{
		{"read error", func(f *l07Fixture) { f.cache.readErr = errors.New("redis down") }},
		{"write error", func(f *l07Fixture) { f.cache.writeErr = errors.New("redis down") }},
		{"store missing", func(f *l07Fixture) { f.svc.cache = &f.cache.mockGatewayCacheForPlatform }},
		{"wait count error", func(f *l07Fixture) {
			f.svc.concurrencyService = NewConcurrencyService(&l07WaitCountFailing{probeSlotCache: f.slots})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := newL07Fixture(t, nil)
			base.bind("sess", 1)
			bctx, _ := newL07Ctx(t)
			want := base.selectWith(t, bctx, "sess", nil).Account.ID

			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			tc.setup(f)
			f.bind("sess", 1)
			ctx, st := newL07Ctx(t)
			got := f.selectWith(t, ctx, "sess", nil)
			require.Equal(t, want, got.Account.ID)
			require.Zero(t, st.KeptSticky())
			if tc.name != "wait count error" {
				require.Equal(t, base.bound("sess"), f.bound("sess"), "same binding as the baseline")
				require.Equal(t, "state_error", l07Fields(ctx, "busy_wait_full").Abandon)
			}
		})
	}
}

// A failed wait-count read reports zero waiters, so the baseline waits.
type l07WaitCountFailing struct{ *probeSlotCache }

func (c *l07WaitCountFailing) GetAccountWaitingCount(context.Context, int64) (int, error) {
	return 3, errors.New("redis down")
}

// ---- §11.4 (8) disabled states equal the baseline ----

type l07Scenario struct {
	name     string
	inFlight int
	waiting  int
	excluded map[int64]struct{}
}

func l07Scenarios() []l07Scenario {
	return []l07Scenario{{"busy", 1, 3, nil}, {"free owner", 0, 0, nil}, {"busy queue not full", 1, 1, nil}, {"retry", 1, 3, map[int64]struct{}{3: {}}}}
}

func (f *l07Fixture) runScenario(t *testing.T, sc l07Scenario, session string) (int64, bool, []string) {
	f.slots.inFlight[1], f.slots.waiting[1] = sc.inFlight, sc.waiting
	f.bind(session, 1)
	f.cache.takeOps()
	ctx, _ := newL07Ctx(t)
	r := f.selectWith(t, ctx, session, sc.excluded)
	return r.Account.ID, r.Acquired, f.cache.takeOps()
}

func l07DisabledStates(t *testing.T) []struct {
	name    string
	install func(*l07Fixture)
	session string
} {
	control := l07SessionInCohort(t, false)
	return []struct {
		name    string
		install func(*l07Fixture)
		session string
	}{
		{"kill switch", func(f *l07Fixture) { f.withPolicies(t, nil, true, true, nil) }, "sess"},
		{"master off", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_feature_master": `{"rev":2,"spec":{"enabled":false}}`}, true, false, nil)
		}, "sess"},
		{"capability false, DB enabled", func(f *l07Fixture) { f.withPolicies(t, nil, false, false, nil) }, "sess"},
		{"policy disabled", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":false,"group_ids":[31],"rollout_percent":100}}}`}, true, false, nil)
		}, "sess"},
		{"rollout zero", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":0}}}`}, true, false, nil)
		}, "sess"},
		{"group outside", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[32],"rollout_percent":100}}}`}, true, false, nil)
		}, "sess"},
		{"parse error", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":"yes"}}}`}, true, false, nil)
		}, "sess"},
		{"control cohort", func(f *l07Fixture) {
			f.withPolicies(t, map[string]string{"cache_routing_policy": `{"rev":3,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":50}}}`}, true, false, nil)
		}, control},
	}
}

func TestL07DisabledStatesEqualBaseline(t *testing.T) {
	for _, state := range l07DisabledStates(t) {
		for _, sc := range l07Scenarios() {
			t.Run(state.name+"/"+sc.name, func(t *testing.T) {
				base := newL07Fixture(t, nil)
				wantID, wantAcquired, wantOps := base.runScenario(t, sc, state.session)

				f := newL07Fixture(t, nil)
				state.install(f)
				gotID, gotAcquired, gotOps := f.runScenario(t, sc, state.session)
				require.Equal(t, wantID, gotID)
				require.Equal(t, wantAcquired, gotAcquired)
				require.Equal(t, wantOps, gotOps, "sticky write sequence")
				require.Zero(t, f.cache.l07Calls, "no L-07 state access")
				require.Zero(t, f.svc.cachePolicies.L07Counters().KeepTotal)
			})
		}
	}
}

func TestL07DisabledStatesAllocateLikeBaseline(t *testing.T) {
	measure := func(f *l07Fixture, sc l07Scenario, session string) float64 {
		f.slots.inFlight[1], f.slots.waiting[1] = sc.inFlight, sc.waiting
		gid := l07TestGroup
		return testing.AllocsPerRun(50, func() {
			f.cache.sessionBindings[session] = 1
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			r, err := f.svc.SelectAccountWithLoadAwareness(ctx, &gid, session, l07TestModel, sc.excluded, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if r.ReleaseFunc != nil {
				r.ReleaseFunc()
			}
			f.cache.mu.Lock()
			f.cache.ops = f.cache.ops[:0]
			f.cache.mu.Unlock()
		})
	}
	for _, state := range l07DisabledStates(t) {
		for _, sc := range l07Scenarios() {
			base := newL07Fixture(t, nil)
			want := measure(base, sc, state.session)
			f := newL07Fixture(t, nil)
			state.install(f)
			require.Equal(t, want, measure(f, sc, state.session), "%s/%s allocs/op", state.name, sc.name)
		}
	}
}

func TestL07AccessorANDsCapabilityKillAndMaster(t *testing.T) {
	f := newL07Fixture(t, nil)
	for _, state := range l07DisabledStates(t) {
		if state.name == "control cohort" || state.name == "group outside" {
			continue // decided per request, not by the accessor
		}
		state.install(f)
		require.Nil(t, f.svc.cachePolicies.Snapshot().RoutingL07(), state.name)
	}
	f.withPolicies(t, nil, true, false, nil)
	p := f.svc.cachePolicies.Snapshot().RoutingL07()
	require.NotNil(t, p)
	require.Equal(t, CacheRoutingL07Policy{RolloutPercent: 100, MaxConsecutiveBusy: 2, MaxHoldSeconds: 60, GroupKeepRatioPct: 5, HealthLookbackSeconds: 120,
		GuardErrorDeltaPP: 5, GuardPauseSeconds: 600, active: true, revision: 3, groups: map[int64]struct{}{31: {}}}, *p)
	require.True(t, p.AllowsGroup(31))
	require.False(t, p.AllowsGroup(32))
	// Gemini, share and non-Anthropic groups are never armed.
	ctx, st := newL07Ctx(t)
	require.Nil(t, f.svc.l07Begin(ctx, &Group{ID: 31, Platform: PlatformAnthropic}, "sess", PlatformGemini, 0))
	require.Nil(t, f.svc.l07Begin(ctx, &Group{ID: 31, Platform: PlatformComposite}, "sess", PlatformAnthropic, 0))
	require.Nil(t, f.svc.l07Begin(ctx, nil, "sess", PlatformAnthropic, 0))
	require.Nil(t, f.svc.l07Begin(ctx, &Group{ID: 31, Platform: PlatformAnthropic}, "", PlatformAnthropic, 0))
	require.Nil(t, st.L07())
}

// Stored limits reach the request path as effective values, clipped by a
// lowered static cap (contract m4l07 §1).
func TestL07StoredLimitsReachTheRequestPath(t *testing.T) {
	store := newPolicyTestStore()
	store.rows["cache_feature_master"] = `{"rev":1,"spec":{"enabled":true}}`
	store.rows["cache_routing_policy"] = `{"rev":7,"spec":{"keep_sticky_on_busy":{"enabled":true,"group_ids":[31],"rollout_percent":20,` +
		`"max_consecutive_busy":4,"max_hold_seconds":200,"owner_busy_skip_per_min_cap":8,"group_keep_ratio_pct":15,"health_lookback_seconds":300}}}`
	for _, tc := range []struct {
		caps                   [3]int
		consecutive, hold, pct int
	}{
		{[3]int{0, 0, 0}, 4, 200, 15},
		{[3]int{3, 120, 10}, 3, 120, 10},
	} {
		cfg := policyTestConfig()
		cfg.Gateway.CacheRouting.L07MaxConsecutiveCap = tc.caps[0]
		cfg.Gateway.CacheRouting.L07MaxHoldSecondsCap = tc.caps[1]
		cfg.Gateway.CacheRouting.L07GroupKeepRatioCapPct = tc.caps[2]
		s := NewCachePolicyService(store, cfg, WithCacheM4Policies(nil), WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L07: true}))
		require.NoError(t, s.Refresh(context.Background()))
		p := s.Snapshot().RoutingL07()
		require.NotNil(t, p)
		require.Equal(t, []int{20, tc.consecutive, tc.hold, 8, tc.pct, 300, 7},
			[]int{p.RolloutPercent, p.MaxConsecutiveBusy, p.MaxHoldSeconds, p.OwnerBusySkipPerMinCap, p.GroupKeepRatioPct, p.HealthLookbackSeconds, int(p.Revision())})
	}
}

func TestL07StaticCapsClipEffectiveValues(t *testing.T) {
	entry := cachePolicyEntry{envelope: CachePolicyEnvelope{Rev: 4, Spec: map[string]any{"keep_sticky_on_busy": map[string]any{
		"enabled": true, "group_ids": []int64{}, "rollout_percent": 10,
		"max_consecutive_busy": 5, "max_hold_seconds": 300, "owner_busy_skip_per_min_cap": 8, "group_keep_ratio_pct": 20, "health_lookback_seconds": 30,
	}}}}
	caps := cacheL07Caps{MaxConsecutive: 3, MaxHoldSeconds: 120, GroupKeepRatioPct: 10, GuardErrorDeltaPP: 7, GuardPauseSeconds: 60}
	p := routingL07FromEntry(entry, true, true, caps)
	require.Equal(t, 3, p.MaxConsecutiveBusy)
	require.Equal(t, 120, p.MaxHoldSeconds)
	require.Equal(t, 10, p.GroupKeepRatioPct)
	require.Equal(t, 8, p.OwnerBusySkipPerMinCap)
	require.Equal(t, 30, p.HealthLookbackSeconds)
	require.True(t, p.AllowsGroup(999), "empty group_ids means every group")
	require.True(t, p.active)
	require.False(t, routingL07FromEntry(entry, false, true, caps).active, "master")
	require.False(t, routingL07FromEntry(entry, true, false, caps).active, "capability")
	entry.parseError = true
	require.False(t, routingL07FromEntry(entry, true, true, caps).active, "parse error")
}

// ---- guard (A5) and counters ----

func TestL07GuardTripsOnlyTowardBaseline(t *testing.T) {
	r := newL07Runtime()
	now := time.Now()
	for i := 0; i < l07GuardMinSamples; i++ {
		r.noteOutcome(1, false, i == 0, 5, 600, now) // 5% failures without keeps
		require.False(t, r.guardTripped(now))
	}
	for i := 0; i < l07GuardMinSamples-1; i++ {
		r.noteOutcome(1, true, i < 4, 5, 600, now)
	}
	require.False(t, r.guardTripped(now), "below the minimum sample")
	r.noteOutcome(1, true, false, 5, 600, now) // 4/20 = 20% vs 5%: +15pp
	require.True(t, r.guardTripped(now))
	require.True(t, r.guardTripped(now.Add(599*time.Second)))
	require.False(t, r.guardTripped(now.Add(601*time.Second)), "the pause ends by itself")

	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.svc.cachePolicies.l07.guardUntil.Store(f.now.Add(time.Minute).UnixNano())
	require.True(t, f.svc.cachePolicies.L07GuardTripped())
	f.bind("sess", 1)
	ctx, _ := newL07Ctx(t)
	f.selectWith(t, ctx, "sess", nil)
	require.Equal(t, int64(2), f.bound("sess"))
	require.Equal(t, "guard", l07Fields(ctx, "busy_wait_full").Abandon)
	require.True(t, f.svc.cachePolicies.L07Counters().GuardTripped)
}

func TestL07OutcomeAndUsageCounters(t *testing.T) {
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, nil)
	f.bind("sess", 1)
	ctx, _ := newL07Ctx(t)
	f.selectWith(t, ctx, "sess", nil)
	f.svc.l07NoteUsage(ctx, &f.accounts[1], 0, 1000) // cold spill
	c := f.svc.cachePolicies.L07Counters()
	require.Equal(t, int64(1), c.SpillColdTotal)
	require.Zero(t, c.ReturnHitTotal)

	f.slots.inFlight[1], f.slots.waiting[1] = 0, 0
	ctx2, _ := newL07Ctx(t)
	require.Equal(t, int64(1), f.selectWith(t, ctx2, "sess", nil).Account.ID)
	f.svc.l07NoteUsage(ctx2, &f.accounts[0], 950, 1000) // warm return
	require.Equal(t, int64(1), f.svc.cachePolicies.L07Counters().ReturnHitTotal)

	// Unarmed requests never count.
	ctx3, _ := newL07Ctx(t)
	f.svc.l07NoteUsage(ctx3, &f.accounts[1], 0, 1000)
	noteL07AttemptFailure(ctx3, 2, resilience.CategoryTransient)
	require.Equal(t, int64(1), f.svc.cachePolicies.L07Counters().SpillColdTotal)
	// Abandon labels come only from the trace enum.
	for reason := range f.svc.cachePolicies.L07Counters().AbandonTotal {
		require.Contains(t, l07AbandonReasons[:], reason)
	}
}

func TestL07RuntimeMapsStayBounded(t *testing.T) {
	r := newL07Runtime()
	now := time.Now()
	p := &CacheRoutingL07Policy{OwnerBusySkipPerMinCap: 1000, GroupKeepRatioPct: 20}
	for i := 0; i < 100000; i++ {
		r.noteRequest(1, now)
	}
	for i := 0; i < l07MaxOwners+10; i++ {
		r.reserve(int64(i), 1, 1, p, now)
	}
	require.LessOrEqual(t, len(r.owners), l07MaxOwners)
	require.Equal(t, "owner_cap", r.reserve(int64(l07MaxOwners+100), 1, 1, p, now), "a full map refuses")
	later := now.Add(2 * time.Minute)
	r.noteRequest(2, later)
	require.Equal(t, "", r.reserve(int64(l07MaxOwners+100), 1, 2, p, later), "idle owners are evicted")
	for i := 0; i < l07MaxRecent+10; i++ {
		r.noteKeep(1, fmt.Sprint("s", i), now.Add(time.Minute), now)
	}
	require.LessOrEqual(t, len(r.recent), l07MaxRecent)
	for i := 0; i < l07MaxGroups+10; i++ {
		r.noteRequest(int64(i), now)
	}
	require.LessOrEqual(t, len(r.groups), l07MaxGroups)
}

// B-T25-1: the A3/A4 check and the count are one critical section, so a
// burst of concurrent busy requests never overshoots either cap.
func TestL07ReserveIsAtomicUnderConcurrency(t *testing.T) {
	burst := func(n int, reserve func(i int) string) int {
		var mu sync.Mutex
		var wg sync.WaitGroup
		ok := 0
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if reserve(i) == "" {
					mu.Lock()
					ok++
					mu.Unlock()
				}
			}(i)
		}
		close(start)
		wg.Wait()
		return ok
	}
	now := time.Now()
	t.Run("owner cap", func(t *testing.T) {
		r := newL07Runtime()
		for i := 0; i < 1000; i++ {
			r.noteRequest(1, now)
		}
		p := &CacheRoutingL07Policy{GroupKeepRatioPct: 20} // auto cap max(4, 2×1) = 4
		require.Equal(t, 4, burst(100, func(int) string { return r.reserve(7, 1, 1, p, now) }))
		require.Equal(t, int64(4), r.owners[7].sum(now, l07OwnerBucket, l07OwnerBuckets))
	})
	t.Run("group budget", func(t *testing.T) {
		r := newL07Runtime()
		for i := 0; i < 1000; i++ {
			r.noteRequest(1, now)
		}
		p := &CacheRoutingL07Policy{GroupKeepRatioPct: 5} // 5% of 1000 requests
		require.Equal(t, 50, burst(200, func(i int) string { return r.reserve(int64(100+i), 1, 1, p, now) }))
		require.Equal(t, int64(50), r.groups[1].keeps.sum(now, l07GroupBucket, l07GroupBuckets))
	})
	t.Run("busy decisions of 100 sessions on one owner", func(t *testing.T) {
		f := newL07Fixture(t, nil)
		f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.GroupKeepRatioPct = 20 })
		f.padRequests(1000)
		owner := f.accounts[0]
		group := &Group{ID: l07TestGroup, Platform: PlatformAnthropic}
		kept := burst(100, func(i int) string {
			ctx, _ := newL07Ctx(t)
			session := fmt.Sprintf("burst-%d", i)
			rec := f.svc.l07Begin(ctx, group, session, PlatformAnthropic, 0)
			if f.svc.l07OwnerBusy(ctx, rec, &owner, session, l07TestModel, &AcquireResult{}, nil, nil) {
				return ""
			}
			return "abandoned"
		})
		require.Equal(t, 4, kept, "auto owner cap holds under a concurrent burst")
		require.Equal(t, int64(4), f.svc.cachePolicies.L07Counters().KeepTotal)
		require.Equal(t, int64(96), f.svc.cachePolicies.L07Counters().AbandonTotal["owner_cap"])
	})
}

// B-T25-1: a reservation whose keep fails afterwards is returned, so failures
// never consume the owner or group budget.
func TestL07ReservationRollsBackOnFailure(t *testing.T) {
	ownerKeeps := func(f *l07Fixture) (int64, int64) {
		r := f.svc.cachePolicies.l07
		r.mu.Lock()
		defer r.mu.Unlock()
		var o, g int64
		if w := r.owners[1]; w != nil {
			o = w.sum(f.now, l07OwnerBucket, l07OwnerBuckets)
		}
		if gr := r.groups[l07TestGroup]; gr != nil {
			g = gr.keeps.sum(f.now, l07GroupBucket, l07GroupBuckets)
		}
		return o, g
	}
	for _, tc := range []struct {
		name  string
		setup func(*l07Fixture, *resilience.RequestState)
		want  string
	}{
		{"guard", func(f *l07Fixture, _ *resilience.RequestState) {
			f.svc.cachePolicies.l07.guardUntil.Store(f.now.Add(time.Minute).UnixNano())
		}, "guard"},
		{"redis write", func(f *l07Fixture, _ *resilience.RequestState) { f.cache.writeErr = errors.New("redis down") }, "state_error"},
		{"keep", func(_ *l07Fixture, st *resilience.RequestState) { st.KeepSticky(9) }, "state_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newL07Fixture(t, nil)
			f.withPolicies(t, nil, true, false, nil)
			f.padRequests(100)
			ctx, st := newL07Ctx(t)
			tc.setup(f, st)
			rec := f.svc.l07Begin(ctx, &Group{ID: l07TestGroup, Platform: PlatformAnthropic}, "sess", PlatformAnthropic, 0)
			require.False(t, f.svc.l07OwnerBusy(ctx, rec, &f.accounts[0], "sess", l07TestModel, &AcquireResult{}, nil, nil))
			require.Equal(t, tc.want, L07TraceFieldsFromContext(ctx, "busy_wait_full").Abandon)
			o, g := ownerKeeps(f)
			require.Zero(t, o, "owner window unchanged")
			require.Zero(t, g, "group keeps unchanged")
			require.Zero(t, f.svc.cachePolicies.L07Counters().KeepTotal)
		})
	}
	// After a rollback the budget is intact: the next busy request is kept.
	f := newL07Fixture(t, nil)
	f.withPolicies(t, nil, true, false, func(p *CacheRoutingL07Policy) { p.OwnerBusySkipPerMinCap = 1 })
	f.padRequests(100)
	f.cache.writeErr = errors.New("redis down")
	ctx, _ := newL07Ctx(t)
	rec := f.svc.l07Begin(ctx, &Group{ID: l07TestGroup, Platform: PlatformAnthropic}, "a", PlatformAnthropic, 0)
	require.False(t, f.svc.l07OwnerBusy(ctx, rec, &f.accounts[0], "a", l07TestModel, &AcquireResult{}, nil, nil))
	f.cache.writeErr = nil
	ctx2, _ := newL07Ctx(t)
	rec2 := f.svc.l07Begin(ctx2, &Group{ID: l07TestGroup, Platform: PlatformAnthropic}, "b", PlatformAnthropic, 0)
	require.True(t, f.svc.l07OwnerBusy(ctx2, rec2, &f.accounts[0], "b", l07TestModel, &AcquireResult{}, nil, nil), "cap 1 is still free")
	o, _ := ownerKeeps(f)
	require.Equal(t, int64(1), o)
}
