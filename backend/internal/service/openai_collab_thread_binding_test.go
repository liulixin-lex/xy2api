//go:build unit

package service

// Binding scenarios here exercise unfinished code through the test-only hook.
// They do not establish equivalence or enable thread bindings in this release.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

type collabOracleCase struct {
	accounts                      []Account
	bound                         int64
	guardian, avoid, alias, plain bool
	mode                          int
	slots                         map[int64]bool
	production                    bool
	migration                     int
	shipped                       bool
	switches                      int
}

type collabOracleRepo struct {
	*selectionRuntimeRepo
	fail bool
}

func (r collabOracleRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, g int64, p string) ([]Account, error) {
	if r.fail {
		return nil, errors.New("synthetic list failure")
	}
	return r.selectionRuntimeRepo.ListSchedulableByGroupIDAndPlatform(ctx, g, p)
}

type collabOracleLoads struct {
	*selectionRuntimeLoads
	fail bool
}

type collabOracleAtomicCache struct{ *selectionRuntimeCache }

func (c collabOracleAtomicCache) ClaimSessionAccountID(ctx context.Context, group int64, key string, account int64, ttl time.Duration, _ bool) (int64, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "claim", account, ttl)
		defer finish()
	}

	if c.sessionBindings[key] == 0 {
		c.sessionBindings[key] = account
	}
	return c.sessionBindings[key], nil
}
func (c collabOracleAtomicCache) DeleteSessionAccountIDIfOwner(ctx context.Context, group int64, key string, account int64) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, group, c.sessionBindings, nil, "delete", account, 0)
		defer finish()
	}

	if c.sessionBindings[key] == account {
		delete(c.sessionBindings, key)
	}
	return nil
}

func (c collabOracleLoads) AcquireAccountSlot(ctx context.Context, id int64, limit int, key string) (bool, error) {
	if c.fail {
		return false, errors.New("synthetic slot failure")
	}
	return c.selectionRuntimeLoads.AcquireAccountSlot(ctx, id, limit, key)
}

func collabOracleOutcome(t *testing.T, c collabOracleCase, on bool) string {
	f := newCollabThreadFixture(t, c.accounts...)
	f.loads.acquireResults = c.slots
	f.svc.cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = c.migration&1 != 0
	f.svc.cfg.Gateway.OpenAIWS.SessionHashDualWriteOld = c.migration&2 != 0
	session := "shared-session"
	if c.alias {
		session = "v2:shared-session"
	}
	ctx := withOpenAINoStickyMigration(selectionReviewContext())
	ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child-execution", preserveBinding: true, selectionPreservesBinding: &atomic.Bool{}})
	legacy := "legacy"
	if c.production {
		ctx, session = collabAtomicRequest(t, f, resilience.NewCollabTreeAffinity(32, time.Hour), "child", false, c.guardian)
		legacy = openAILegacySessionHashFromContext(ctx)
	}
	requested := on
	on = on && !c.shipped && c.migration == 0 && (!c.alias || legacy == "")
	owner := c.bound
	if on {
		owner = 1
	}
	if owner > 0 {
		f.cache.sessionBindings["openai:"+session] = owner
	}
	if c.alias && legacy != "" {
		f.cache.sessionBindings["openai:"+legacy] = owner
		ctx = withOpenAILegacySessionHash(ctx, legacy)
	}
	if c.plain {
		f.svc.cache = &f.cache.schedulerTestGatewayCache
	} else if c.alias || c.production {
		f.svc.cache = &collabOracleAtomicCache{f.cache}
	}
	if c.guardian && !c.production {
		ctx = context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: session})
	}
	if c.avoid {
		scope := session
		if on {
			scope = openAIRejectionScope(ctx).session
		}
		require.NoError(t, f.svc.resilience.store.AddSessionAvoid(ctx, 7, scope, 1, time.Minute))
	}
	leases := newSelectionTrialLeaseFake()
	NewOpenAISelectionBoard(f.svc, func() schedquality.View { return f.view }, leases)
	switch c.mode {
	case 1:
		f.view.States[fakeSchedViewKey{7, 2, "m"}] = schedquality.StateView{Error: schedquality.LineView{Identity: schedquality.IdentityRear}}
	case 2, 3:
		selectionSetTrial(f, 2, leases)
		leases.full[2] = c.mode == 3
	case 4:
		until := time.Now().Add(time.Minute)
		f.repo.accounts[1].TempUnschedulableUntil = &until
	case 5:
		f.loads.loadBatchErr = errors.New("synthetic load failure")
	case 6:
		f.svc.accountRepo = collabOracleRepo{f.repo, true}
	case 7:
		f.svc.concurrencyService = NewConcurrencyService(collabOracleLoads{f.loads, true})
	case 8:
		f.repo.accounts[1].RateMultiplier = floatPtr(1)
		f.repo.accounts[2].RateMultiplier = floatPtr(.2)
		ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, threshold: .5, ceilingOn: true, pricingAt: time.Now()})
	case 9:
		require.NoError(t, f.svc.resilience.store.AddSessionAvoid(ctx, 7, firstOutputAvoidSession(session), 2, time.Minute))
	case 10:
		// A fresh owner slot can coexist with a stale full batch snapshot.
		f.loads.loadMap = map[int64]*AccountLoadInfo{1: {LoadRate: 100}, 2: {LoadRate: 100}, 3: {LoadRate: 100}}
	case 11:
		if c.alias && legacy != "" {
			delete(f.cache.sessionBindings, "openai:"+session)
		}
	}
	before := maps.Clone(f.cache.sessionBindings)
	if requested {
		settings := collabAffinitySettings()
		if c.shipped {
			settings = collabReleaseSettings(c.switches)
		}
		ctx = WithOpenAICollabTreeAffinity(ctx, resilience.NewCollabTreeAffinity(32, time.Hour), settings, 7, collabAffinityIdentity("child", "root"), resilience.CollabRoleChild, true)
		if c.shipped {
			require.False(t, f.svc.PrepareOpenAICollabThreadBinding(ctx, 1, session, PlatformOpenAI, OpenAIClientTransportHTTP, true))
		} else {
			ctx = prepareAffinityTest(f, ctx, session)
		}
		b := openAICollabBindingFrom(ctx)
		require.Equal(t, on, b != nil)
		if on && (!c.production || c.guardian || c.bound != 1) {
			require.NoError(t, b.write(ctx, c.bound, time.Hour))
		}
	}
	start := time.Now()

	result, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, ptrInt64Selection(7), "", session, "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
	out := fmt.Sprint(err)
	if result != nil && result.Account != nil {
		out = fmt.Sprintf("account=%d acquired=%v wait=%+v", result.Account.ID, result.Acquired, result.WaitPlan)
		if result.Acquired {
			CompleteOpenAIStickyChange(ctx, result.Account.ID, true, false)
			require.NoError(t, f.svc.BindStickySessionAfterProfitAdmission(ctx, ptrInt64Selection(7), session, result.Account.ID))
		}
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	if on {
		for key, value := range before {
			require.Equal(t, value, f.cache.sessionBindings[key], "tree or alias changed: %s", key)
		}
		for key := range f.cache.sessionBindings {
			if strings.HasPrefix(key, "openai:") {
				require.Contains(t, before, key, "child must not adopt a tree alias")
			}
		}
	}
	member := TrialLeaseSessionMember(session)
	if on && openAICollabBindingFrom(ctx).present() {
		member = TrialLeaseSessionMember(openAICollabBindingFrom(ctx).key)
	}
	requestMember := TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID())
	calls := make([]string, len(leases.calls))
	for i, call := range leases.calls {
		call = strings.ReplaceAll(call, requestMember, "request")
		calls[i] = strings.ReplaceAll(call, member, "binding")
	}
	held := make([]string, 0, len(leases.held))
	for key, present := range leases.held {
		if present {
			key = strings.ReplaceAll(key, requestMember, "request")
			held = append(held, strings.ReplaceAll(key, member, "binding"))
		}
	}
	sort.Strings(held)
	pending := "none"
	if change := pendingOpenAIStickyChange(ctx); change != nil {
		pending = fmt.Sprintf("%s/%s/%d/%t/%t", change.kind, change.reason, change.target, change.ownerReleased, change.done)
	}
	return fmt.Sprintf("%s elapsed=%v attempts=%v leases=%v held=%v pending=%s bypass=%t", out, time.Since(start), f.attempts, calls, held, pending, resilience.StateFrom(ctx).StickyBypassChangesBinding())
}

func TestCollabThreadBindingGeneratedOracle(t *testing.T) {
	withResilienceSettings(t, nil)
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	r := rand.New(rand.NewSource(17))
	for i := 0; i < 3000; i++ {
		c := collabOracleCase{bound: int64(r.Intn(4)), guardian: r.Intn(2) == 0, avoid: r.Intn(2) == 0, alias: r.Intn(2) == 0, plain: r.Intn(2) == 0, mode: i % 12, slots: map[int64]bool{}}
		for id := int64(1); id <= 3; id++ {
			a := selectionFixtureAccount(id, r.Intn(3))
			a.Schedulable = r.Intn(8) != 0
			c.accounts = append(c.accounts, a)
			c.slots[id] = r.Intn(3) != 0
		}
		synctest.Test(t, func(t *testing.T) {
			resilience.ProcessNegativeCache().InvalidateGroup(7)
			want := collabOracleOutcome(t, c, false)
			resilience.ProcessNegativeCache().InvalidateGroup(7)
			got := collabOracleOutcome(t, c, true)
			require.Equal(t, want, got, "case=%d bound=%d mode=%d guardian=%v plain=%v alias=%v", i, c.bound, c.mode, c.guardian, c.plain, c.alias)
		})
	}
}

func TestCollabThreadBindingDirectWriters(t *testing.T) {
	t.Run("legacy_inactive", testCollabLegacyDirectWriters)
	t.Run("no_alias", testCollabDirectWriters)
}

func testCollabDirectWriters(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
	f.svc.cache = collabOracleAtomicCache{f.cache}
	f.cache.sessionBindings["openai:v2:shared-session"] = 1
	f.cache.sessionBindings["openai:legacy"] = 1
	ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "v2:shared-session")
	b := openAICollabBindingFrom(ctx)
	require.NoError(t, b.write(ctx, 2, time.Hour))
	require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "v2:shared-session", 1))
	owner, _, _ := b.effective(ctx)
	require.EqualValues(t, 2, owner)
	require.NoError(t, f.svc.deleteStickySessionAccountIDIfOwner(ctx, ptrInt64Selection(7), "v2:shared-session", 2))
	owner, missing, _ := b.effective(ctx)
	require.Zero(t, owner)
	require.True(t, missing)
	require.NoError(t, f.svc.setStickySessionAccountID(ctx, ptrInt64Selection(7), "v2:shared-session", 2, time.Hour))
	require.NoError(t, f.svc.refreshStickySessionTTL(ctx, ptrInt64Selection(7), "v2:shared-session", time.Hour, 2))
	cache := collabThreadVersionedCache{b}
	expected, err := cache.GetSessionBinding(ctx, 7, "openai:v2:shared-session")
	require.NoError(t, err)
	_, err = cache.RecordSessionBindingFailure(context.Background(), 7, "openai:v2:shared-session", "ignored", expected, 1, time.Minute)
	require.NoError(t, err)
	commit, err := cache.CommitSessionBinding(context.Background(), 7, "openai:v2:shared-session", expected, 2, time.Hour, BindingCommitOptions{})
	require.NoError(t, err)
	require.Equal(t, BindingCommitted, commit.Status)
	_, aliases := any(cache).(OpenAIStickyAliasCache)
	require.False(t, aliases, "active thread cache cannot expose legacy operations")
	rec := f.svc.collabSessionRecorder(ctx)
	for i := 0; i < resilience.Current().Session.StreakThreshold; i++ {
		rec.Failure(context.Background(), 7, "v2:shared-session", 2, resilience.CategoryTransient)
	}
	owner, missing, _ = b.effective(ctx)
	require.Zero(t, owner)
	require.True(t, missing)
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:v2:shared-session"])
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:legacy"])
}

type collabSlowBindingCache struct {
	GatewayCache
	reads atomic.Int64
	fail  bool
}

func (c *collabSlowBindingCache) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	if strings.HasPrefix(key, "collab-thread-binding-v1:") {
		c.reads.Add(1)
		if !c.fail {
			<-ctx.Done()
		}
		return 0, errors.New("synthetic read failure")
	}
	return c.GatewayCache.GetSessionAccountID(ctx, g, key)
}

func TestCollabThreadBindingReadBudgetAndScope(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
		cache := &collabSlowBindingCache{GatewayCache: f.svc.cache}
		f.svc.cache = cache
		ctx := collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root")
		short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		start := time.Now()
		short = prepareAffinityTest(f, short, "shared-session")
		require.Equal(t, start, time.Now())
		require.Zero(t, cache.reads.Load())
		require.NotNil(t, openAICollabBindingFrom(short))
		ctx = collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root")
		start = time.Now()
		ctx = prepareAffinityTest(f, ctx, "shared-session")
		require.Zero(t, time.Since(start))
		require.Zero(t, cache.reads.Load())
		f.svc.enableUnshippedCollabThreadBinding(ctx, 1, "shared-session", PlatformOpenAI, OpenAIClientTransportHTTP, true)
		require.Zero(t, cache.reads.Load())
		key := openAICollabBindingFrom(ctx).key
		require.True(t, strings.HasPrefix(key, "collab-thread-binding-v1:"))
		require.NotContains(t, key, "child")
		require.NotContains(t, key, "openai:")
	})
}

type collabAvoidOutage struct {
	ResilienceStore
	fail bool
}

func (s *collabAvoidOutage) SessionAvoidWrittenAt(ctx context.Context, g int64, key string) (map[int64]time.Time, error) {
	if s.fail {
		return nil, errors.New("synthetic avoidance outage")
	}
	return s.ResilienceStore.SessionAvoidWrittenAt(ctx, g, key)
}

func TestCollabThreadBindingLocalAvoidanceAndNamespaces(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
	f.cache.sessionBindings["openai:shared-session"] = 1
	local := resilience.NewCollabTreeAffinity(32, time.Hour)
	ctx := prepareAffinityTest(f, collabAffinityContext(local, collabAffinitySettings(), "child", "root"), "shared-session")
	first := openAICollabBindingFrom(ctx)
	f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
	outage := &collabAvoidOutage{ResilienceStore: f.svc.resilience.store, fail: true}
	f.svc.resilience.store = outage
	retry := prepareAffinityTest(f, collabAffinityContext(local, collabAffinitySettings(), "child", "root"), "shared-session")
	selected, _, err := collabAffinitySelect(f, retry, "m", nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, selected.Account.ID)
	selected.ReleaseFunc()
	require.Contains(t, f.svc.collabThreadAvoidances(retry), int64(1))
	f.svc.resilience.sessionResetAt = func(int64) time.Time { return time.Now().Add(time.Second) }
	require.Empty(t, f.svc.collabThreadAvoidances(retry))
	other := withOpenAINoStickyMigration(collabAffinityContext(local, collabAffinitySettings(), "child", "root"))
	other = context.WithValue(other, openAITerminalRejectionScopeKey{}, openAIRejectionScope(ctx))
	require.True(t, f.svc.enableUnshippedCollabThreadBinding(other, 2, "shared-session", PlatformOpenAI, OpenAIClientTransportHTTP, true))
	require.NotEqual(t, first.key, openAICollabBindingFrom(other).key)
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
}

func TestCollabThreadBindingTerminalAndPendingCompletion(t *testing.T) {
	withResilienceSettings(t, nil)
	f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2))
	f.cache.sessionBindings["openai:shared-session"] = 1
	ctx := prepareAffinityTest(f, collabAffinityContext(resilience.NewCollabTreeAffinity(32, time.Hour), collabAffinitySettings(), "child", "root"), "shared-session")
	b := openAICollabBindingFrom(ctx)
	require.NoError(t, b.write(ctx, 0, time.Hour))
	selected, _, err := collabAffinitySelect(f, ctx, "m", map[int64]struct{}{1: {}})
	require.NoError(t, err)
	require.EqualValues(t, 2, selected.Account.ID)
	change := pendingOpenAIStickyChange(ctx)
	require.NotNil(t, change)
	_, isolated := change.cache.(collabThreadVersionedCache)
	require.True(t, isolated)
	CompleteOpenAIStickyChange(ctx, 2, true, false)
	owner, _, err := b.effective(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, owner)
	selected.ReleaseFunc()
	account := selectionFixtureAccount(2, 2)
	account.Credentials = map[string]any{"base_url": "https://synthetic.example/v1"}
	for i := 0; i < resilience.Current().Session.StreakThreshold; i++ {
		f.svc.noteOpenAITerminalRejectionForSession(ctx, &account, 7, "shared-session", 400, nil, []byte(`{"error":{"message":"synthetic upstream refusal"}}`), errors.New("synthetic rejection"))
	}
	owner, missing, _ := b.effective(ctx)
	require.EqualValues(t, 2, owner)
	require.False(t, missing)
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
}

func TestCollabThreadBindingExcludedDimensions(t *testing.T) {
	for _, mode := range []string{"off", "plain", "platform", "websocket", "passthrough", "optional", "quality", "trials"} {
		t.Run(mode, func(t *testing.T) {
			f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1))
			f.svc.cfg.JWT.Secret = "synthetic-affinity-secret"
			settings := collabAffinitySettings()
			settings.Collab.TreeAffinity = mode != "off"
			ctx := collabAffinityContext(resilience.NewCollabTreeAffinity(16, time.Hour), settings, "child", "root")
			ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, openAITerminalRejectionScope{session: "child-execution"})
			if mode != "plain" {
				ctx = withOpenAINoStickyMigration(ctx)
			}
			platform, transport, native := PlatformOpenAI, OpenAIClientTransportHTTP, true
			switch mode {
			case "platform":
				platform = PlatformGrok
			case "websocket":
				transport = OpenAIClientTransportWS
			case "passthrough":
				native = false
			case "optional":
				ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true})
			case "quality":
				ctx = withOpenAIQualityRouting(ctx)
			case "trials":
				f.svc.selectionBoard.leases = newSelectionTrialLeaseFake()
			}
			active := f.svc.enableUnshippedCollabThreadBinding(ctx, 1, "shared-session", platform, transport, native)
			require.Equal(t, mode == "trials", active)
			require.Equal(t, active, openAICollabBindingFrom(ctx) != nil)
		})
	}
}

type collabBindingExpiryCache struct {
	GatewayCache
	values    map[string]int64
	until     map[string]time.Time
	failWrite bool
}

func (c *collabBindingExpiryCache) GetSessionAccountID(ctx context.Context, g int64, key string) (int64, error) {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, g, c.values, c.until, "read", 0, 0)
		defer finish()
	}

	if !strings.HasPrefix(key, "collab-thread-binding-v1:") {
		return c.GatewayCache.GetSessionAccountID(ctx, g, key)
	}
	if time.Now().Before(c.until[key]) {
		return c.values[key], nil
	}
	return 0, ErrStickySessionNotFound
}
func (c *collabBindingExpiryCache) SetSessionAccountID(ctx context.Context, g int64, key string, account int64, ttl time.Duration) error {
	if binding, ok := StickyThreadBindingFrom(ctx); ok {
		var finish func()
		ctx, finish = collabFakeThreadOperation(ctx, binding, c, g, c.values, c.until, "set", account, ttl)
		defer finish()
	}

	if !strings.HasPrefix(key, "collab-thread-binding-v1:") {
		return c.GatewayCache.SetSessionAccountID(ctx, g, key, account, ttl)
	}
	if c.failWrite {
		return errors.New("synthetic persistence failure")
	}
	c.values[key], c.until[key] = account, time.Now().Add(ttl)
	return nil
}

func TestCollabThreadBindingRequestSequence(t *testing.T) {
	withResilienceSettings(t, nil)
	synctest.Test(t, func(t *testing.T) {
		f := newCollabThreadFixture(t, selectionFixtureAccount(1, 1), selectionFixtureAccount(2, 2), selectionFixtureAccount(3, 3))
		f.cache.sessionBindings["openai:shared-session"] = 1
		cache := &collabBindingExpiryCache{GatewayCache: f.svc.cache, values: map[string]int64{}, until: map[string]time.Time{}}
		f.svc.cache = cache
		local := resilience.NewCollabTreeAffinity(32, time.Hour)
		request := func(store *resilience.CollabTreeAffinity) context.Context {
			return prepareAffinityTest(f, collabAffinityContext(store, collabAffinitySettings(), "child", "root"), "shared-session")
		}
		ctx := request(local)
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
		bind := func(ctx context.Context, want int64) {
			r, _, err := collabAffinitySelect(f, ctx, "m", nil)
			require.NoError(t, err)
			require.EqualValues(t, want, r.Account.ID)
			bindErr := f.svc.BindStickySessionAfterProfitAdmission(ctx, ptrInt64Selection(7), "shared-session", r.Account.ID)
			if cache.failWrite {
				require.Error(t, bindErr)
			} else {
				require.NoError(t, bindErr)
			}
			r.ReleaseFunc()
			require.EqualValues(t, 1, f.cache.sessionBindings["openai:shared-session"])
		}
		bind(ctx, 2)
		bind(request(local), 2)
		// A fresh instance sees the same binding and existing thread avoidance.
		bind(request(resilience.NewCollabTreeAffinity(32, time.Hour)), 2)
		time.Sleep(time.Duration(resilience.Current().Session.AvoidSeconds+1) * time.Second)
		ctx = request(local)
		bind(ctx, 2)
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 2, true)
		bind(request(local), 1)
		// Failed persistence retains today's error and the shared binding.
		cache.failWrite = true
		ctx = request(local)
		f.svc.AvoidOpenAICollabStateAccount(ctx, 7, "shared-session", 1, true)
		bind(request(local), 3)
		cache.failWrite = false
		time.Sleep(2 * time.Hour)
		ctx = request(resilience.NewCollabTreeAffinity(32, time.Hour))
		require.False(t, openAICollabBindingFrom(ctx).present())
		bind(ctx, 1)
	})
}
