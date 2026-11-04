//go:build unit

package service

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/sharesched"
	"github.com/stretchr/testify/require"
)

func TestGateV3ShareSameSiteHoldOverridesSoftSpill(t *testing.T) {
	for _, tc := range []struct {
		name, penalty     string
		missing, disabled bool
	}{
		{name: "hold only"}, {name: "short account limit", penalty: "rate"},
		{name: "model limit early return", penalty: "model"},
		{name: "transient soft penalty", penalty: "transient"},
		{name: "longer soft penalty has another reason", penalty: "longer"},
		{name: "missing owner with short limit", penalty: "rate", missing: true},
		{name: "missing owner with model limit", penalty: "model", missing: true},
		{name: "disabled retains ordinary soft spill", penalty: "rate", disabled: true},
		{name: "disabled hold alone retains healthy owner", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, sharesched.SchedulerShare, 1, 2)
			const session = "same-site-share-owner"
			owner, other := env.account(1), env.account(2)
			owner.Credentials = map[string]any{"base_url": "https://site-a.example/v1"}
			other.Credentials = map[string]any{"base_url": "https://site-b.example/v1"}
			now := time.Now()
			until := now.Add(time.Minute)
			switch tc.penalty {
			case "rate":
				owner.RateLimitResetAt = &until
			case "model":
				owner.Extra = map[string]any{"model_rate_limits": map[string]any{"gpt-5.1": map[string]any{"rate_limit_reset_at": until.Format(time.RFC3339)}}}
			case "transient", "longer":
				if tc.penalty == "longer" {
					until = now.Add(8 * time.Minute)
				}
				owner.TempUnschedulableUntil = &until
				owner.TempUnschedulableReason = "transient:TRANSIENT HTTP 502"
			}
			peer := *owner
			peer.ID = 999
			env.svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
			env.svc.resilience.noteOpenAISameSiteStreamFailure(owner, now)
			tier, _, why := env.svc.resilienceOpenAIPenaltyOf(owner, "gpt-5.1", now)
			require.Equal(t, resilience.TierSoft, tier, "global penalty remains eligible for last resort")
			if tc.penalty == "longer" {
				require.NotEqual(t, "same_site_hold", why)
			}
			var snapshot *openAISnapshotCacheStub
			if tc.missing {
				snapshot = &openAISnapshotCacheStub{snapshotAccounts: []*Account{other}, accountsByID: map[int64]*Account{1: owner, 2: other}}
				env.svc.schedulerSnapshot = &SchedulerSnapshotService{cache: snapshot}
			}
			if tc.disabled {
				settings := resilience.Current().Clone()
				settings.SameSiteHold.Enabled = false
				resilience.Publish(settings)
			}
			env.cache.bind("openai:"+session, 1, time.Hour-30*time.Second)
			ctx := cacheableContext()
			result, _ := env.selectAccount(t, ctx, "", session, nil)
			wantServing := int64(2)
			if tc.disabled && tc.penalty == "" {
				wantServing = 1
			}
			require.Equal(t, wantServing, result.Account.ID)
			group := shareTestGroupID
			require.NoError(t, env.svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, result), &group, session, result.Account.ID))
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			wantOwner := int64(2)
			if tc.disabled {
				wantOwner = 1
			}
			require.Equal(t, wantOwner, env.cache.sessionBindings["openai:"+session])
			if !tc.disabled {
				require.Zero(t, env.counter(shareStatsReasonPrefix+string(sharesched.ReasonSoftSpill)))
			}
			owner.RateLimitResetAt = nil
			owner.TempUnschedulableUntil = nil
			owner.Extra = nil
			tracker := &env.svc.resilience.sameSiteHold
			tracker.mu.Lock()
			entry := tracker.accounts[owner.ID]
			entry.until = now.Add(-time.Second)
			tracker.accounts[owner.ID] = entry
			tracker.mu.Unlock()
			if snapshot != nil {
				snapshot.snapshotAccounts = []*Account{owner, other}
			}
			next, _ := env.selectAccount(t, ctx, "", session, nil)
			require.Equal(t, wantOwner, next.Account.ID, "expiry must not return a rebound session to its old owner")
			if next.ReleaseFunc != nil {
				next.ReleaseFunc()
			}
		})
	}
	for _, overlap := range []bool{false, true} {
		t.Run("single held account remains last resort/soft="+fmtBool(overlap), func(t *testing.T) {
			env := newOpenAIRuntimeBlockEnv(t, sharesched.SchedulerShare, lrAccountA)
			a := env.account(lrAccountA)
			if overlap {
				until := time.Now().Add(time.Minute)
				a.RateLimitResetAt = &until
			}
			peer := *a
			peer.ID = 999
			now := time.Now()
			env.svc.resilience.noteOpenAISameSiteStreamFailure(&peer, now.Add(-time.Second))
			env.svc.resilience.noteOpenAISameSiteStreamFailure(a, now)
			result := selectOpenAILastResort(t, env)
			require.Equal(t, a.ID, result.Account.ID)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}

func TestGateV3ShareLatencyOrdering(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		ownerTier, otherTier int
		both, bind, disabled bool
		model                string
	}{
		{name: "same tier healthy first"},
		{name: "held tier zero before tier one", otherTier: 1},
		{name: "held tier zero before tier two", otherTier: 2},
		{name: "all held remain eligible", both: true},
		{name: "valid sticky owner remains", bind: true},
		{name: "another model does not defer", model: "other-model"},
		{name: "disabled gate preserves share order", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withResilienceSettings(t, nil)
			const session = "quality-held-placement"
			first, other := openAIShareTarget(t, session)
			env := newOpenAIShareTestEnv(t, twoAccountPolicy(), 1, 2)
			env.svc.SetGroupQualityGateView(&schedulingQualityView{enabled: !tc.disabled, tiers: map[int64]int{first: tc.ownerTier, other: tc.otherTier}})
			model := tc.model
			if model == "" {
				model = "gpt-5.1"
			}
			now := time.Now()
			for n := 0; n < 6; n++ {
				env.svc.getOpenAIAccountLatencyHoldState().record(first, model, 11*time.Second, now)
				if tc.both {
					env.svc.getOpenAIAccountLatencyHoldState().record(other, model, 11*time.Second, now)
				}
			}
			if tc.bind {
				env.cache.bind("openai:"+session, first, time.Hour-30*time.Second)
			}
			result, _ := env.selectAccount(t, context.Background(), "", session, nil)
			want := other
			if tc.otherTier > tc.ownerTier || tc.both || tc.bind || tc.disabled || tc.model != "" {
				want = first
			}
			require.Equal(t, want, result.Account.ID)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
	t.Run("spill candidates also defer held accounts", func(t *testing.T) {
		withResilienceSettings(t, nil)
		svc := &OpenAIGatewayService{}
		svc.SetGroupQualityGateView(&schedulingQualityView{enabled: true})
		a, b, c := gateV3Account(1, 0), gateV3Account(2, 0), gateV3Account(3, 0)
		gateV3Hold(svc, 2, time.Now())
		group := int64(7)
		plan := sharesched.Decision{Bound: 1, Steps: []sharesched.Step{{AccountID: 1}, {AccountID: 2}, {AccountID: 3}}}
		plan = svc.orderOpenAIShareQuality(&group, "gpt-test", map[int64]*Account{1: &a, 2: &b, 3: &c}, plan)
		require.Equal(t, []sharesched.Step{{AccountID: 1}, {AccountID: 3}, {AccountID: 2}}, plan.Steps)
	})
}

func TestGateV3PredictionOrderTransitivityAndPermutations(t *testing.T) {
	a, b, c := gateV3Account(1, 0), gateV3Account(2, 0), gateV3Account(3, 0)
	source := []accountWithLoad{{&a, &AccountLoadInfo{LoadRate: 30}}, {&b, &AccountLoadInfo{LoadRate: 20}}, {&c, &AccountLoadInfo{LoadRate: 10}}}
	order := openAICandidateOrder{rates: openAILegacyUpstreamRateOrder{enabled: true, rates: map[int64]float64{1: 1, 2: 1, 3: 1}}, expectedP90: map[int64]int{1: 1000, 3: 3000}}
	less := func(i, j int) bool { return openAICandidateLess(source[i], source[j], order) }
	require.False(t, less(0, 2) && less(2, 1) && less(1, 0), "missing predictions cannot form a comparison cycle")
	for _, permutation := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		items := []accountWithLoad{source[permutation[0]], source[permutation[1]], source[permutation[2]]}
		sort.SliceStable(items, func(i, j int) bool { return openAICandidateLess(items[i], items[j], order) })
		require.Equal(t, int64(2), items[1].account.ID, "unknown's base position is determined by load")
		orderOpenAIPredictedPositions(items, func(item accountWithLoad) accountWithLoad { return item }, order, nil, false)
		require.Equal(t, []int64{1, 2, 3}, []int64{items[0].account.ID, items[1].account.ID, items[2].account.ID})
	}
	for _, boundary := range []string{"tier", "held", "priority", "near capacity", "rate", "unknown rate", "compact"} {
		t.Run(boundary, func(t *testing.T) {
			a, b := gateV3Account(1, 0), gateV3Account(2, 0)
			items := []accountWithLoad{{&a, &AccountLoadInfo{LoadRate: 10}}, {&b, &AccountLoadInfo{LoadRate: 20}}}
			order := openAICandidateOrder{rates: openAILegacyUpstreamRateOrder{enabled: true, rates: map[int64]float64{1: 1, 2: 1}}, tiers: map[int64]int{}, expectedP90: map[int64]int{1: 3000, 2: 1000}}
			held := map[int64]openAIAccountLatencyHoldSnapshot{}
			switch boundary {
			case "tier":
				order.tiers[2] = 1
			case "held":
				held[2] = openAIAccountLatencyHoldSnapshot{Held: true}
			case "priority":
				b.Priority = 1
			case "near capacity":
				items[1].loadInfo.LoadRate = 90
			case "rate":
				order.rates.rates[2] = 2
			case "unknown rate":
				delete(order.rates.rates, 2)
			case "compact":
				a.Extra = map[string]any{"openai_compact_supported": true}
			}
			orderOpenAIPredictedPositions(items, func(item accountWithLoad) accountWithLoad { return item }, order, held, boundary == "compact")
			require.Equal(t, int64(1), items[0].account.ID, "predictions cannot cross an ordering cohort")
		})
	}
}

func TestGateV3SameRateSelectionUsesPrediction(t *testing.T) {
	a, b := gateV3Account(1, 0), gateV3Account(2, 0)
	a.RateMultiplier, b.RateMultiplier = floatPtr(1), floatPtr(1)
	svc := &OpenAIGatewayService{}
	svc.SetGroupQualityGateView(&schedulingQualityView{enabled: true, predicted: map[int64]int{1: 6000, 2: 1000}})
	svc.accountRepo = schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo{accounts: []Account{a, b}}}
	group := int64(7)
	selected, _, _ := svc.selectBestAccount(context.Background(), &group, PlatformOpenAI, []Account{a, b}, "gpt-test", nil, false, "", true)
	require.NotNil(t, selected)
	require.Equal(t, b.ID, selected.ID)
}

func TestGateV3LatencyHoldStartedWarnFields(t *testing.T) {
	withResilienceSettings(t, nil)
	sink, restore := captureStructuredLog(t)
	defer restore()
	svc := &OpenAIGatewayService{}
	a := gateV3Account(1, 0)
	now := time.Now()
	for round := 0; round < 2; round++ {
		for n := 0; n < 6; n++ {
			svc.recordOpenAIAccountLatencySample(&a, "gpt-test", 11*time.Second, now)
		}
		now = now.Add(3 * time.Minute)
	}
	const event = "openai_account_latency_hold_started"
	require.True(t, sink.ContainsMessageAtLevel(event, "warn"))
	fields := gateV3LogFields(sink, event)
	require.Len(t, fields, 2)
	require.EqualValues(t, 180, fields[0]["hold_seconds"])
	require.EqualValues(t, 1, fields[0]["level"])
	require.EqualValues(t, 360, fields[1]["hold_seconds"])
	require.EqualValues(t, 2, fields[1]["level"])
}
