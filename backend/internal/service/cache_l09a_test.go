//go:build unit

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/pagination"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Byte-identical copy of 00-shared/cache_trace_contract/l09a (v1.0.1),
// except the README. SHA256SUMS digest as published by F (kickoff v1.1).
const cacheTraceL09ASumsSHA = "86b1020fe994be697f4a24c527e945da51b72c41fd6dc8c11953fdb5a2274960"

func TestCacheTraceL09AContractEmitterGolden(t *testing.T) {
	root := "testdata/cache_trace_contract/l09a"
	sums, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	require.NoError(t, err)
	require.Equal(t, cacheTraceL09ASumsSHA, fmt.Sprintf("%x", sha256.Sum256(sums)))
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		parts := strings.Fields(line)
		require.Len(t, parts, 2)
		raw, err := os.ReadFile(filepath.Join(root, parts[1]))
		if parts[1] == "README.md" {
			require.True(t, os.IsNotExist(err))
			continue
		}
		require.NoError(t, err)
		require.Equal(t, parts[0], fmt.Sprintf("%x", sha256.Sum256(raw)), parts[1])
	}
	paths, err := filepath.Glob(filepath.Join(root, "examples", "*.json"))
	require.NoError(t, err)
	require.Len(t, paths, 9)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var fixture struct {
				Payload map[string]any `json:"payload"`
			}
			require.NoError(t, json.Unmarshal(raw, &fixture))
			got := cacheTraceContractEmit(t, fixture.Payload)
			require.Equal(t, fixture.Payload, got)
			event, err := json.Marshal(got)
			require.NoError(t, err)
			t.Logf("L09A_TRACE_CONTRACT_EVENT %s", event)
		})
	}
}

// A trace without a first-selection outcome is filled from its own snapshot.
func TestCacheTraceL09ADefaults(t *testing.T) {
	f := newL09AFixture(t, l09aFixtureOptions{})
	for _, tc := range []struct {
		policy *CachePolicySnapshot
		route  string
		skip   string
	}{
		{nil, "sticky_new", "policy_off"},
		{f.policies.Snapshot(), "sticky_new", "not_first_selection"},
		{f.policies.Snapshot(), "fallback_group", "excluded_path"},
	} {
		d := cacheTraceData{RouteReason: tc.route}
		l09aTraceDefaults(&d, tc.policy)
		require.Equal(t, "none", d.L09AArm)
		require.False(t, d.L09AApplied)
		require.Equal(t, tc.skip, d.L09ASkip)
	}
	d := cacheTraceData{L09AArm: "on", L09ASkip: "none", L09AApplied: true}
	l09aTraceDefaults(&d, nil)
	require.Equal(t, "on", d.L09AArm, "a recorded outcome is kept")
}

type l09aGroups struct {
	fail  bool
	calls int
}

func (g *l09aGroups) ListWithFilters(_ context.Context, _ pagination.PaginationParams, _, _, _ string, _ *bool) ([]Group, *pagination.PaginationResult, error) {
	g.calls++
	if g.fail {
		return nil, nil, fmt.Errorf("unavailable")
	}
	target := int64(77)
	return []Group{{ID: 10, Platform: PlatformOpenAI, AvailabilityFallbackGroupID: &target}, {ID: 77, Platform: PlatformOpenAI}}, &pagination.PaginationResult{Total: 2}, nil
}

type l09aFixtureOptions struct {
	capability *bool
	mode       string
	rollout    *int
	aware      *bool
	staticPct  *int
	master     *bool
	kill       bool
	groupsFail bool
	advanced   bool
	accounts   []Account
}

type l09aFixture struct {
	s        *OpenAIGatewayService
	policies *CachePolicyService
	cache    *atomicStickyTestCache
	groups   *l09aGroups
	group    int64
	model    string
}

const l09aSession = "v2:l09a0000000001"

func l09aBool(v bool) *bool { return &v }
func l09aInt(v int) *int    { return &v }

// Owner 1 sits on relay-a and is balance paused. Candidate 2 is on relay-b
// with the better old cache score; candidate 3 shares relay-a.
func l09aAccounts(now time.Time) []Account {
	until := now.Add(10 * time.Minute)
	apiKey := func(id int64, host string) Account {
		return Account{ID: id, Name: fmt.Sprint("a", id), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Concurrency: 100, Priority: 1, GroupIDs: []int64{10}, Credentials: map[string]any{"base_url": "https://" + host + "/v1", "api_key": "sk-test"}}
	}
	owner := apiKey(1, "relay-a.example")
	owner.TempUnschedulableUntil = &until
	owner.TempUnschedulableReason = resilience.BalancePauseReason("HTTP 402: insufficient balance")
	return []Account{owner, apiKey(2, "relay-b.example"), apiKey(3, "Relay-A.example")}
}

func newL09AFixture(t *testing.T, o l09aFixtureOptions) *l09aFixture {
	t.Helper()
	now := time.Now()
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.JWT.Secret = "l09a-test-secret"
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.OpenAICache.DiagnosticsEnabled = true
	cfg.Gateway.OpenAICache.AwareRoutingEnabled = o.aware == nil || *o.aware
	cfg.Gateway.OpenAICache.RolloutPercent = 100
	if o.staticPct != nil {
		cfg.Gateway.OpenAICache.RolloutPercent = *o.staticPct
	}
	cfg.Gateway.CacheFeatures.KillSwitch = o.kill
	store := newPolicyTestStore()
	master := o.master == nil || *o.master
	store.rows["cache_feature_master"] = fmt.Sprintf(`{"rev":1,"spec":{"enabled":%t}}`, master)
	mode := o.mode
	if mode == "" {
		mode = "prev_host"
	}
	rollout := 100
	if o.rollout != nil {
		rollout = *o.rollout
	}
	store.rows["cache_routing_policy"] = fmt.Sprintf(`{"rev":1,"spec":{"affinity_mode":%q,"affinity_rollout_percent":%d}}`, mode, rollout)
	groups := &l09aGroups{fail: o.groupsFail}
	capable := o.capability == nil || *o.capability
	policies := NewCachePolicyService(store, cfg, WithCacheM4Policies(groups), WithCacheM4BuildCapabilities(CacheM4BuildCapabilities{L07: true, L09A: capable}))
	require.NoError(t, policies.Refresh(context.Background()))
	accounts := o.accounts
	if accounts == nil {
		accounts = l09aAccounts(now)
	}
	cache := &atomicStickyTestCache{bindings: map[string]int64{"openai:" + l09aSession: 1}}
	s := &OpenAIGatewayService{cache: cache, cfg: cfg, accountRepo: stubOpenAIAccountRepo{accounts: accounts},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}), cachePolicies: policies}
	if o.advanced {
		s.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
	} else {
		s.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("false")
	}
	f := &l09aFixture{s: s, policies: policies, cache: cache, groups: groups, group: 10, model: "gpt-5.4"}
	// Complete, recent evidence for both candidates; relay-b reads more.
	d := f.diagnostic()
	telemetry := s.cacheTelemetry()
	for _, a := range accounts {
		sample := openAICacheSample{count: 20, input: 2048, read: 1000, at: now}
		if a.ID == 2 {
			sample.read = 2000
		}
		for i := range sample.recent {
			sample.recent[i] = now
		}
		account := a
		telemetry.scores[cacheSampleKey(d, &account, f.model)] = sample
	}
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	return f
}

func (f *l09aFixture) diagnostic() *OpenAICacheDiagnostic {
	return &OpenAICacheDiagnostic{Session: l09aSession, Prefix: "prefix", WirePrefix: "prefix", Model: f.model, GroupID: f.group}
}

// request builds a new gateway request: request state, diagnostic, trace and
// the request's policy snapshot, as the handler and middleware attach them.
func (f *l09aFixture) request(t *testing.T) (*gin.Context, context.Context, *cacheTrace) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	trace := &cacheTrace{}
	ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	ctx = context.WithValue(ctx, openAICacheDiagnosticKey{}, f.diagnostic())
	ctx = context.WithValue(ctx, cacheTraceKey{}, trace)
	c.Request = c.Request.WithContext(ctx)
	f.s.CaptureCachePolicy(c)
	return c, ctx, trace
}

func (f *l09aFixture) selectAccount(t *testing.T, ctx context.Context, excluded map[int64]struct{}) *AccountSelectionResult {
	t.Helper()
	group := f.group
	selection, _, err := f.s.SelectAccountWithSchedulerForCapability(ctx, &group, "", l09aSession, f.model, excluded, OpenAIUpstreamTransportAny, "", false, false, true)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
	return selection
}

func (f *l09aFixture) rebind() { f.cache.bindings["openai:"+l09aSession] = 1 }

func l09aTraceFields(trace *cacheTrace) (string, bool, string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return trace.data.L09AArm, trace.data.L09AApplied, trace.data.L09ASkip
}

// Blocking positive case (kickoff v1.1 §7.5): the next new request after a
// balance-paused owner orders the same-host candidate first and records
// l09a_applied=true, in both schedulers.
func TestCacheL09AAppliedAfterBalancePausedOwner(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint("advanced=", advanced), func(t *testing.T) {
			f := newL09AFixture(t, l09aFixtureOptions{advanced: advanced})
			require.NotNil(t, f.policies.Snapshot().RoutingL09A())
			for i := 0; i < 20; i++ {
				f.rebind()
				c, ctx, trace := f.request(t)
				f.s.ArmCacheL09A(c, ctx)
				selection := f.selectAccount(t, ctx, nil)
				require.Equal(t, int64(3), selection.Account.ID, "the candidate on the owner's host leads its tied block")
				arm, applied, skip := l09aTraceFields(trace)
				require.Equal(t, "on", arm)
				require.True(t, applied)
				require.Equal(t, "none", skip)
				_, bound := f.cache.bindings["openai:"+l09aSession]
				require.True(t, bound, "the new selection binds as before")
			}
			require.Equal(t, int64(20), f.policies.L09ACounters().SkipTotal["none"])
		})
	}
}

// With L-09a off or skipped the old cache-aware order is untouched: without
// a sticky owner, the better old score wins in every off configuration
// (E §6 first row), and the hook is the original one.
func TestCacheL09AOffKeepsOldCacheAwareOrder(t *testing.T) {
	cases := map[string]l09aFixtureOptions{
		"capability_false": {capability: l09aBool(false)},
		"mode_score":       {mode: "score"},
		"rollout_zero":     {rollout: l09aInt(0)},
		"master_off":       {master: l09aBool(false)},
		"kill":             {kill: true},
		"groups_unknown":   {groupsFail: true},
		"on_no_hint":       {},
	}
	for name, o := range cases {
		for _, advanced := range []bool{false, true} {
			o := o
			o.advanced = advanced
			t.Run(fmt.Sprint(name, "/advanced=", advanced), func(t *testing.T) {
				f := newL09AFixture(t, o)
				if name != "on_no_hint" {
					require.Nil(t, f.policies.Snapshot().RoutingL09A())
				}
				for i := 0; i < 20; i++ {
					delete(f.cache.bindings, "openai:"+l09aSession)
					c, ctx, trace := f.request(t)
					f.s.ArmCacheL09A(c, ctx)
					if name != "on_no_hint" {
						require.Nil(t, resilience.StateFrom(ctx).CacheL09A(), "an off snapshot stores no permit")
					}
					selection := f.selectAccount(t, ctx, nil)
					require.Equal(t, int64(2), selection.Account.ID, "old cache score still orders the tied block")
					if name == "on_no_hint" {
						arm, applied, skip := l09aTraceFields(trace)
						require.Equal(t, "on", arm)
						require.False(t, applied)
						require.Equal(t, "no_hint", skip)
					}
				}
			})
		}
	}
}

// Static cache-aware off (production) and stale snapshots: zero execution.
func TestCacheL09AStaticOffAndStaleNeverRun(t *testing.T) {
	f := newL09AFixture(t, l09aFixtureOptions{aware: l09aBool(false)})
	require.Nil(t, f.policies.Snapshot().RoutingL09A())
	view, err := f.policies.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "blocked", view.Effective.State)
	require.Equal(t, []string{"static_aware_routing_disabled"}, view.Effective.Blockers)
	f = newL09AFixture(t, l09aFixtureOptions{staticPct: l09aInt(0)})
	require.Nil(t, f.policies.Snapshot().RoutingL09A())
	require.Zero(t, f.groups.calls, "group roles are read only for an effective prev_host")

	f = newL09AFixture(t, l09aFixtureOptions{})
	f.policies.mu.Lock()
	f.policies.stale = true
	f.policies.publishEntries(f.policies.Snapshot().entries)
	f.policies.mu.Unlock()
	require.Nil(t, f.policies.Snapshot().RoutingL09A(), "behaviorStale turns L-09a off")
}

func TestCacheL09ACounterCases(t *testing.T) {
	now := time.Now()
	withOwner := func(edit func(*Account)) []Account {
		accounts := l09aAccounts(now)
		edit(&accounts[0])
		return accounts
	}
	type expect struct {
		arm, skip, unusable string
	}
	cases := []struct {
		name     string
		opts     l09aFixtureOptions
		prepare  func(*l09aFixture, context.Context)
		expected expect
	}{
		{"auth_before_window_filled", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			a.Status, a.ErrorMessage = StatusError, "Authentication failed (401): invalid or expired credentials"
		})}, nil, expect{"on", "hint_unusable", "host_suspect"}},
		{"auth_host_suspect", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			a.Status, a.ErrorMessage = StatusError, "Authentication failed (401): invalid or expired credentials"
		})}, func(f *l09aFixture, _ context.Context) {
			f.policies.Snapshot().routingL09A.since = now.Add(-11 * time.Minute)
			f.policies.l09a.observe("openai|relay-a.example", 1, false, true, now)
			f.policies.l09a.observe("openai|relay-a.example", 9, false, true, now)
		}, expect{"on", "hint_unusable", "host_suspect"}},
		{"balance_host_suspect", l09aFixtureOptions{}, func(f *l09aFixture, _ context.Context) {
			f.policies.l09a.observe("openai|relay-a.example", 8, false, true, now)
			f.policies.l09a.observe("openai|relay-a.example", 9, false, true, now)
		}, expect{"on", "hint_unusable", "host_suspect"}},
		{"oauth_official_host", l09aFixtureOptions{accounts: withOwner(func(a *Account) { a.Type = AccountTypeOAuth })}, nil, expect{"on", "hint_unusable", "official_host"}},
		{"non_json_last_error", l09aFixtureOptions{}, func(f *l09aFixture, _ context.Context) {
			f.policies.l09a.observe("openai|relay-a.example", 1, true, false, now)
		}, expect{"on", "hint_unusable", "non_json"}},
		{"non_json_evidence", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			a.TempUnschedulableReason = resilience.BalancePauseReason("HTTP 402: <!DOCTYPE html><title>relay</title>")
		})}, nil, expect{"on", "hint_unusable", "non_json"}},
		{"reason_not_listed", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			a.Status, a.ErrorMessage = StatusDisabled, ""
		})}, nil, expect{"on", "hint_unusable", "reason_not_listed"}},
		{"non_hard_rate_limit", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			reset := now.Add(10 * time.Minute)
			a.TempUnschedulableUntil, a.TempUnschedulableReason = nil, ""
			a.RateLimitResetAt = &reset
		})}, nil, expect{"none", "sticky_present", ""}},
		{"non_hard_temp_unschedulable", l09aFixtureOptions{accounts: withOwner(func(a *Account) {
			a.TempUnschedulableReason = "upstream transport failure"
		})}, nil, expect{"none", "sticky_present", ""}},
		{"no_same_host_candidate", l09aFixtureOptions{accounts: func() []Account {
			accounts := l09aAccounts(now)
			accounts[2].Credentials["base_url"] = "https://relay-c.example/v1"
			return accounts
		}()}, nil, expect{"on", "no_same_host_candidate", ""}},
		{"insufficient_samples", l09aFixtureOptions{}, func(f *l09aFixture, _ context.Context) {
			account := l09aAccounts(now)[2]
			delete(f.s.cacheTelemetry().scores, cacheSampleKey(f.diagnostic(), &account, f.model))
		}, expect{"on", "insufficient_samples", ""}},
		{"arm_off", l09aFixtureOptions{rollout: l09aInt(1)}, nil, expect{"off", "arm_off", ""}},
		{"fallback_request", l09aFixtureOptions{}, func(f *l09aFixture, ctx context.Context) {
			require.True(t, resilience.StateFrom(ctx).MarkFallback(5))
		}, expect{"none", "excluded_path", ""}},
		{"quarantine_fail_open", l09aFixtureOptions{}, nil, expect{"none", "excluded_path", ""}},
	}
	require.GreaterOrEqual(t, l09aCohort(l09aSession), 1, "arm_off needs the session outside a 1%% arm")
	for _, tc := range cases {
		for _, advanced := range []bool{false, true} {
			opts := tc.opts
			opts.advanced = advanced
			t.Run(fmt.Sprint(tc.name, "/advanced=", advanced), func(t *testing.T) {
				f := newL09AFixture(t, opts)
				c, ctx, trace := f.request(t)
				if tc.prepare != nil {
					tc.prepare(f, ctx)
				}
				if tc.name == "quarantine_fail_open" {
					ctx = withOpenAIProxyStreamQuarantineBypass(ctx)
					c.Request = c.Request.WithContext(ctx)
				}
				f.s.ArmCacheL09A(c, ctx)
				before := f.policies.L09ACounters()
				f.selectAccount(t, ctx, nil)
				arm, applied, skip := l09aTraceFields(trace)
				require.Equal(t, tc.expected.arm, arm)
				require.False(t, applied)
				require.Equal(t, tc.expected.skip, skip)
				after := f.policies.L09ACounters()
				require.Equal(t, before.SkipTotal[skip]+1, after.SkipTotal[skip])
				if tc.expected.unusable != "" {
					require.Equal(t, before.HintUnusableTotal[tc.expected.unusable]+1, after.HintUnusableTotal[tc.expected.unusable])
				}
			})
		}
	}
}

// A selection a sticky owner finally served (claim reconciliation lost to a
// concurrent binding) reports sticky_present, whatever an earlier pass of
// the same selection recorded (cache_trace l09a allOf: sticky_hit and
// prev_owner allow only the pre-arm reasons).
func TestCacheL09AFinishStickyWins(t *testing.T) {
	f := newL09AFixture(t, l09aFixtureOptions{})
	for _, decision := range []OpenAIAccountScheduleDecision{
		{StickySessionHit: true, Layer: openAIAccountScheduleLayerLoadBalance},
		{StickyPreviousHit: true, Layer: openAIAccountScheduleLayerPreviousResponse},
	} {
		for _, recorded := range []string{"no_hint", "none", "arm_off"} {
			_, ctx, trace := f.request(t)
			rec := &l09aRequest{policy: f.policies.Snapshot().RoutingL09A(), arm: "on", skip: recorded}
			slot := &resilience.CacheL09A{Record: rec}
			slot.Phase.Store(resilience.CacheL09AInFirstSelection)
			f.s.l09aFinish(ctx, slot, nil, decision)
			arm, applied, skip := l09aTraceFields(trace)
			require.Equal(t, "none", arm)
			require.False(t, applied)
			require.Equal(t, "sticky_present", skip)
		}
	}
}

// The AUTH whitelist: with the window filled and no other failing account
// on the host, the hint is usable.
func TestCacheL09AAuthOwnerWithCorroboration(t *testing.T) {
	now := time.Now()
	accounts := l09aAccounts(now)
	accounts[0].TempUnschedulableUntil, accounts[0].TempUnschedulableReason = nil, ""
	accounts[0].Status, accounts[0].ErrorMessage = StatusError, "Authentication failed (401): invalid or expired credentials"
	f := newL09AFixture(t, l09aFixtureOptions{accounts: accounts})
	f.policies.Snapshot().routingL09A.since = now.Add(-11 * time.Minute)
	f.policies.l09a.observe("openai|relay-a.example", 1, false, true, now)
	c, ctx, trace := f.request(t)
	f.s.ArmCacheL09A(c, ctx)
	require.Equal(t, int64(3), f.selectAccount(t, ctx, nil).Account.ID)
	_, applied, _ := l09aTraceFields(trace)
	require.True(t, applied)
}

// The permit flips on the first entry: a failover reselection, a handler
// reselection and an unarmed caller never evaluate L-09a.
func TestCacheL09AOnlyFirstSelection(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint("advanced=", advanced), func(t *testing.T) {
			f := newL09AFixture(t, l09aFixtureOptions{advanced: advanced})
			c, ctx, trace := f.request(t)
			f.s.ArmCacheL09A(c, ctx)
			require.Equal(t, int64(3), f.selectAccount(t, ctx, nil).Account.ID)
			slot := resilience.StateFrom(ctx).CacheL09A()
			require.Equal(t, resilience.CacheL09ADone, slot.Phase.Load())
			counters := f.policies.L09ACounters()
			// Failover: the owner binding is back and must stay a normal sticky.
			f.rebind()
			f.s.ArmCacheL09A(c, ctx)
			require.Same(t, slot, resilience.StateFrom(ctx).CacheL09A(), "a request is armed once")
			f.selectAccount(t, ctx, map[int64]struct{}{3: {}})
			require.Nil(t, f.s.l09aFirst(ctx))
			require.Equal(t, counters, f.policies.L09ACounters(), "later selections record nothing")
			arm, applied, skip := l09aTraceFields(trace)
			require.Equal(t, "on", arm)
			require.True(t, applied, "the trace keeps the first selection's outcome")
			require.Equal(t, "none", skip)

			// An unknown caller that never arms: not a first selection.
			f.rebind()
			_, ctx, trace = f.request(t)
			f.selectAccount(t, ctx, nil)
			arm, _, _ = l09aTraceFields(trace)
			require.Empty(t, arm)
			d := trace.data
			l09aTraceDefaults(&d, f.policies.Snapshot())
			require.Equal(t, "not_first_selection", d.L09ASkip)

			// A request whose selection already began cannot be armed.
			f.rebind()
			c, ctx, _ = f.request(t)
			resilience.StateFrom(ctx).NoteTurn(f.group, f.model, l09aSession)
			f.s.ArmCacheL09A(c, ctx)
			require.Nil(t, resilience.StateFrom(ctx).CacheL09A())
		})
	}
}

// The fallback targets travel with the snapshot; a request in such a group
// is excluded, and an unknown group role fails closed.
func TestCacheL09AFallbackTargetGroup(t *testing.T) {
	f := newL09AFixture(t, l09aFixtureOptions{})
	policy := f.policies.Snapshot().RoutingL09A()
	require.True(t, policy.fallbackTarget(77))
	require.False(t, policy.fallbackTarget(10))
	c, ctx, _ := f.request(t)
	target := int64(77)
	c.Set("api_key", &APIKey{ID: 1, GroupID: &target})
	f.s.ArmCacheL09A(c, ctx)
	rec := resilience.StateFrom(ctx).CacheL09A().Record.(*l09aRequest)
	require.True(t, rec.excluded)
	require.Nil(t, newL09AFixture(t, l09aFixtureOptions{groupsFail: true}).policies.Snapshot().RoutingL09A())
}

// INV-19 and the zero-execution feed: the host window is fed only while
// L-09a is effective, and never from #126 statistics.
func TestCacheL09AUpstreamErrorFeed(t *testing.T) {
	account := l09aAccounts(time.Now())[2]
	off := newL09AFixture(t, l09aFixtureOptions{mode: "score"})
	off.s.l09aObserveUpstreamError(&account, http.StatusUnauthorized, nil, []byte(`{"error":{"message":"bad key"}}`))
	require.Empty(t, off.policies.l09a.accounts)
	on := newL09AFixture(t, l09aFixtureOptions{})
	on.s.l09aObserveUpstreamError(&account, http.StatusUnauthorized, nil, []byte(`<html>blocked</html>`))
	suspect, html := on.policies.l09a.suspect("openai|relay-a.example", 3, time.Now())
	require.False(t, suspect)
	require.True(t, html)
	other := l09aAccounts(time.Now())[0]
	on.s.l09aObserveUpstreamError(&other, http.StatusBadGateway, nil, []byte(`{"error":{"message":"bad gateway"}}`))
	suspect, _ = on.policies.l09a.suspect("openai|relay-a.example", 3, time.Now())
	require.True(t, suspect, "two distinct failing accounts make the host suspect")
	oauth := account
	oauth.Type = AccountTypeOAuth
	on.s.l09aObserveUpstreamError(&oauth, http.StatusUnauthorized, nil, nil)
	_, known := on.policies.l09a.hosts["openai|official"]
	require.False(t, known)
}

// Off-state allocations (kickoff §6): an armed-but-off request allocates
// exactly what a request without any L-09a policy does, alternating the
// two arms in one fixture and taking the minimum of five rounds each.
func TestCacheL09AOffStateAllocsEqual(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for name, o := range map[string]l09aFixtureOptions{
			"capability_false": {capability: l09aBool(false)},
			"mode_score":       {mode: "score"},
			"rollout_zero":     {rollout: l09aInt(0)},
			"static_off":       {aware: l09aBool(false)},
		} {
			o.advanced = advanced
			t.Run(fmt.Sprint(name, "/advanced=", advanced), func(t *testing.T) {
				f := newL09AFixture(t, o)
				run := func(withPolicy bool) float64 {
					return testing.AllocsPerRun(50, func() {
						f.rebind()
						c, ctx, _ := f.request(t)
						if !withPolicy {
							c.Set(cachePolicySnapshotGinKey, (*CachePolicySnapshot)(nil))
						}
						f.s.ArmCacheL09A(c, ctx)
						group := f.group
						selection, _, err := f.s.SelectAccountWithSchedulerForCapability(ctx, &group, "", l09aSession, f.model, nil, OpenAIUpstreamTransportAny, "", false, false, true)
						if err != nil || selection == nil {
							panic("selection failed")
						}
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
					})
				}
				best := [2]float64{-1, -1}
				for round := 0; round < 5; round++ {
					for arm, withPolicy := range []bool{false, true} {
						if n := run(withPolicy); best[arm] < 0 || n < best[arm] {
							best[arm] = n
						}
					}
				}
				t.Logf("L09A_OFF_ALLOCS name=%s advanced=%t no_policy=%v off_policy=%v", name, advanced, best[0], best[1])
				require.Equal(t, best[0], best[1])
			})
		}
	}
}

func TestCachePolicyL09ARules(t *testing.T) {
	// History records the new leaf; old storage gets 0 in memory only.
	require.True(t, cachePolicyHistoryField("routing", "affinity_rollout_percent"))
	f := newL09AFixture(t, l09aFixtureOptions{})
	f.policies.repo.(*policyTestStore).rows["cache_routing_policy"] = `{"rev":4,"spec":{"affinity_mode":"score"}}`
	require.NoError(t, f.policies.Refresh(context.Background()))
	view, err := f.policies.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, int64(4), view.Stored.Rev)
	require.Equal(t, 0, view.Stored.Spec["affinity_rollout_percent"])
	require.Equal(t, `{"rev":4,"spec":{"affinity_mode":"score"}}`, f.policies.repo.(*policyTestStore).rows["cache_routing_policy"], "the fill is never written back")

	// prev_host with rollout 0 is stored but has no effect.
	f = newL09AFixture(t, l09aFixtureOptions{rollout: l09aInt(0)})
	view, err = f.policies.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "off", view.Effective.State)
	require.Contains(t, view.Effective.Warnings, "no_effect_rollout_zero")
	require.Nil(t, f.policies.Snapshot().RoutingL09A())

	// prev_host+quality cannot be stored; a stored value is a parse error.
	f = newL09AFixture(t, l09aFixtureOptions{mode: "prev_host+quality"})
	require.Nil(t, f.policies.Snapshot().RoutingL09A())
	view, err = f.policies.Get(context.Background(), "routing")
	require.NoError(t, err)
	require.Equal(t, "parse_error", view.Effective.State)
}

// The AUTH class reads the error status text that the existing 401 handling
// writes (X P3_REVIEW appendix): drive real 401 bodies of an OpenAI API-key
// account through HandleUpstreamError and classify what SetError stored, so
// a changed wording cannot silently disable or widen the AUTH whitelist.
func TestCacheL09AAuthClassMatchesSetErrorText(t *testing.T) {
	now := time.Now()
	for name, body := range map[string]string{
		"error_message": `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`,
		"detail":        `{"detail":"Unauthorized"}`,
		"token_revoked": `{"error":{"code":"token_revoked","message":"revoked"}}`,
		"plain_text":    `unauthorized`,
		"empty_body":    ``,
	} {
		t.Run(name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
			rl.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(body))
			require.Equal(t, 1, repo.setErrorCalls)
			owner := *account
			owner.Status, owner.ErrorMessage = StatusError, repo.lastErrorMsg
			require.Equal(t, "auth", l09aOwnerHardClass(&owner, now), repo.lastErrorMsg)
		})
	}
	// Other error statuses stay outside the AUTH class.
	repo := &rateLimitAccountRepoStub{}
	rl := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	rl.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, []byte(`{"error":{"message":"organization has been disabled"}}`))
	require.Equal(t, 1, repo.setErrorCalls)
	owner := *account
	owner.Status, owner.ErrorMessage = StatusError, repo.lastErrorMsg
	require.Equal(t, "other", l09aOwnerHardClass(&owner, now))
}
