//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func traceRoutingContext() (context.Context, *cacheTrace, *resilience.RequestState) {
	trace := &cacheTrace{data: cacheTraceData{RouteReason: "unknown", RebindCause: "none"}}
	st := resilience.NewRequestState()
	return context.WithValue(resilience.WithRequestState(context.Background(), st), cacheTraceKey{}, trace), trace, st
}

func TestCacheTraceRoutingStickyFailoverSequence(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	for _, loadBatch := range []bool{false, true} {
		ctx, trace, _ := traceRoutingContext()
		group := int64(38100)
		s := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(group, true), loadBatch, schedulerTestConcurrencyCache{})
		session := "v2:synthetic-routing"
		require.NoError(t, s.setStickySessionAccountID(ctx, &group, session, 38102, time.Hour))
		selectAccount := func(excluded map[int64]struct{}) *AccountSelectionResult {
			sel, _, err := s.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", excluded, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
			require.NoError(t, err)
			require.NotNil(t, sel)
			releaseLegacySchedulerDecisionSelection(sel)
			return sel
		}
		require.Equal(t, int64(38102), selectAccount(nil).Account.ID)
		require.Equal(t, "sticky_hit", trace.data.RouteReason)
		NoteCacheTraceFailover(ctx, 1)
		require.Equal(t, int64(38101), selectAccount(map[int64]struct{}{38102: {}}).Account.ID)
		require.Equal(t, "failover_n", trace.data.RouteReason)
		require.Equal(t, 1, trace.routing.failoverN)
	}
}

func TestCacheTraceRoutingNewAndPreviousOwner(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	ctx, trace, _ := traceRoutingContext()
	group := int64(38110)
	s := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(group, true), true, schedulerTestConcurrencyCache{})
	sel, _, err := s.SelectAccountWithSchedulerForCapability(ctx, &group, "", "synthetic-new", "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
	require.NoError(t, err)
	releaseLegacySchedulerDecisionSelection(sel)
	require.Equal(t, "sticky_new", trace.data.RouteReason)
	require.NoError(t, s.getOpenAIWSStateStore().BindResponseAccount(ctx, group, "resp_trace", 38102, time.Hour))
	sel, _, err = s.SelectAccountWithSchedulerForCapability(ctx, &group, "resp_trace", "synthetic-prev", "gpt-5.1", nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityChatCompletions, false, true, true)
	require.NoError(t, err)
	releaseLegacySchedulerDecisionSelection(sel)
	require.Equal(t, int64(38102), sel.Account.ID)
	require.Equal(t, "prev_owner", trace.data.RouteReason)
}

func TestCacheTraceRoutingSoftDiversionPreservesOwner(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	ctx, trace, _ := traceRoutingContext()
	group := int64(38100)
	accounts := newLegacySchedulerDecisionTestAccounts(group, true)
	until := time.Now().Add(time.Minute)
	accounts[1].TempUnschedulableUntil = &until
	accounts[1].TempUnschedulableReason = "transient:synthetic"
	s := newLegacySchedulerDecisionTestService(accounts, true, schedulerTestConcurrencyCache{})
	session := "v2:synthetic-soft-owner"
	require.NoError(t, s.setStickySessionAccountID(ctx, &group, session, 38102, time.Hour))
	sel, _, err := s.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
	require.NoError(t, err)
	require.NotNil(t, sel)
	releaseLegacySchedulerDecisionSelection(sel)
	require.Equal(t, int64(38101), sel.Account.ID)
	require.Equal(t, "soft_divert", trace.data.RouteReason)
	owner, err := s.getStickySessionAccountID(ctx, &group, session)
	require.NoError(t, err)
	require.Equal(t, int64(38102), owner)
}

func TestCacheTraceRoutingOverflowPreservesOwner(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	ctx, trace, _ := traceRoutingContext()
	group := int64(1)
	session := "v2:synthetic-overflow"
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + session: 1}}
	concurrency := stubConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: true}, waitCounts: map[int64]int{1: 1}, loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}, 2: {AccountID: 2, LoadRate: 10}}}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 1
	s := &OpenAIGatewayService{cfg: cfg, cache: cache, concurrencyService: NewConcurrencyService(concurrency), accountRepo: stubOpenAIAccountRepo{accounts: []Account{
		{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 6, Priority: 1, GroupIDs: []int64{group}},
		{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, Concurrency: 6, Priority: 1, GroupIDs: []int64{group}},
	}}}
	sel, _, err := s.SelectAccountWithScheduler(ctx, &group, "", session, "gpt-4", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	releaseLegacySchedulerDecisionSelection(sel)
	require.True(t, sel.StickyCapacitySpillover)
	require.Equal(t, "overflow", trace.data.RouteReason)
	require.Equal(t, int64(1), cache.sessionBindings["openai:"+session])
}

func TestCacheTraceRoutingLastResort(t *testing.T) {
	withResilienceSettings(t, nil)
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	ctx, trace, st := traceRoutingContext()
	group := int64(9)
	until := time.Now().Add(time.Minute)
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	s := &OpenAIGatewayService{cfg: cfg, cache: &schedulerTestGatewayCache{}, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), resilience: NewResilienceService(nil, newMemResilienceStore()), accountRepo: openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, RateLimitResetAt: &until}}}}}
	sel, _, err := s.SelectAccountWithScheduler(ctx, &group, "", "", "gpt-5.5", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, sel)
	releaseLegacySchedulerDecisionSelection(sel)
	require.Equal(t, int64(71), st.LastResort())
	require.Equal(t, "last_resort", trace.data.RouteReason)
}

func TestCacheTraceRoutingRebindOnlyAtThreshold(t *testing.T) {
	withResilienceSettings(t, nil)
	for _, tc := range []struct {
		name     string
		category resilience.Category
		output   bool
		want     string
	}{
		{"pre-output", resilience.CategoryTransient, false, "pre_output_error"},
		{"post-output", resilience.CategoryTransient, true, "post_output"},
		{"stall", resilience.CategoryStall, true, "stall"},
		{"first-event", resilience.CategoryFirstOutputTimeout, false, "first_event_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, trace, st := traceRoutingContext()
			if tc.output {
				st.NoteFirstOutput(time.Now())
			}
			rec := &sessionFailureRecorder{r: NewResilienceService(nil, newMemResilienceStore())}
			rec.Failure(ctx, 7, "synthetic-session", 51, tc.category)
			require.Equal(t, "none", trace.data.RebindCause)
			rec.Failure(ctx, 7, "synthetic-session", 51, tc.category)
			require.Equal(t, tc.want, trace.data.RebindCause)
			require.Equal(t, "session_rebind", trace.data.RouteReason)
			require.True(t, st.Avoided(51))
		})
	}
}

func TestCacheTraceRoutingFallbackAndPrivacy(t *testing.T) {
	for _, noSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("no_session=%t", noSession), func(t *testing.T) {
			f := cacheTraceNewContractFixture(t, "t4-fallback", "responses")
			s, c, body := f.s, f.c, f.body
			st := resilience.NewRequestState()
			require.True(t, st.MarkFallback(7777777))
			// Re-create the ingress context with fallback already selected, as in 0.4.6.
			c.Request = c.Request.WithContext(resilience.WithRequestState(context.Background(), st))
			if noSession {
				c.Request.Header.Del("session_id")
				body = []byte(cacheTraceNoSessionBody)
				require.Empty(t, s.GenerateSessionHash(c, body))
			} else {
				require.NotEmpty(t, s.GenerateSessionHash(c, body))
			}
			trace := cacheTraceFromGin(c)
			require.NotNil(t, trace)
			if noSession {
				require.Empty(t, trace.sessionKey)
				require.Empty(t, trace.data.SessionDigest)
				require.Equal(t, "no_session", trace.data.GapSource)
			}
			require.Equal(t, "fallback_group", trace.data.RouteReason)
			sel := &AccountSelectionResult{Account: &Account{ID: 9}}
			NoteCacheTraceFailover(c.Request.Context(), 2)
			noteCacheTraceSelection(c.Request.Context(), sel, OpenAIAccountScheduleDecision{StickySessionHit: true})
			require.Equal(t, "fallback_group", trace.data.RouteReason)
			var out bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
			defer slog.SetDefault(old)
			trace.emitCacheTrace()
			for _, marker := range []string{"ctc-private", "trace-private", "7777777"} {
				require.NotContains(t, out.String(), marker)
			}
			require.Zero(t, testing.AllocsPerRun(100, func() {
				NoteCacheTraceFailover(context.Background(), 1)
				noteCacheTraceSelection(context.Background(), sel, OpenAIAccountScheduleDecision{})
				noteCacheTraceRebind(context.Background(), resilience.CategoryTransient)
				noteCacheTraceFallback(context.Background())
			}))
		})
	}
}

func TestCacheTraceRoutingAdvancedStickyFailover(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	ctx, trace, _ := traceRoutingContext()
	group := int64(38120)
	s := newLegacySchedulerDecisionTestService(newLegacySchedulerDecisionTestAccounts(group, true), true, schedulerTestConcurrencyCache{})
	s.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true", "false")
	require.True(t, s.isOpenAIAdvancedSchedulerEnabled(ctx))
	session := "v2:synthetic-advanced-trace"
	require.NoError(t, s.setStickySessionAccountID(ctx, &group, session, 38102, time.Hour))
	selectAccount := func(excluded map[int64]struct{}) *AccountSelectionResult {
		sel, _, err := s.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", excluded, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
		require.NoError(t, err)
		require.NotNil(t, sel)
		releaseLegacySchedulerDecisionSelection(sel)
		return sel
	}
	require.Equal(t, int64(38102), selectAccount(nil).Account.ID)
	require.Equal(t, "sticky_hit", trace.data.RouteReason)
	NoteCacheTraceFailover(ctx, 1)
	require.Equal(t, int64(38101), selectAccount(map[int64]struct{}{38102: {}}).Account.ID)
	require.Equal(t, "failover_n", trace.data.RouteReason)
}

// Each signal is supplied through the public/internal observation entry that
// the matching production decision calls. The expected order is PM's table,
// independent of cacheTraceRoutePriority's implementation.
func traceRoutingSignal(t *testing.T, ctx context.Context, st *resilience.RequestState, selection *AccountSelectionResult, decision *OpenAIAccountScheduleDecision, reason string) {
	t.Helper()
	switch reason {
	case "sticky_new":
		decision.Layer = openAIAccountScheduleLayerLoadBalance
	case "sticky_hit":
		decision.StickySessionHit = true
	case "prev_owner":
		decision.StickyPreviousHit = true
	case "overflow":
		selection.StickyCapacitySpillover = true
	case "soft_divert":
		noteCacheTraceSoftOwner(ctx, selection.Account.ID+1)
	case "session_rebind":
		noteCacheTraceRebind(ctx, resilience.CategoryStall)
	case "failover_n":
		NoteCacheTraceFailover(ctx, 2)
	case "fallback_group":
		require.True(t, st.MarkFallback(7777777))
		noteCacheTraceFallback(ctx)
	case "last_resort":
		noteCacheTraceLastResort(ctx, selection.Account.ID)
	default:
		t.Fatalf("unknown test signal %q", reason)
	}
}

func TestCacheTraceRoutingApprovedPriorityEveryPair(t *testing.T) {
	reasons := []string{"sticky_new", "sticky_hit", "prev_owner", "overflow", "soft_divert", "session_rebind", "failover_n", "fallback_group", "last_resort"}
	pairs := 0
	for low := 0; low < len(reasons); low++ {
		for high := low + 1; high < len(reasons); high++ {
			pairs++
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s+%s/reverse=%t", reasons[low], reasons[high], reverse), func(t *testing.T) {
					ctx, trace, st := traceRoutingContext()
					selection := &AccountSelectionResult{Account: &Account{ID: 9}}
					decision := OpenAIAccountScheduleDecision{}
					first, second := reasons[low], reasons[high]
					if reverse {
						first, second = second, first
					}
					traceRoutingSignal(t, ctx, st, selection, &decision, first)
					traceRoutingSignal(t, ctx, st, selection, &decision, second)
					noteCacheTraceSelection(ctx, selection, decision)
					require.Equal(t, reasons[high], trace.data.RouteReason)
					if first == "session_rebind" || second == "session_rebind" {
						require.Equal(t, "stall", trace.data.RebindCause)
					} else {
						require.Equal(t, "none", trace.data.RebindCause)
					}
				})
			}
		}
	}
	require.Equal(t, 36, pairs)
}

func TestCacheTraceRoutingApprovedPriorityCompleteSequence(t *testing.T) {
	ctx, trace, st := traceRoutingContext()
	selection := &AccountSelectionResult{Account: &Account{ID: 9}}
	decision := OpenAIAccountScheduleDecision{}
	for _, reason := range []string{"sticky_new", "sticky_hit", "prev_owner", "overflow", "soft_divert", "session_rebind", "failover_n", "fallback_group", "last_resort"} {
		traceRoutingSignal(t, ctx, st, selection, &decision, reason)
		noteCacheTraceSelection(ctx, selection, decision)
		require.Equal(t, reason, trace.data.RouteReason)
	}
	require.Equal(t, "stall", trace.data.RebindCause, "higher route priority never erases the independent cause")
}

func TestCacheTraceRoutingSelectionMarkersResetButRequestFactsRemain(t *testing.T) {
	ctx, trace, st := traceRoutingContext()
	selection := &AccountSelectionResult{Account: &Account{ID: 9}}
	decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerLoadBalance}
	noteCacheTraceSoftOwner(ctx, 18)
	noteCacheTraceLastResort(ctx, 9)
	noteCacheTraceRebind(ctx, resilience.CategoryStall)
	NoteCacheTraceFailover(ctx, 2)
	noteCacheTraceSelection(ctx, selection, decision)
	require.Equal(t, "last_resort", trace.data.RouteReason)
	beginCacheTraceSelection(ctx)
	require.Zero(t, trace.routing.softOwner)
	require.Zero(t, trace.routing.lastResort)
	require.Equal(t, 2, trace.routing.failoverN)
	require.Equal(t, "stall", trace.data.RebindCause)
	noteCacheTraceSelection(ctx, selection, decision)
	require.Equal(t, "last_resort", trace.data.RouteReason)
	require.True(t, st.MarkFallback(7777777))
	noteCacheTraceFallback(ctx)
	NoteCacheTraceFailover(ctx, 3)
	beginCacheTraceSelection(ctx)
	noteCacheTraceSelection(ctx, selection, decision)
	require.Equal(t, "last_resort", trace.data.RouteReason)
	require.Equal(t, 3, trace.routing.failoverN)
}

func TestCacheTraceRoutingExactMarkersAndUnknownShare(t *testing.T) {
	for _, layer := range []string{openAIAccountScheduleLayerShare, "unclassified-quality", ""} {
		t.Run(layer, func(t *testing.T) {
			ctx, trace, st := traceRoutingContext()
			selection := &AccountSelectionResult{Account: &Account{ID: 9}}
			decision := OpenAIAccountScheduleDecision{Layer: layer, SelectedAccountID: 9}
			// These pre-existing RequestState values cannot stand in for a fresh
			// policy outcome. The observer deliberately never reads them.
			st.KeepSticky(8)
			st.SetLastResort(9)
			noteCacheTraceSelection(ctx, selection, decision)
			require.Equal(t, "unknown", trace.data.RouteReason)
			noteCacheTraceSoftOwner(ctx, 9)
			noteCacheTraceLastResort(ctx, 8)
			noteCacheTraceSelection(ctx, selection, decision)
			require.Equal(t, "unknown", trace.data.RouteReason, "same retained owner or different LR account is not a diversion/LR")
			decision.StickySessionHit = true
			noteCacheTraceSelection(ctx, selection, decision)
			require.Equal(t, "sticky_hit", trace.data.RouteReason)
			decision.StickyPreviousHit = true
			noteCacheTraceSelection(ctx, selection, decision)
			require.Equal(t, "prev_owner", trace.data.RouteReason)
			selection.StickyCapacitySpillover = true
			noteCacheTraceSelection(ctx, selection, decision)
			require.Equal(t, "overflow", trace.data.RouteReason)
		})
	}
}

func TestCacheTraceRoutingMissingTraceNoStateMutationOrAllocation(t *testing.T) {
	st := resilience.NewRequestState()
	st.KeepSticky(4)
	st.SetLastResort(5)
	require.True(t, st.MarkFallback(7777777))
	ctx := resilience.WithRequestState(context.Background(), st)
	selection := &AccountSelectionResult{Account: &Account{ID: 9}}
	invoke := func() {
		beginCacheTraceSelection(ctx)
		noteCacheTraceSoftOwner(ctx, 9)
		noteCacheTraceLastResort(ctx, 9)
		NoteCacheTraceFailover(ctx, 2)
		noteCacheTraceRebind(ctx, resilience.CategoryTransient)
		noteCacheTraceFallback(ctx)
		noteCacheTraceSelection(ctx, selection, OpenAIAccountScheduleDecision{StickySessionHit: true})
	}
	require.Zero(t, testing.AllocsPerRun(100, invoke))
	require.Equal(t, int64(4), st.KeptSticky())
	require.Equal(t, int64(5), st.LastResort())
	require.Equal(t, int64(7777777), st.OriginGroupID())
	require.False(t, st.Avoided(9))
	require.False(t, st.FirstOutputNoted())
	require.Nil(t, cacheTraceFrom(ctx))
}

func TestCacheTraceRoutingEnabledDisabledSchedulerEquivalent(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	type outcome struct {
		account, owner     int64
		acquired, released []int64
		layer              string
		spill              bool
	}
	for _, advanced := range []bool{false, true} {
		for _, soft := range []bool{false, true} {
			for _, batch := range []bool{false, true} {
				t.Run(fmt.Sprintf("advanced=%t/soft=%t/batch=%t", advanced, soft, batch), func(t *testing.T) {
					run := func(enabled bool) outcome {
						ctx, _, _ := traceRoutingContext()
						if !enabled {
							ctx = resilience.WithRequestState(context.Background(), resilience.NewRequestState())
						}
						group := int64(38100)
						accounts := newLegacySchedulerDecisionTestAccounts(group, true)
						if soft {
							until := time.Now().Add(time.Minute)
							accounts[1].TempUnschedulableUntil = &until
							accounts[1].TempUnschedulableReason = "transient:synthetic"
						}
						acquired, released := []int64{}, []int64{}
						svc := newLegacySchedulerDecisionTestService(accounts, batch, schedulerTestConcurrencyCache{acquiredIDs: &acquired, releasedIDs: &released})
						if advanced {
							svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true", "false")
						}
						resetOpenAIAdvancedSchedulerSettingCacheForTest()
						session := "v2:synthetic-observer-equivalence"
						require.NoError(t, svc.setStickySessionAccountID(ctx, &group, session, 38102, time.Hour))
						selection, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
						require.NoError(t, err)
						require.NotNil(t, selection)
						releaseLegacySchedulerDecisionSelection(selection)
						owner, err := svc.getStickySessionAccountID(ctx, &group, session)
						require.NoError(t, err)
						return outcome{selection.Account.ID, owner, acquired, released, decision.Layer, selection.StickyCapacitySpillover}
					}
					require.Equal(t, run(false), run(true), "trace presence cannot change account, binding, acquire/release order or scheduler decision")
				})
			}
		}
	}
}

// Exercise actual scheduling, then real Responses/Chat Forward (which calls
// resetCacheTraceAttempt), then finishCacheDiagnostic's immutable snapshot and
// settlement event. Only the upstream network response is synthetic.
func TestCacheTraceRoutingFinalSuccessfulForwardPreservesDecision(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	for _, protocol := range []string{"responses", "chat"} {
		for _, scenario := range []string{"failover", "fallback+failover", "rebind+soft_divert", "failover+last_resort"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				f := cacheTraceNewContractFixture(t, "t4-routing", protocol)
				st := resilience.NewRequestState()
				f.c.Request = f.c.Request.WithContext(resilience.WithRequestState(f.c.Request.Context(), st))
				session := f.s.GenerateSessionHash(f.c, f.body)
				ctx := f.c.Request.Context()
				trace := cacheTraceFromGin(f.c)
				require.NotNil(t, trace)
				group := int64(38100)
				accounts := newLegacySchedulerDecisionTestAccounts(group, true)
				wantRoute, wantCause := "failover_n", "none"
				switch scenario {
				case "fallback+failover":
					require.True(t, st.MarkFallback(7777777))
					wantRoute = "fallback_group"
				case "rebind+soft_divert":
					until := time.Now().Add(time.Minute)
					accounts[1].TempUnschedulableUntil = &until
					accounts[1].TempUnschedulableReason = "transient:synthetic"
					rec := &sessionFailureRecorder{r: NewResilienceService(nil, newMemResilienceStore())}
					// Rebind belongs to an earlier failed account; current retained sticky
					// owner 38102 remains eligible for soft-owner retention.
					rec.Failure(ctx, group, session, 999, resilience.CategoryStall)
					rec.Failure(ctx, group, session, 999, resilience.CategoryStall)
					wantRoute, wantCause = "session_rebind", "stall"
				case "failover+last_resort":
					until := time.Now().Add(time.Minute)
					accounts = accounts[:1]
					accounts[0].RateLimitResetAt = &until
					wantRoute = "last_resort"
				}
				svc := newLegacySchedulerDecisionTestService(accounts, true, schedulerTestConcurrencyCache{})
				svc.resilience = NewResilienceService(nil, newMemResilienceStore())
				if scenario == "failover+last_resort" {
					svc.accountRepo = openAIGroupListingRepo{schedulerTestOpenAIAccountRepo{accounts: accounts}}
				}
				if scenario == "rebind+soft_divert" {
					require.NoError(t, svc.setStickySessionAccountID(ctx, &group, session, 38102, time.Hour))
				}
				if scenario != "rebind+soft_divert" {
					NoteCacheTraceFailover(ctx, 1)
				}
				selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", session, "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true)
				require.NoError(t, err)
				require.NotNil(t, selection)
				releaseLegacySchedulerDecisionSelection(selection)
				require.Equal(t, wantRoute, trace.data.RouteReason)
				require.Equal(t, wantCause, trace.data.RebindCause)
				if scenario == "rebind+soft_divert" {
					require.Equal(t, int64(38102), trace.routing.softOwner)
					require.Equal(t, int64(38101), selection.Account.ID)
				}
				if scenario == "failover+last_resort" {
					require.Equal(t, selection.Account.ID, trace.routing.lastResort)
				}
				f.account.ID = selection.Account.ID
				result := f.forward(t, protocol, false)
				require.NotNil(t, result.CacheDiagnostic)
				require.NotNil(t, result.CacheTrace)
				require.Equal(t, wantRoute, result.CacheTrace.data.RouteReason)
				require.Equal(t, wantCause, result.CacheTrace.data.RebindCause)
				require.Equal(t, selection.Account.ID, result.CacheTrace.data.AccountID)
				awaitCacheTrace(t, f.s, f.c, result)
				t3SettlementWorkerDrained(t, f.s)
				logs := t3SettlementCapture(t)
				f.s.settleCacheTrace(f.c.Request.Context(), result, f.account)
				events := t3SettlementEvents(t, logs, "cache_trace")
				require.Len(t, events, 1)
				require.Equal(t, wantRoute, events[0]["route_reason"])
				require.Equal(t, wantCause, events[0]["rebind_cause"])
				for _, secret := range []string{"ctc-private", "7777777", "synthetic-token"} {
					require.NotContains(t, logs.String(), secret)
				}
			})
		}
	}
}

func TestCacheTraceRoutingProductionShareKnownAndUnknown(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	withResilienceSettings(t, nil)
	t.Run("new_session_then_affinity_hit", func(t *testing.T) {
		ctx, trace, _ := traceRoutingContext()
		env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
		first, decision := env.selectAccount(t, ctx, "", "t4-share-new", nil)
		releaseLegacySchedulerDecisionSelection(first)
		require.Equal(t, openAIAccountScheduleLayerShare, decision.Layer)
		require.False(t, decision.StickySessionHit)
		require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+"new_session"))
		require.Equal(t, "unknown", trace.data.RouteReason)
		second, decision := env.selectAccount(t, ctx, "", "t4-share-new", nil)
		releaseLegacySchedulerDecisionSelection(second)
		require.Equal(t, first.Account.ID, second.Account.ID)
		require.True(t, decision.StickySessionHit)
		require.Equal(t, "sticky_hit", trace.data.RouteReason)
	})
	t.Run("spill_rebind_has_no_legacy_overflow_flag", func(t *testing.T) {
		ctx, trace, _ := traceRoutingContext()
		env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
		target, other := openAIShareTarget(t, "t4-share-busy")
		env.cache.bind("openai:t4-share-busy", target, time.Hour-30*time.Second)
		env.conc.busy[target] = -1
		selection, decision := env.selectAccount(t, withShareRequestProfile(ctx, make([]byte, 400)), "", "t4-share-busy", nil)
		releaseLegacySchedulerDecisionSelection(selection)
		require.Equal(t, other, selection.Account.ID)
		require.False(t, decision.StickySessionHit)
		require.False(t, selection.StickyCapacitySpillover)
		require.Equal(t, int64(1), env.counter(shareStatsReasonPrefix+"spill_rebind"))
		require.Equal(t, "unknown", trace.data.RouteReason)
	})
}
