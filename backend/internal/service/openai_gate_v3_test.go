package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func gateV3Account(id int64, priority int) Account {
	return Account{ID: id, Priority: priority, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10, GroupIDs: []int64{7}}
}
func gateV3Hold(s *OpenAIGatewayService, id int64, now time.Time) {
	for i := 0; i < 6; i++ {
		s.getOpenAIAccountLatencyHoldState().record(id, "gpt-test", 11*time.Second, now)
	}
}
func TestGateV3LatencyEscalation(t *testing.T) {
	state := newOpenAIAccountLatencyHoldState(16)
	now := time.Unix(1000, 0)
	for i, want := range []time.Duration{3, 6, 12, 15, 15} {
		var entry openAIAccountLatencyHoldEntry
		var started bool
		for n := 0; n < 6; n++ {
			entry, started, _, _ = state.record(1, "gpt-test", 10*time.Second, now)
		}
		require.True(t, started)
		require.Equal(t, want*time.Minute, entry.holdUntil.Sub(now))
		require.Equal(t, min(i+1, 4), entry.level)
		require.Zero(t, entry.weightedSamples)
		now = entry.holdUntil
		require.False(t, state.snapshot(1, "gpt-test", now).Held)
	}
	now = now.Add(30*time.Minute + time.Nanosecond)
	var entry openAIAccountLatencyHoldEntry
	for n := 0; n < 6; n++ {
		entry, _, _, _ = state.record(1, "gpt-test", 10*time.Second, now)
	}
	require.Equal(t, 1, entry.level)
	require.Equal(t, now.Add(3*time.Minute), entry.holdUntil)
	require.False(t, newOpenAIAccountLatencyHoldState(16).snapshot(1, "gpt-test", now).Held)
}
func TestGateV3LatencyBoundariesAndClaudeUnchanged(t *testing.T) {
	now := time.Unix(2000, 0)
	for _, tc := range []struct {
		name           string
		fast, slow     int
		threshold      float64
		escalate, want bool
	}{
		{"below 30 percent", 8, 3, .30, true, false}, {"at 30 percent", 7, 3, .30, true, true},
		{"Claude retains 35 percent", 7, 3, .35, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newOpenAIAccountLatencyHoldState(16)
			for n := 0; n < tc.fast; n++ {
				state.recordWithPolicy(1, "gpt-test", time.Second, now, tc.threshold, tc.escalate)
			}
			for n := 0; n < tc.slow; n++ {
				state.recordWithPolicy(1, "gpt-test", 10*time.Second, now, tc.threshold, tc.escalate)
			}
			require.Equal(t, tc.want, state.snapshot(1, "gpt-test", now).Held)
		})
	}
	state := newOpenAIAccountLatencyHoldState(16)
	for n := 0; n < 6; n++ {
		state.record(1, "gpt-test", 10*time.Second, now)
	}
	end := now.Add(3 * time.Minute)
	for n := 0; n < 6; n++ {
		_, started, _, _ := state.record(1, "gpt-test", 10*time.Second, end.Add(-time.Second))
		require.False(t, started)
	}
	entry, started, _, _ := state.record(1, "gpt-test", 10*time.Second, end)
	require.True(t, started)
	require.Equal(t, 2, entry.level)
	now = entry.holdUntil.Add(30 * time.Minute)
	for n := 0; n < 6; n++ {
		entry, _, _, _ = state.record(1, "gpt-test", 10*time.Second, now)
	}
	require.Equal(t, 3, entry.level, "exactly 30 minutes still escalates")
	svc := &GatewayService{}
	for round := 0; round < 2; round++ {
		for n := 0; n < 6; n++ {
			svc.recordAnthropicAccountLatencySample(&Account{ID: 1, Platform: PlatformAnthropic}, "claude-test", 11*time.Second, now)
		}
		hold := svc.anthropicLatencyHold.Load().snapshot(1, "claude-test", now)
		require.Equal(t, now.Add(3*time.Minute), hold.HoldUntil)
		now = hold.HoldUntil
	}
}
func TestGateV3LatencyStatus(t *testing.T) {
	svc := &OpenAIGatewayService{}
	now := time.Now()
	gateV3Hold(svc, 1, now)
	for n := 0; n < 6; n++ {
		svc.getOpenAIAccountLatencyHoldState().record(1, "a-model", 11*time.Second, now.Add(time.Second))
	}
	until, models := svc.OpenAIAccountLatencyHoldStatus(1, now)
	require.Equal(t, []string{"a-model", "gpt-test"}, models)
	require.Equal(t, now.Add(3*time.Minute+time.Second), until)
	until, models = svc.OpenAIAccountLatencyHoldStatus(1, until)
	require.True(t, until.IsZero())
	require.Empty(t, models)
}
func gateV3SelectionFixture() (*OpenAIGatewayService, *schedulingQualityView, []Account) {
	accounts := []Account{gateV3Account(1, 9), gateV3Account(2, 0), gateV3Account(3, 10)}
	cfg := newSchedulerTestOpenAIWSV2Config()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}}
	view := &schedulingQualityView{enabled: true, tiers: map[int64]int{1: 0, 2: 2, 3: 0}}
	svc.SetGroupQualityGateView(view)
	gateV3Hold(svc, 1, time.Now())
	return svc, view, accounts
}
func TestGateV3FiveLatencyPartitionPaths(t *testing.T) {
	for _, path := range []string{"best", "load", "load query failure", "fallback wait", "borrow"} {
		t.Run(path, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc, _, accounts := gateV3SelectionFixture()
			group := int64(7)
			ctx := svc.withGroupQualityGate(context.Background(), &group)
			cache := schedulerTestConcurrencyCache{}
			if path == "load query failure" {
				cache.loadBatchErr = errors.New("load unavailable")
			}
			if path == "fallback wait" {
				cache.acquireResults = map[int64]bool{1: false, 2: false, 3: false}
			}
			svc.concurrencyService = NewConcurrencyService(cache)
			if path == "best" {
				selected, _, _ := svc.selectBestAccount(ctx, &group, PlatformOpenAI, accounts, "gpt-test", nil, false, "", false)
				require.Equal(t, int64(3), selected.ID)
				return
			}
			var result *AccountSelectionResult
			var err error
			if path == "borrow" {
				owner := gateV3Account(99, 0)
				result, err = svc.selectOpenAIStickyCapacityBorrow(ctx, &owner, OpenAIAccountScheduleRequest{GroupID: &group, Platform: PlatformOpenAI, RequestedModel: "gpt-test"})
			} else {
				result, err = svc.selectAccountWithLoadAwareness(ctx, &group, PlatformOpenAI, "", "gpt-test", nil, false, "", true)
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, int64(3), result.Account.ID)
			if path == "fallback wait" {
				require.NotNil(t, result.WaitPlan)
			}
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}
func TestGateV3OrderingAndPreferences(t *testing.T) {
	svc, view, accounts := gateV3SelectionFixture()
	group := int64(7)
	a, b := &accounts[0], &accounts[1]
	order := svc.openAICandidateOrderForItems(&group, openAILegacyUpstreamRateOrder{}, []accountWithLoad{{account: a}, {account: b}})
	require.True(t, openAICandidateLess(accountWithLoad{account: a}, accountWithLoad{account: b}, order))
	a.Priority, b.Priority = 0, 0
	view.tiers[2] = 0
	view.predicted = map[int64]int{1: 10000, 2: 5000}
	order = svc.openAICandidateOrderForItems(&group, openAILegacyUpstreamRateOrder{enabled: true, rates: map[int64]float64{1: .2, 2: .2}}, []accountWithLoad{{account: a}, {account: b}})
	require.True(t, openAICandidateLess(accountWithLoad{account: b}, accountWithLoad{account: a}, order))
	delete(view.predicted, 1)
	order = svc.openAICandidateOrderForItems(&group, order.rates, []accountWithLoad{{account: a}, {account: b}})
	require.True(t, openAICandidateLess(accountWithLoad{account: a}, accountWithLoad{account: b}, order), "missing prediction is neutral")
	view.tiers = map[int64]int{1: 0, 2: 1, 3: 2}
	items := []accountWithLoad{}
	for i := range accounts {
		accounts[i].Priority = 0
		items = append(items, accountWithLoad{&accounts[i], &AccountLoadInfo{}})
	}
	shuffleOpenAIWithinQualityGroups(items, view.tiers, nil)
	require.Equal(t, int64(1), items[0].account.ID)
	require.Equal(t, int64(3), items[2].account.ID)
	svc.preferOpenAIReplacementSessionWithTiers(context.Background(), items, &group, "session", "gpt-test", openAILegacyUpstreamRateOrder{}, false, "", view.tiers)
	require.Equal(t, int64(1), items[0].account.ID)
	require.Equal(t, int64(3), items[2].account.ID)
	svc.preferOpenAIQualityCacheCandidates(context.Background(), items, "gpt-test", view.tiers)
	require.Equal(t, int64(1), items[0].account.ID)
	require.Equal(t, int64(3), items[2].account.ID)
	view.enabled = false
	order = svc.openAICandidateOrderForItems(&group, openAILegacyUpstreamRateOrder{}, items)
	require.Nil(t, order.tiers)
}
func TestGateV3AdvancedPoolsAndShare(t *testing.T) {
	group := int64(7)
	svc := &OpenAIGatewayService{cfg: newSchedulerTestOpenAIWSV2Config()}
	svc.cfg.Gateway.OpenAIWS.LBTopK = 1
	view := &schedulingQualityView{enabled: true, tiers: map[int64]int{}}
	var accounts []*Account
	for id := int64(1); id <= 6; id++ {
		a := gateV3Account(id, int(7-id))
		accounts = append(accounts, &a)
		view.tiers[id] = int((id - 1) / 2)
		if id%2 == 0 {
			gateV3Hold(svc, id, time.Now())
		}
	}
	svc.SetGroupQualityGateView(view)
	scheduler := &defaultOpenAIAccountScheduler{service: svc}
	plan := scheduler.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{GroupID: &group, RequestedModel: "gpt-test"}, accounts, nil)
	var got []int64
	for _, c := range plan.selectionOrder {
		got = append(got, c.account.ID)
	}
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6}, got)
	view.enabled = false
	plan = scheduler.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{GroupID: &group, RequestedModel: "gpt-test"}, accounts, nil)
	require.Len(t, plan.selectionOrder, 1, "disabled group retains one TopK pool")
	view.enabled = true
	decision := sharesched.Decision{Steps: []sharesched.Step{{AccountID: 6}, {AccountID: 3}, {AccountID: 1}}}
	ordered := svc.orderOpenAIShareQuality(&group, decision)
	require.Equal(t, int64(1), ordered.Steps[0].AccountID)
	decision = sharesched.Decision{Bound: 6, Steps: []sharesched.Step{{AccountID: 6}, {AccountID: 3}, {AccountID: 1}}}
	ordered = svc.orderOpenAIShareQuality(&group, decision)
	require.Equal(t, int64(6), ordered.Steps[0].AccountID)
	require.Equal(t, int64(1), ordered.Steps[1].AccountID)
}

type gateV3RebindCache struct {
	*atomicStickyTestCache
	fail  bool
	err   error
	calls int
}

func (c *gateV3RebindCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (c *gateV3RebindCache) CompareAndSwapSessionAccountID(_ context.Context, _ int64, key string, owner, target int64, _ time.Duration) (bool, error) {
	c.calls++
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || c.fail || c.bindings[key] != owner {
		return false, c.err
	}
	c.bindings[key] = target
	return true, nil
}
func TestGateV3Migration(t *testing.T) {
	for _, tc := range []struct {
		name                                                                          string
		held, guard, pinned, full, busy, casFail, cacheErr, noTarget, noCAS, disabled bool
		ws, qualityOwned, shared, targetHeld, loadErr, missingLoad                    bool
	}{
		{name: "tier"}, {name: "fixed owner held", held: true}, {name: "guard", guard: true}, {name: "quality pinned", pinned: true},
		{name: "full", full: true}, {name: "slot race", busy: true}, {name: "CAS miss", casFail: true}, {name: "cache error", cacheErr: true},
		{name: "ordinary recovery or bad-memory rejects", noTarget: true}, {name: "strict missing dimension rejects", noTarget: true},
		{name: "WebSocket", ws: true}, {name: "quality routing ownership", qualityOwned: true}, {name: "share scheduling", shared: true}, {name: "target held", targetHeld: true}, {name: "load error", loadErr: true}, {name: "missing fresh load", missingLoad: true}, {name: "cache lacks CAS", noCAS: true}, {name: "disabled", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousLog := slog.Default()
			var eventLog bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&eventLog, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLog) })
			group := int64(7)
			session := "v2:gate-v3"
			owner := gateV3Account(1, 0)
			target := gateV3Account(2, 10)
			unknown := gateV3Account(3, 0)
			cache := &gateV3RebindCache{atomicStickyTestCache: &atomicStickyTestCache{bindings: map[string]int64{"openai:" + session: 1}}, fail: tc.casFail}
			if tc.cacheErr {
				cache.err = errors.New("redis failed")
			}
			var acquired, released []int64
			load := &AccountLoadInfo{}
			if tc.full {
				load.CurrentConcurrency = 10
			}
			loadCache := schedulerTestConcurrencyCache{acquireResults: map[int64]bool{2: !tc.busy}, loadMap: map[int64]*AccountLoadInfo{2: load}, acquiredIDs: &acquired, releasedIDs: &released}
			if tc.loadErr {
				loadCache.loadBatchErr = errors.New("no fresh load")
			}
			if tc.missingLoad {
				loadCache.skipDefaultLoad = true
				loadCache.loadMap = map[int64]*AccountLoadInfo{}
			}
			concurrency := NewConcurrencyService(loadCache)
			svc := &OpenAIGatewayService{cfg: &config.Config{RunMode: config.RunModeStandard}, cache: cache, concurrencyService: concurrency, accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{owner, target, unknown}}}}
			if tc.noCAS {
				svc.cache = cache.atomicStickyTestCache
			}
			view := &schedulingQualityView{enabled: !tc.disabled, tiers: map[int64]int{1: 2}, targets: map[int64]bool{2: !tc.noTarget}}
			svc.SetGroupQualityGateView(view)
			if tc.held {
				view.tiers[1] = 0
				gateV3Hold(svc, 1, time.Now())
			}
			ctx := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
			if tc.guard {
				ctx = withOpenAIStickyMigrationGuard(ctx, "resp-chain", OpenAIUpstreamTransportAny)
			}
			if tc.pinned {
				ctx = WithOpenAIQualityTurnExemption(ctx, true)
			}
			if tc.ws {
				ctx = withOpenAIStickyMigrationGuard(ctx, "", OpenAIUpstreamTransportResponsesWebsocketV2)
			}
			if tc.qualityOwned {
				q := &openAIQualityRequest{group: group, session: session, state: OpenAIQualityState{Generation: 1}}
				ctx = context.WithValue(withOpenAIQualityRouting(ctx), openAIQualityContextKey{}, q)
			}
			if tc.shared {
				share := &ShareSchedulingService{}
				share.state.Store(&shareSchedulingState{settings: ShareSchedulingSettings{Enabled: true}, groups: map[int64]*shareGroupPolicy{group: {groupID: group, policy: sharesched.Policy{Scheduler: sharesched.SchedulerShare}}}})
				svc.shareScheduling = share
			}
			if tc.targetHeld {
				gateV3Hold(svc, 2, time.Now())
			}
			id, result := svc.migrateOpenAILatencyHeldOwner(ctx, &group, PlatformOpenAI, session, "gpt-test", 1, nil, false, "", false)
			want := !tc.guard && !tc.pinned && !tc.full && !tc.busy && !tc.casFail && !tc.cacheErr && !tc.noTarget && !tc.noCAS && !tc.disabled && !tc.ws && !tc.qualityOwned && !tc.shared && !tc.targetHeld && !tc.loadErr && !tc.missingLoad
			require.Equal(t, want, result != nil)
			if want {
				require.Contains(t, eventLog.String(), `"msg":"group_quality_gate.migrate"`)
				require.Contains(t, eventLog.String(), `"from_account_id":1,"to_account_id":2`)
				trigger := "tier"
				if tc.held {
					trigger = "latency_hold"
				}
				require.Contains(t, eventLog.String(), `"trigger":"`+trigger+`"`)
			} else {
				require.NotContains(t, eventLog.String(), `"msg":"group_quality_gate.migrate"`)
			}
			key := openAIQualityMigrationKey{group, session}
			require.Equal(t, want, svc.getOpenAIAccountLatencyHoldState().migration.coolingDown(key, time.Now()))
			if want {
				require.Equal(t, int64(2), id)
				require.Equal(t, int64(2), cache.bindings["openai:"+session])
				require.Equal(t, []int64{2}, acquired)
				result.ReleaseFunc()
				view.tiers[1] = 0
				id, result = svc.migrateOpenAILatencyHeldOwner(ctx, &group, PlatformOpenAI, session, "gpt-test", 2, nil, false, "", false)
				require.Equal(t, int64(2), id)
				require.Nil(t, result, "a healthy new owner never proactively returns")
				view.tiers[2] = 2
				view.targets = map[int64]bool{1: true}
				view.tiers[1] = 0
				id, result = svc.migrateOpenAILatencyHeldOwner(ctx, &group, PlatformOpenAI, session, "gpt-test", 2, nil, false, "", false)
				require.Equal(t, int64(2), id)
				require.Nil(t, result, "successful migration starts cooldown")
				require.NoError(t, svc.deleteStickySessionAccountIDIfOwner(ctx, &group, session, 2))
				_, err := svc.getStickySessionAccountID(ctx, &group, session)
				require.ErrorIs(t, err, ErrStickySessionNotFound, "failure rebind ignores quality cooldown")
			} else {
				require.Equal(t, int64(1), id)
				require.Equal(t, int64(1), cache.bindings["openai:"+session])
				if tc.casFail || tc.cacheErr {
					require.Equal(t, []int64{2}, released)
				}
			}
		})
	}
}
func TestGateV3LowRateScope(t *testing.T) {
	for _, tc := range []struct {
		name, scope, ids string
		group            int64
		advanced, want   bool
	}{
		{"default", "", "", 7, false, true}, {"all", "all", "[]", 7, false, true},
		{"included", "groups", "[7,8]", 7, false, true}, {"excluded", "groups", "[8]", 7, false, false},
		{"empty", "groups", "[]", 7, false, false}, {"advanced", "all", "[]", 7, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			advanced := "false"
			if tc.advanced {
				advanced = "true"
			}
			repo := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{SettingKeyOpenAILowUpstreamRatePriorityEnabled: "true", SettingKeyOpenAILowUpstreamRatePriorityScope: tc.scope, SettingKeyOpenAILowUpstreamRatePriorityGroupIDs: tc.ids, openAIAdvancedSchedulerSettingKey: advanced}}
			svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{settingService: NewSettingService(repo, nil)}}
			require.Equal(t, tc.want, svc.isOpenAILowUpstreamRatePriorityEnabled(context.Background(), &tc.group))
		})
	}
}
func TestGateV3AdvancedMigration(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			group := int64(7)
			session := "legacy-session"
			cache := &gateV3RebindCache{atomicStickyTestCache: &atomicStickyTestCache{bindings: map[string]int64{"openai:" + session: 1}}}
			svc := &OpenAIGatewayService{cache: cache, cfg: newSchedulerTestOpenAIWSV2Config(), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), accountRepo: schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{gateV3Account(1, 0), gateV3Account(2, 1)}}}}
			svc.SetGroupQualityGateView(&schedulingQualityView{enabled: enabled, tiers: map[int64]int{1: 2}, targets: map[int64]bool{2: true}})
			gateV3Hold(svc, 1, time.Now())
			scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
			result, _, err := scheduler.Select(context.Background(), OpenAIAccountScheduleRequest{GroupID: &group, Platform: PlatformOpenAI, SessionHash: session, RequestedModel: "gpt-test", AdvancedScheduler: true})
			require.NoError(t, err)
			require.NotNil(t, result)
			want := int64(1)
			if enabled {
				want = 2
			}
			require.Equal(t, want, result.Account.ID)
			require.Equal(t, want, cache.bindings["openai:"+session])
			result.ReleaseFunc()
		})
	}
}

func TestGateV3CacheAndSessionPreferenceStayInSegment(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.OpenAICache = config.OpenAICacheConfig{AwareRoutingEnabled: true, RolloutPercent: 100}
	group := int64(7)
	view := &schedulingQualityView{enabled: true, tiers: map[int64]int{1: 0, 2: 0, 3: 1, 4: 0}}
	svc.SetGroupQualityGateView(view)
	diag := &OpenAICacheDiagnostic{Session: "v2:segments", Prefix: "prefix", Model: "gpt-test", GroupID: group}
	ctx := context.WithValue(context.Background(), openAICacheDiagnosticKey{}, diag)
	var items []accountWithLoad
	for _, id := range []int64{1, 4, 2, 3} {
		a := gateV3Account(id, 0)
		sample := openAICacheSample{count: 20, input: 2048, read: float64(id) * 400, at: time.Now()}
		for n := range sample.recent {
			sample.recent[n] = sample.at
		}
		svc.cacheTelemetry().scores[cacheSampleKey(diag, &a, "gpt-test")] = sample
		items = append(items, accountWithLoad{&a, &AccountLoadInfo{}})
	}
	gateV3Hold(svc, 2, time.Now())
	svc.preferOpenAIQualityCacheCandidates(ctx, items, "gpt-test", view.tiers)
	require.Equal(t, []int64{4, 1, 2, 3}, []int64{items[0].account.ID, items[1].account.ID, items[2].account.ID, items[3].account.ID})
	svc.recentSessionSuccess().record(openAIRecentSessionKey{group, "session", "gpt-test"}, 3, time.Now())
	svc.recentSessionSuccess().record(openAIRecentSessionKey{group, "session", "gpt-test"}, 2, time.Now())
	svc.preferOpenAIReplacementSession(ctx, items, &group, "session", "gpt-test", openAILegacyUpstreamRateOrder{}, false, "")
	require.Equal(t, int64(2), items[2].account.ID)
	require.Equal(t, int64(3), items[3].account.ID)
}

func TestGateV3MigrationCooldownLRU(t *testing.T) {
	state := openAIQualityMigrationState{}
	now := time.Unix(1000, 0)
	for n := int64(1); n <= openAIQualityMigrationMax; n++ {
		state.note(openAIQualityMigrationKey{n, "session"}, now)
	}
	require.True(t, state.coolingDown(openAIQualityMigrationKey{1, "session"}, now))
	state.note(openAIQualityMigrationKey{openAIQualityMigrationMax + 1, "session"}, now)
	require.Len(t, state.entries, openAIQualityMigrationMax)
	require.False(t, state.coolingDown(openAIQualityMigrationKey{2, "session"}, now))
	require.True(t, state.coolingDown(openAIQualityMigrationKey{1, "session"}, now))
	require.False(t, state.coolingDown(openAIQualityMigrationKey{1, "session"}, now.Add(5*time.Minute)))
}

type gateV3MigrationDBRepo struct {
	upstreamCostCountingAccountRepo
	readIDs []int64
}

func (r *gateV3MigrationDBRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.readIDs = append(r.readIDs, id)
	return r.upstreamCostCountingAccountRepo.GetByID(ctx, id)
}

func TestGateV3MigrationRechecksOnlyOrderedFreeCandidates(t *testing.T) {
	for _, tc := range []struct {
		name                                                 string
		rejectFirstDB, rejectAllDB, rejectFirstSlot, allFull bool
		wantID                                               int64
		wantReads, wantAcquires                              []int64
	}{
		{name: "first DB recheck fails, second qualifies", rejectFirstDB: true, wantID: 3, wantReads: []int64{2, 3}, wantAcquires: []int64{3}},
		{name: "first qualifies, later candidates are not read", wantID: 2, wantReads: []int64{2}, wantAcquires: []int64{2}},
		{name: "first slot fills, second qualifies", rejectFirstSlot: true, wantID: 3, wantReads: []int64{2, 3}, wantAcquires: []int64{2, 3}},
		{name: "all full, no candidate DB reads", allFull: true, wantID: 1},
		{name: "all DB rechecks fail, retain owner", rejectAllDB: true, wantID: 1, wantReads: []int64{2, 3, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := int64(7)
			const session = "v2:migration-db-order"
			snapshot := &openAISnapshotCacheStub{accountsByID: map[int64]*Account{}}
			repo := &gateV3MigrationDBRepo{upstreamCostCountingAccountRepo: upstreamCostCountingAccountRepo{accounts: map[int64]*Account{}}}
			loads := map[int64]*AccountLoadInfo{}
			for id := int64(1); id <= 4; id++ {
				a := gateV3Account(id, int(id))
				snapshot.snapshotAccounts = append(snapshot.snapshotAccounts, &a)
				snapshot.accountsByID[id] = &a
				fresh := a
				fresh.UpdatedAt = time.Now()
				if id > 1 {
					fresh.Concurrency = 8
				}
				if tc.rejectAllDB || tc.rejectFirstDB && id == 2 {
					fresh.Schedulable = false
				}
				repo.accounts[id] = &fresh
				loads[id] = &AccountLoadInfo{}
				if tc.allFull {
					loads[id].CurrentConcurrency = 10
				}
			}
			cache := &gateV3RebindCache{atomicStickyTestCache: &atomicStickyTestCache{bindings: map[string]int64{"openai:" + session: 1}}}
			var acquired []int64
			concurrency := NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: loads, acquiredIDs: &acquired, acquireResults: map[int64]bool{2: !tc.rejectFirstSlot}})
			svc := &OpenAIGatewayService{cfg: newSchedulerTestOpenAIWSV2Config(), cache: cache, accountRepo: repo, schedulerSnapshot: &SchedulerSnapshotService{cache: snapshot}, concurrencyService: concurrency}
			svc.SetGroupQualityGateView(&schedulingQualityView{enabled: true, tiers: map[int64]int{1: 2}, targets: map[int64]bool{2: true, 3: true, 4: true}})
			owner, selection := svc.migrateOpenAILatencyHeldOwner(context.Background(), &group, PlatformOpenAI, session, "gpt-test", 1, nil, false, "", false)
			require.Equal(t, tc.wantID, owner)
			require.Equal(t, tc.wantReads, repo.readIDs, "only attempted candidates may receive a DB recheck")
			require.LessOrEqual(t, repo.calls(), len(tc.wantReads))
			require.Equal(t, tc.wantAcquires, acquired)
			bound, err := cache.GetSessionAccountID(context.Background(), group, "openai:"+session)
			require.NoError(t, err)
			require.Equal(t, tc.wantID, bound)
			if tc.wantID == 1 {
				require.Nil(t, selection)
				require.Zero(t, cache.calls)
				require.False(t, svc.getOpenAIAccountLatencyHoldState().migration.coolingDown(openAIQualityMigrationKey{group, session}, time.Now()))
			} else {
				require.NotNil(t, selection)
				require.Equal(t, 8, selection.Account.Concurrency, "return the DB-rechecked account, not the stale snapshot")
				require.Equal(t, 1, cache.calls)
				selection.ReleaseFunc()
			}
		})
	}
}
