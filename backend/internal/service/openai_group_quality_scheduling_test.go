//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

type schedulingQualityView struct {
	veto  map[[2]int64]bool
	tiers map[int64]int
}

func (v *schedulingQualityView) Vetoed(g, a int64) bool   { return v.veto[[2]int64{g, a}] }
func (v *schedulingQualityView) OrderTier(_, a int64) int { return v.tiers[a] }

type qualityCapacityCache struct {
	schedulerTestConcurrencyCache
	start         time.Time
	openAfter     time.Duration
	allBusy       bool
	waitCount     int
	loadRates     map[int64]int
	acquireErrors map[int64]error
	releases      atomic.Int64
	waits         atomic.Int64
}

func (c *qualityCapacityCache) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	if err := c.acquireErrors[id]; err != nil {
		return false, err
	}
	return !c.allBusy && (id != 1 || c.openAfter > 0 && time.Since(c.start) >= c.openAfter), nil
}
func (c *qualityCapacityCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases.Add(1)
	return nil
}
func (c *qualityCapacityCache) GetAccountWaitingCount(context.Context, int64) (int, error) {
	c.waits.Add(1)
	return c.waitCount, nil
}
func (c *qualityCapacityCache) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	loads := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		loads[account.ID] = &AccountLoadInfo{AccountID: account.ID, LoadRate: c.loadRates[account.ID]}
	}
	return loads, nil
}

// WP2's scenario table uses only the frozen view contract, never evaluator internals.
func TestOpenAIGroupQualityScheduling(t *testing.T) {
	const groupID int64 = 7
	oldRetryDuration, oldRetryInitial := openAIStickyCapacityRetryDuration, openAIStickyCapacityRetryInitial
	openAIStickyCapacityRetryDuration = 25 * time.Millisecond
	openAIStickyCapacityRetryInitial = time.Millisecond
	defer func() {
		openAIStickyCapacityRetryDuration = oldRetryDuration
		openAIStickyCapacityRetryInitial = oldRetryInitial
	}()
	type testCase struct {
		name string
		run  func(*testing.T)
	}
	cases := []testCase{}
	env := func(advanced, load bool) (*OpenAIGatewayService, *schedulerTestGatewayCache, *schedulingQualityView) {
		cfg := newSchedulerTestOpenAIWSV2Config()
		cfg.Gateway.Scheduling.LoadBatchEnabled = load
		cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
		cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
		cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
		cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
		cfg.Gateway.OpenAIWS.LBTopK = 3
		accounts := []Account{}
		for id := int64(1); id <= 3; id++ {
			acc := profitControlWSAccount(id, 0.2, time.Now())
			acc.GroupIDs = []int64{groupID}
			acc.Priority = int(id)
			accounts = append(accounts, acc)
		}
		cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
		view := &schedulingQualityView{veto: map[[2]int64]bool{}, tiers: map[int64]int{}}
		svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: cfg, cache: cache, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(strconv.FormatBool(advanced)), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
		svc.SetGroupQualityGateView(view)
		return svc, cache, view
	}
	qualityBorrowEnv := func(path string) (*OpenAIGatewayService, *schedulerTestGatewayCache, *qualityCapacityCache) {
		svc, cache, _ := env(path == "advanced", path != "no load batch")
		conc := &qualityCapacityCache{start: time.Now()}
		svc.concurrencyService = NewConcurrencyService(conc)
		return svc, cache, conc
	}
	for _, entry := range []string{"legacy text", "advanced text", "WS capability", "compact", "public load awareness", "composite child gate", "model catalog", "dedicated image suppressed"} {
		cases = append(cases, testCase{entry, func(t *testing.T) {
			svc, _, view := env(entry == "advanced text", entry == "public load awareness")
			view.veto[[2]int64{groupID, 1}] = true
			gid := int64(groupID)
			ctx := context.Background()
			var selection *AccountSelectionResult
			var err error
			switch entry {
			case "public load awareness":
				selection, err = svc.SelectAccountWithLoadAwareness(ctx, &gid, "", "gpt-test", nil)
			case "model catalog":
				acc, e := svc.SelectAccountForModel(ctx, &gid, "", "gpt-test")
				err = e
				selection = &AccountSelectionResult{Account: acc}
			case "dedicated image suppressed":
				selection, _, err = svc.SelectAccountWithScheduler(WithOpenAIProfitControlSuppressed(ctx), &gid, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			case "WS capability":
				selection, _, err = svc.SelectAccountWithSchedulerForCapability(ctx, &gid, "", "", "gpt-test", nil, OpenAIUpstreamTransportResponsesWebsocketV2, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			case "composite child gate":
				parent := int64(8)
				resolver := NewCompositeRouteResolver(compositeRouteRepoStub{routes: []CompositeModelRoute{{
					GroupID: parent, PublicModel: "gpt-test", MatchType: CompositeRouteMatchExact,
					TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-test", Endpoint: CompositeRouteEndpointResponses, Enabled: true,
				}}})
				route, resolveErr := resolver.Resolve(ctx, parent, "gpt-test", CompositeRouteEndpointResponses)
				require.NoError(t, resolveErr)
				require.True(t, route.Matched)
				ctx = WithCompositeRouteDecision(ctx, route)
				ctx, _ = svc.WithOpenAIRequestPricingContext(ctx, &parent)
				fallthrough
			default:
				selection, _, err = svc.SelectAccountWithScheduler(ctx, &gid, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, entry == "compact")
			}
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.Account)
			if entry == "model catalog" || entry == "dedicated image suppressed" {
				require.Equal(t, int64(1), selection.Account.ID)
			} else {
				require.NotEqual(t, int64(1), selection.Account.ID)
				require.Equal(t, groupID, selection.groupQualityGate.groupID)
				view.veto[[2]int64{groupID, selection.Account.ID}] = true
				_, vetoed, reason := svc.SelectionAdmissionVetoLatest(ContextWithSelectionProfitGate(context.Background(), selection), selection.Account)
				require.True(t, vetoed)
				require.Equal(t, "group_quality_ejected", reason)
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		}})
	}
	cases = append(cases, testCase{"previous_response exemption survives DB recheck and admission", func(t *testing.T) {
		svc, _, view := env(false, false)
		gid := int64(groupID)
		view.veto[[2]int64{groupID, 1}] = true
		svc.openaiWSStateStore = NewOpenAIWSStateStore(&stubGatewayCache{})
		require.NoError(t, svc.openaiWSStateStore.BindResponseAccount(context.Background(), groupID, "resp_previous", 1, time.Hour))
		selection, err := svc.SelectAccountByPreviousResponseID(context.Background(), &gid, "resp_previous", "gpt-test", nil, false)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, int64(1), selection.Account.ID)
		ctx := ContextWithSelectionProfitGate(context.Background(), selection)
		require.NotNil(t, svc.recheckSelectedOpenAIAccountFromDB(ctx, selection.Account, &gid, PlatformOpenAI, "gpt-test", false, ""))
		_, vetoed, _ := svc.SelectionAdmissionVetoLatest(ctx, selection.Account)
		require.False(t, vetoed)
		selection.ReleaseFunc()
	}})
	cases = append(cases, testCase{"guardian parent exemption", func(t *testing.T) {
		svc, cache, view := env(true, false)
		gid := int64(groupID)
		parentID := "22222222-2222-4222-8222-222222222222"
		cache.sessionBindings["openai:"+DeriveSessionHashFromSeed(parentID)] = 1
		view.veto[[2]int64{groupID, 1}] = true
		ctx := guardianAffinityTestContext(t, codexAutoReviewModel, "guardian", parentID, "")
		selection, decision, err := svc.SelectAccountWithScheduler(ctx, &gid, "", "guardian-child-session", codexAutoReviewModel, nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.Equal(t, openAIAccountScheduleLayerGuardianParent, decision.Layer)
		require.Equal(t, int64(1), selection.Account.ID)
		_, vetoed, _ := svc.SelectionAdmissionVetoLatest(ContextWithSelectionProfitGate(ctx, selection), selection.Account)
		require.False(t, vetoed)
		selection.ReleaseFunc()
	}})
	for _, entry := range []string{"share ordinary", "share missing bound", "share final verification"} {
		cases = append(cases, testCase{entry, func(t *testing.T) {
			e := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
			gid := int64(shareTestGroupID)
			view := &schedulingQualityView{veto: map[[2]int64]bool{{gid, 1}: true}}
			e.svc.SetGroupQualityGateView(view)
			ctx := e.svc.withGroupQualityGate(context.Background(), &gid)
			in := openAIShareSelection{groupID: &gid, platform: PlatformOpenAI, model: "gpt-5.1"}
			if entry == "share final verification" {
				acc, reason := e.svc.verifyOpenAIShareAccount(ctx, in, e.account(1))
				require.Nil(t, acc)
				require.Equal(t, sharesched.Reason("group_quality_ejected"), reason)
				return
			}
			if entry == "share missing bound" {
				e.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{snapshotAccounts: []*Account{e.account(2)}, accountsByID: map[int64]*Account{1: e.account(1), 2: e.account(2)}}}
			}
			cands, _, stats, err := e.svc.shareOpenAICandidates(ctx, in, 1)
			require.NoError(t, err)
			found := false
			for _, c := range cands {
				if c.AccountID == 1 {
					found = true
					require.False(t, c.Eligible)
					require.Equal(t, sharesched.Reason("group_quality_ejected"), c.Reason)
				}
			}
			require.True(t, found)
			require.Contains(t, stats.summary(""), "group_quality_ejected=1")
		}})
	}
	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		for _, atomicCache := range []bool{false, true} {
			for _, excludedOwner := range []bool{false, true} {
				cases = append(cases, testCase{path + "/ejected sticky rebind/atomic=" + strconv.FormatBool(atomicCache) + "/excluded=" + strconv.FormatBool(excludedOwner), func(t *testing.T) {
					svc, cache, view := env(path == "advanced", path != "no load batch")
					gid := int64(groupID)
					session := "session"
					if atomicCache {
						session = "v2:session"
						svc.cache = &atomicStickyTestCache{GatewayCache: cache, bindings: map[string]int64{svc.openAISessionCacheKey(session): 1}}
					} else {
						cache.sessionBindings["openai:"+session] = 1
					}
					view.veto[[2]int64{groupID, 1}] = true
					var excluded map[int64]struct{}
					if excludedOwner {
						excluded = map[int64]struct{}{1: {}}
					}
					selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &gid, "", session, "gpt-test", excluded, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotEqual(t, int64(1), selection.Account.ID)
					require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(context.Background(), selection), &gid, session, selection.Account.ID))
					owner, err := svc.getStickySessionAccountID(context.Background(), &gid, session)
					require.NoError(t, err)
					require.Equal(t, selection.Account.ID, owner)
					selection.ReleaseFunc()
				}})
			}
		}
	}

	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		for _, scenario := range []string{"short retry", "borrow stable", "queue full", "cancel"} {
			cases = append(cases, testCase{path + "/" + scenario, func(t *testing.T) {
				svc, cache, _ := env(path == "advanced", path != "no load batch")
				gid := int64(groupID)
				cache.sessionBindings["openai:session"] = 1
				conc := &qualityCapacityCache{start: time.Now(), allBusy: scenario == "queue full"}
				if scenario == "queue full" {
					conc.waitCount = svc.schedulingConfig().StickySessionMaxWaiting
				}
				if scenario == "short retry" {
					conc.openAfter = openAIStickyCapacityRetryInitial / 2
				}
				svc.concurrencyService = NewConcurrencyService(conc)
				repo, ok := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
				require.True(t, ok)
				for i := range repo.accounts {
					if scenario != "queue full" {
						repo.accounts[i].Priority = 0
					}
				}
				svc.accountRepo = repo
				ctx := context.Background()
				if scenario == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 5*time.Millisecond)
					defer cancel()
				}
				var borrowed int64
				count := 1
				if scenario == "borrow stable" {
					count = 2
				}
				for i := 0; i < count; i++ {
					selection, _, err := svc.SelectAccountWithScheduler(ctx, &gid, "", "session", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					if scenario == "cancel" {
						require.ErrorIs(t, err, context.DeadlineExceeded)
						require.Nil(t, selection)
						return
					}
					require.NoError(t, err)
					require.NotNil(t, selection)
					if scenario == "queue full" {
						require.False(t, selection.Acquired)
						require.Equal(t, int64(1), selection.WaitPlan.AccountID, "fallback ranks the full pool, including the best owner")
						require.Nil(t, selection.ReleaseFunc)
						require.Equal(t, svc.schedulingConfig().FallbackMaxWaiting, selection.WaitPlan.MaxWaiting)
						require.True(t, selection.StickyCapacitySpillover)
					} else {
						require.True(t, selection.Acquired)
						require.NotNil(t, selection.ReleaseFunc)
						if scenario == "short retry" {
							require.Equal(t, int64(1), selection.Account.ID)
							require.Less(t, time.Since(conc.start), 2*time.Second)
						} else {
							require.NotEqual(t, int64(1), selection.Account.ID)
							require.True(t, selection.StickyCapacitySpillover)
							if i > 0 {
								require.Equal(t, borrowed, selection.Account.ID)
							}
							borrowed = selection.Account.ID
						}
						selection.ReleaseFunc()
						require.Equal(t, int64(i+1), conc.releases.Load())
					}
					if scenario == "queue full" {
						require.Equal(t, int64(1), conc.waits.Load())
					} else {
						require.Zero(t, conc.waits.Load())
					}
					require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
				}
			}})
		}
	}
	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		cases = append(cases, testCase{"Redis slot error returns sticky WaitPlan/" + path, func(t *testing.T) {
			svc, cache, _ := env(path == "advanced", path != "no load batch")
			gid := int64(groupID)
			cache.sessionBindings["openai:session"] = 1
			conc := &qualityCapacityCache{start: time.Now(), acquireErrors: map[int64]error{1: errors.New("redis unavailable")}}
			svc.concurrencyService = NewConcurrencyService(conc)
			selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &gid, "", "session", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.NotNil(t, selection.WaitPlan)
			require.Equal(t, int64(1), selection.WaitPlan.AccountID)
			require.Equal(t, svc.schedulingConfig().StickySessionMaxWaiting, selection.WaitPlan.MaxWaiting)
		}})
	}
	cases = append(cases, testCase{"non OpenAI sticky full retains advanced concurrency escape", func(t *testing.T) {
		svc, cache, _ := env(true, true)
		gid := int64(groupID)
		cache.sessionBindings["openai:session"] = 1
		repo, ok := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
		require.True(t, ok)
		for i := range repo.accounts {
			repo.accounts[i].Platform = PlatformGrok
			repo.accounts[i].Credentials = map[string]any{"model_mapping": map[string]any{"grok-4": "grok-4"}}
		}
		svc.accountRepo = repo
		svc.concurrencyService = NewConcurrencyService(&qualityCapacityCache{start: time.Now()})
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(context.Background(), &gid, "", "session", "grok-4", nil,
			OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true, PlatformGrok)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.True(t, selection.Acquired)
		require.Equal(t, int64(2), selection.Account.ID)
		require.Nil(t, selection.WaitPlan)
		selection.ReleaseFunc()
	}})
	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		path := path
		cases = append(cases, testCase{"quality routing borrow keeps binding and request filters/" + path, func(t *testing.T) {
			svc, _, _ := qualityBorrowEnv(path)
			repo, ok := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
			require.True(t, ok)
			for i := range repo.accounts {
				if repo.accounts[i].Credentials == nil {
					repo.accounts[i].Credentials = make(map[string]any)
				}
				repo.accounts[i].Credentials["base_url"] = map[int64]string{
					1: "https://quality-owner.example", 2: "https://quality-excluded.example", 3: "https://quality-avoided.example",
				}[repo.accounts[i].ID]
			}
			borrowed := profitControlWSAccount(4, 0.2, time.Now())
			borrowed.GroupIDs = []int64{groupID}
			borrowed.Credentials = make(map[string]any)
			borrowed.Credentials["base_url"] = "https://quality-borrowed.example"
			repo.accounts = append(repo.accounts, borrowed)
			svc.accountRepo = repo
			now := time.Now().UnixMilli()
			avoid := map[string]OpenAIQualityAvoid{"3": {
				Provider: qualityProvider(&repo.accounts[2]), At: now, Until: now + int64(time.Minute/time.Millisecond),
			}}
			q := &openAIQualityRequest{scope: "quality-borrow-filter", group: groupID, apiID: 91, session: "quality-session", model: "gpt-test", loaded: true,
				state: OpenAIQualityState{Generation: 1, Owner: 1, Binding: 1, Avoid: avoid}}
			ctx := context.WithValue(withOpenAIQualityRouting(context.Background()), openAIQualityContextKey{}, q)
			gid := groupID
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &gid, "", q.session, q.model, map[int64]struct{}{2: {}}, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(4), selection.Account.ID, "the caller exclusion and provider avoid must remain active during borrowing")
			require.True(t, selection.StickyCapacitySpillover)
			require.Equal(t, int64(1), q.state.Binding, "a capacity borrow must not migrate quality routing's durable binding")
			selection.ReleaseFunc()
		}})
	}
	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		path := path
		cases = append(cases, testCase{"quality routing borrow is stable while the first borrow remains in flight/" + path, func(t *testing.T) {
			svc, _, conc := qualityBorrowEnv(path)
			repo, ok := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
			require.True(t, ok)
			borrowed := profitControlWSAccount(4, 0.2, time.Now())
			borrowed.GroupIDs = []int64{groupID}
			borrowed.Priority = 0
			for i := range repo.accounts {
				repo.accounts[i].Priority = 0
			}
			repo.accounts = append(repo.accounts, borrowed)
			svc.accountRepo = repo
			q := &openAIQualityRequest{scope: "quality-borrow-stable", group: groupID, apiID: 92, session: "quality-session", model: "gpt-test", loaded: true,
				state: OpenAIQualityState{Generation: 1, Owner: 1, Binding: 1}}
			ctx := context.WithValue(withOpenAIQualityRouting(context.Background()), openAIQualityContextKey{}, q)
			gid := groupID
			var first int64
			var firstRelease func()
			for i := 0; i < 2; i++ {
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &gid, "", q.session, q.model, nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.True(t, selection.StickyCapacitySpillover)
				if i == 0 {
					first = selection.Account.ID
				} else {
					require.Equal(t, first, selection.Account.ID)
				}
				require.Equal(t, int64(1), q.state.Binding)
				if i == 0 && path != "no load batch" {
					conc.loadRates = map[int64]int{1: 0, 2: 0, 3: 0, selection.Account.ID: 95}
				}
				if i == 0 {
					firstRelease = selection.ReleaseFunc
				} else {
					selection.ReleaseFunc()
				}
			}
			require.NotNil(t, firstRelease)
			firstRelease()
		}})
	}
	for _, path := range []string{"no load batch", "load aware", "advanced"} {
		cases = append(cases, testCase{"quality borrow prefers recent success across the load band/" + path, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc, _, conc := qualityBorrowEnv(path)
			settings := &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
				openAIAdvancedSchedulerSettingKey:              strconv.FormatBool(path == "advanced"),
				SettingKeyOpenAILowUpstreamRatePriorityEnabled: "true",
			}}
			svc.rateLimitService = &RateLimitService{settingService: NewSettingService(settings, svc.cfg)}
			repo, ok := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
			require.True(t, ok)
			for i := range repo.accounts {
				repo.accounts[i].Priority = 0
			}
			recent := profitControlWSAccount(4, 0.2, time.Now())
			recent.GroupIDs = []int64{groupID}
			recent.Priority = 0
			moreExpensive := profitControlWSAccount(5, 0.8, time.Now())
			moreExpensive.GroupIDs = []int64{groupID}
			moreExpensive.Priority = 0
			repo.accounts = append(repo.accounts, recent, moreExpensive)
			svc.accountRepo = repo
			conc.loadRates = map[int64]int{1: 0, 2: 0, 3: 95, 4: 0, 5: 0}
			session := "quality-recent-success-session"
			svc.recentSessionSuccess().record(openAIRecentSessionKey{groupID, session, "gpt-test"}, 3, time.Now())
			q := &openAIQualityRequest{scope: "quality-borrow-recent", group: groupID, apiID: 93, session: session, model: "gpt-test", loaded: true,
				state: OpenAIQualityState{Generation: 1, Owner: 1, Binding: 1}}
			ctx := context.WithValue(withOpenAIQualityRouting(context.Background()), openAIQualityContextKey{}, q)
			gid := groupID
			selection, _, err := svc.selectAccountWithQualityRouting(ctx, &gid, "", session, "gpt-test", nil,
				OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, true)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(3), selection.Account.ID, "recent success must outrank load-band ordering in the same priority and cost cohort")
			require.True(t, selection.StickyCapacitySpillover)
			selection.ReleaseFunc()
		}})
	}
	cases = append(cases, testCase{"last resort retains profit and hard gates", func(t *testing.T) {
		svc, _, view := env(false, false)
		view.veto[[2]int64{groupID, 1}] = true
		gid := int64(groupID)
		ctx := svc.withGroupQualityGate(withResilienceLastResortGate(context.Background()), &gid)
		acc := profitControlWSAccount(1, 0.8, time.Now())
		veto, _ := groupQualityGateVeto(ctx, &acc)
		require.False(t, veto)
		ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: gid, threshold: 0.5})
		_, veto, reason := svc.SelectionAdmissionVetoLatest(ctx, &acc)
		require.True(t, veto)
		require.Equal(t, openAIProfitFilterReasonThreshold, reason)
		acc.Schedulable = false
		require.NotEmpty(t, openAICompatibleAccountEligibilityFailureReason(ctx, &acc, PlatformOpenAI, "gpt-test", false, ""))
	}})
	for _, row := range []struct {
		name                   string
		low                    bool
		at, bt, ap, bp, al, bl int
		ar, br                 float64
		unlimited, want        bool
	}{
		{name: "tier before priority and cost", low: true, at: 0, bt: 2, ap: 9, bp: 0, ar: 0.9, br: 0.1, want: true},
		{name: "trial after fast", low: true, at: 1, bt: 0, ar: 0.1, br: 0.9, want: false},
		{name: "trial before other", low: true, at: 1, bt: 2, ar: 0.9, br: 0.1, want: true},
		{name: "priority before capacity", low: true, ap: 0, bp: 1, al: 90, bl: 0, ar: 0.9, br: 0.1, want: true},
		{name: "capacity before cost", low: true, al: 80, bl: 0, ar: 0.1, br: 0.9, want: false},
		{name: "cost before load", low: true, al: 50, bl: 0, ar: 0.1, br: 0.9, want: true},
		{name: "unlimited neutral", low: true, al: 90, bl: 0, ar: 0.1, br: 0.9, unlimited: true, want: true},
		{name: "flag off retains original order", low: false, at: 2, bt: 0, al: 0, bl: 50, ar: 0.9, br: 0.1, want: true},
		{name: "no data fast then ID", low: true, ar: 0.2, br: 0.2, want: true},
	} {
		cases = append(cases, testCase{row.name, func(t *testing.T) {
			a, b := &Account{ID: 1, Priority: row.ap, Concurrency: 10}, &Account{ID: 2, Priority: row.bp, Concurrency: 10}
			if row.unlimited {
				a.Concurrency = 0
			}
			order := openAICandidateOrder{rates: openAILegacyUpstreamRateOrder{enabled: row.low, rates: map[int64]float64{1: row.ar, 2: row.br}}, tiers: map[int64]int{1: row.at, 2: row.bt}, groupID: groupID}
			require.Equal(t, row.want, openAICandidateLess(accountWithLoad{a, &AccountLoadInfo{LoadRate: row.al}}, accountWithLoad{b, &AccountLoadInfo{LoadRate: row.bl}}, order))
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
}
