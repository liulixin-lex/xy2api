//go:build unit

package service

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

func TestRatefloorVerdictOrder(t *testing.T) {
	ceiling := 0.8
	for _, tc := range []struct {
		name          string
		rate          float64
		valid, pinned bool
		floor         float64
		want          string
	}{
		{"pinned low", 0.1, true, true, 0.4, "pinned"}, {"pinned high", 2, true, true, 0.4, "pinned"},
		{"pinned invalid", math.NaN(), false, true, 0.4, "pinned"}, {"invalid before floor", 0, false, false, 0.4, openAIProfitFilterReasonInvalidAccountRate},
		{"floor", 0.1, true, false, 0.4, openAIProfitFilterReasonFloor}, {"ceiling", 1, true, false, 0.4, openAIProfitFilterReasonThreshold},
		{"empty range", 0.9, true, false, 1, openAIProfitFilterReasonFloor}, {"floor boundary", 0.4 - 1e-10, true, false, 0.4, "pass"},
		{"ceiling boundary", 0.8 + 1e-10, true, false, 0.4, "pass"}, {"floor outside epsilon", 0.4 - 2e-9, true, false, 0.4, openAIProfitFilterReasonFloor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, profitRangeVerdict(tc.rate, tc.valid, &ceiling, tc.floor, tc.pinned))
		})
	}
	group := profitControlTestGroup(7, 0.2, 0)
	group.ProfitControlEnabled = false
	group.MinUpstreamRate = floatPtr(0.4)
	svc := &OpenAIGatewayService{}
	ctx, _ := svc.WithOpenAIRequestPricingContext(profitControlTestCtx(group), &group.ID)
	gate := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	require.False(t, gate.ceilingOn)
	a := profitControlWSAccount(1, 0.2, time.Now())
	veto, reason := openAIProfitControlVetoReason(ctx, &a)
	require.True(t, veto)
	require.Equal(t, openAIProfitFilterReasonFloor, reason)
	a.RateMultiplier = floatPtr(2)
	a.Extra = nil
	veto, _ = openAIProfitControlVetoReason(ctx, &a)
	require.False(t, veto)
	group.Platform = PlatformGrok
	require.Nil(t, svc.resolveOpenAIProfitControlGate(profitControlTestCtx(group), &group.ID))
}

func TestRatefloorPreviewParity(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(map[bool]string{false: "floor only", true: "range"}[on], func(t *testing.T) {
			group := profitControlTestGroup(10, 0.2, 0)
			group.ProfitControlEnabled = on
			group.MinUpstreamRate = floatPtr(0.4)
			rows := []Account{}
			accounts := []*Account{}
			for i, rate := range []float64{0.1, 0.5, 1, 0.1, 1, -1} {
				a := healthyAccount(int64(i + 1))
				a.Type = AccountTypeAPIKey
				a.RateMultiplier = floatPtr(rate)
				a.AccountGroups = []AccountGroup{{AccountID: a.ID, GroupID: 10, Pinned: i == 3 || i == 4}}
				rows = append(rows, a)
			}
			usable := 0
			for i := range rows {
				accounts = append(accounts, &rows[i])
				if groupMemberUsable(group, &rows[i], time.Now()) {
					usable++
				}
			}
			report := PreviewProfitAdmission([]ProfitPreviewGroupInput{{Group: group, Accounts: accounts}}, time.Now())[0]
			admitted := 0
			for _, v := range report.Verdicts {
				if v.Class == ProfitPreviewClassAdmitted {
					admitted++
				}
			}
			svc := NewProfitPreviewService(&groupAccountsRepo{byGroup: map[int64][]Account{10: rows}}, &fallbackGroupRepo{groups: map[int64]*Group{10: group}})
			preview, err := svc.ProfitControlPreview(context.Background(), 10, nil, group.MinUpstreamRate)
			require.NoError(t, err)
			require.Equal(t, usable, admitted)
			require.Equal(t, usable, preview.Passing)
			require.Equal(t, 1, preview.BelowFloor)
			require.Equal(t, 2, preview.Pinned)
			if on {
				require.Len(t, preview.PinnedLoss, 1)
				require.Equal(t, int64(5), preview.PinnedLoss[0].AccountID)
			} else {
				require.Empty(t, preview.PinnedLoss)
			}
			high := 1.2
			preview, err = svc.ProfitControlPreview(context.Background(), 10, nil, &high)
			require.NoError(t, err)
			require.Equal(t, on, preview.RangeEmpty)
		})
	}
}

func TestRatefloorStickyReleaseAndCeilingPreserve(t *testing.T) {
	for _, path := range []string{"load-aware", "sticky-hit"} {
		for _, floor := range []bool{true, false} {
			t.Run(path+map[bool]string{true: "/floor", false: "/ceiling"}[floor], func(t *testing.T) {
				gid := int64(7)
				owner := selectionFixtureAccount(1, 1)
				owner.RateMultiplier = floatPtr(0.1)
				if !floor {
					owner.RateMultiplier = floatPtr(0.9)
				}
				cached := owner
				cached.RateMultiplier = floatPtr(0.5)
				replacement := selectionFixtureAccount(2, 2)
				replacement.RateMultiplier = floatPtr(0.6)
				repo := schedulerTestOpenAIAccountRepo{accounts: []Account{owner, replacement}}
				cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:range-sticky": 1}}
				group := profitControlTestGroup(gid, 0.2, 0)
				group.MinUpstreamRate = floatPtr(0.4)
				snapshot := NewSchedulerSnapshotService(&openAISnapshotCacheStub{snapshotAccounts: []*Account{&cached, &replacement}, accountsByID: map[int64]*Account{1: &cached, 2: &replacement}}, nil, repo, &fallbackGroupRepo{groups: map[int64]*Group{gid: group}}, nil)
				svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, schedulerSnapshot: snapshot, rateLimitService: newOpenAISelectionRateLimitService(), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
				st := resilience.NewRequestState()
				ctx := resilience.WithRequestState(profitControlTestCtx(group), st)
				ctx, _ = svc.WithOpenAIRequestPricingContext(ctx, &gid)
				if path == "load-aware" {
					selected, err := svc.SelectAccountWithLoadAwareness(ctx, &gid, "range-sticky", "m", nil)
					require.NoError(t, err)
					require.NotNil(t, selected)
					require.EqualValues(t, 2, selected.Account.ID)
					if selected.ReleaseFunc != nil {
						selected.ReleaseFunc()
					}
				} else {
					require.Nil(t, svc.tryStickySessionHit(ctx, &gid, PlatformOpenAI, "range-sticky", "m", nil, false, 1, ""))
				}
				want := int64(1)
				reason := openAIProfitFilterReasonThreshold
				if floor {
					want = 0
					reason = openAIProfitFilterReasonFloor
				}
				require.Equal(t, want, cache.sessionBindings["openai:range-sticky"])
				require.Equal(t, reason, st.StickyBypassReason(), "must reach the database profit recheck, not owner_group_mismatch")
			})
		}
	}
}

func TestRatefloorBasisUsesSelectedAccount(t *testing.T) {
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	ctx := resilience.WithRequestState(context.Background(), st)
	gate := &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, floor: 0.4, sell: 1.5}
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, gate)
	for id := int64(1); id <= 10; id++ {
		a := Account{ID: id, RateMultiplier: floatPtr(0.1)}
		openAIProfitControlVetoReason(ctx, &a)
	}
	selected := Account{ID: 20, RateMultiplier: floatPtr(0.5)}
	other := Account{ID: 21, RateMultiplier: floatPtr(0.6)}
	openAIProfitControlVetoReason(ctx, &selected)
	openAIProfitControlVetoReason(ctx, &other)
	st.NoteGatewaySend(selected.ID)
	meta, _ := st.ClaimGatewayRequest(false)
	require.Equal(t, int64(20), meta.ProfitBasis.Admitted.AccountID)
	require.Len(t, meta.ProfitBasis.Vetoes, 8)
	require.Equal(t, int64(3), meta.ProfitBasis.Vetoes[0].AccountID)
	row := &UsageLog{}
	applyGatewayRequestMetadata(resilience.WithGatewayRequestMetadata(ctx, meta), row)
	require.JSONEq(t, string(marshalProfitBasis(meta.ProfitBasis)), string(row.ProfitBasis))
	*meta.ProfitBasis.Admitted.Upstream = 99
	again, _ := st.ClaimGatewayRequest(false)
	require.Equal(t, 0.5, *again.ProfitBasis.Admitted.Upstream)
	st.BeginGatewayTurn(time.Now())
	next, _ := st.ClaimGatewayRequest(false)
	require.Nil(t, next.ProfitBasis)
}

func TestRatefloorQueuedOwnerUsesSuccessfulReplacement(t *testing.T) {
	for _, floor := range []bool{true, false} {
		t.Run(map[bool]string{true: "floor", false: "ceiling"}[floor], func(t *testing.T) {
			owner := selectionFixtureAccount(1, 1)
			owner.RateMultiplier = floatPtr(0.5)
			other := selectionFixtureAccount(2, 2)
			other.RateMultiplier = floatPtr(0.6)
			f := newSelectionRuntimeFixture(t, owner, other)
			f.cache.sessionBindings["openai:range-queued"] = 1
			gid := int64(7)
			st := resilience.NewRequestState()
			ctx := resilience.WithRequestState(context.Background(), st)
			gate := &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, ceilingOn: true, threshold: 0.8, floor: 0.4, pricingAt: time.Now()}
			ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, gate)
			req := OpenAIAccountScheduleRequest{GroupID: &gid, Platform: PlatformOpenAI, SessionHash: "range-queued", RequestedModel: "m"}
			ctx = context.WithValue(ctx, openAISelectionRequestKey{}, req)
			f.svc.prepareOpenAIStickyChange(ctx, &gid, req.SessionHash)
			require.NotNil(t, pendingOpenAIStickyChange(ctx))
			queued := &AccountSelectionResult{Account: &owner, WaitPlan: &AccountWaitPlan{AccountID: 1, MaxConcurrency: 10, Timeout: time.Second}}
			slotGranted := make(chan struct{})
			result := make(chan string)
			go func() {
				<-slotGranted
				_, veto, reason := f.svc.SelectionAdmissionVetoLatest(ctx, queued.Account)
				if !veto {
					reason = "admitted"
				}
				result <- reason
			}()
			rate := 0.1
			if !floor {
				rate = 0.9
			}
			f.repo.accounts[0].RateMultiplier = &rate
			close(slotGranted)
			expected := openAIProfitFilterReasonFloor
			if !floor {
				expected = openAIProfitFilterReasonThreshold
			}
			require.Equal(t, expected, <-result)
			require.Equal(t, floor, pendingOpenAIStickyChange(ctx).ownerReleased)
			replacement := &AccountSelectionResult{Account: &other}
			planOpenAIStickyReplacement(ctx, &gid, req.SessionHash, "m", replacement)
			require.EqualValues(t, 1, f.cache.sessionBindings["openai:range-queued"], "replacement must not commit before successful output")
			CompleteOpenAIStickyChange(ctx, 2, true, false)
			want := int64(1)
			if floor {
				want = 2
			}
			require.Equal(t, want, f.cache.sessionBindings["openai:range-queued"])
		})
	}
}

func TestRatefloorConfigValidationAndClear(t *testing.T) {
	for _, rate := range []float64{math.NaN(), math.Inf(1), -1, 0.12345, 0.12340000004, 0.00001, 1000000} {
		require.Error(t, ValidateMinUpstreamRate(PlatformOpenAI, &rate))
	}
	for _, platform := range []string{PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformComposite} {
		require.Error(t, ValidateMinUpstreamRate(platform, floatPtr(0.4)))
	}
	for _, v := range []float64{0, 0.0001, 0.0071, 0.1234, 999999.9999} {
		require.NoError(t, ValidateMinUpstreamRate(PlatformOpenAI, floatPtr(v)))
	}
	repo := &groupRepoStubForAdmin{getByID: &Group{ID: 7, Platform: PlatformOpenAI, MinUpstreamRate: floatPtr(0.4)}}
	svc := &adminServiceImpl{groupRepo: repo}
	group, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{})
	require.NoError(t, err)
	require.Equal(t, 0.4, *group.MinUpstreamRate)
	group, err = svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{MinUpstreamRate: floatPtr(0)})
	require.NoError(t, err)
	require.Nil(t, group.MinUpstreamRate)
}

func TestRatefloorQueuedUnpinDoesNotRestoreStaleCache(t *testing.T) {
	gid := int64(7)
	owner := profitControlWSAccount(1, 0.1, time.Now())
	owner.Extra = nil
	owner.GroupIDs = []int64{gid}
	owner.AccountGroups = []AccountGroup{{AccountID: 1, GroupID: gid, Pinned: true}}
	owner.UpdatedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cached := owner
	cached.AccountGroups = append([]AccountGroup(nil), owner.AccountGroups...)
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{owner}}
	bindings := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:queued-unpin": 1}}
	svc := &OpenAIGatewayService{accountRepo: repo, cache: bindings, schedulerSnapshot: NewSchedulerSnapshotService(&openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: &cached}}, nil, repo, nil, nil)}
	group := profitControlTestGroup(gid, 0.2, 0)
	group.MinUpstreamRate = floatPtr(0.4)
	ctx := profitControlTestCtx(group)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, floor: 0.4, pricingAt: time.Now()})
	ctx = context.WithValue(ctx, openAISelectionRequestKey{}, OpenAIAccountScheduleRequest{GroupID: &gid, Platform: PlatformOpenAI, SessionHash: "queued-unpin", RequestedModel: "m"})
	granted := make(chan struct{})
	result := make(chan string)
	go func() { <-granted; _, _, reason := svc.SelectionAdmissionVetoLatest(ctx, &cached); result <- reason }()
	repo.accounts[0].AccountGroups[0].Pinned = false
	close(granted)
	require.Equal(t, openAIProfitFilterReasonFloor, <-result)
	require.True(t, cached.PinnedIn(gid), "outbox has not refreshed the cached projection yet")
	require.Zero(t, bindings.sessionBindings["openai:queued-unpin"])
}

func TestRatefloorSoftOwnerCanCommitReplacement(t *testing.T) {
	owner := selectionFixtureAccount(1, 1)
	owner.RateMultiplier = floatPtr(0.1)
	until := time.Now().Add(time.Minute)
	owner.TempUnschedulableUntil = &until
	owner.TempUnschedulableReason = "transient:synthetic"
	replacement := selectionFixtureAccount(2, 2)
	replacement.RateMultiplier = floatPtr(0.6)
	f := newSelectionRuntimeFixture(t, owner, replacement)
	f.cache.sessionBindings["openai:floor-soft"] = 1
	gid := int64(7)
	st := resilience.NewRequestState()
	ctx := resilience.WithRequestState(context.Background(), st)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, floor: 0.4, pricingAt: time.Now()})
	selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &gid, "", "floor-soft", "m", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
	require.NoError(t, err)
	require.EqualValues(t, 2, selected.Account.ID)
	defer selected.ReleaseFunc()
	require.True(t, pendingOpenAIStickyChange(ctx).ownerReleased)
	require.EqualValues(t, 1, f.cache.sessionBindings["openai:floor-soft"])
	CompleteOpenAIStickyChange(ctx, 2, true, false)
	require.EqualValues(t, 2, f.cache.sessionBindings["openai:floor-soft"])
}

func TestRatefloorReviewPreviewZeroAndBoundary(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformAntigravity} {
		t.Run(platform, func(t *testing.T) {
			group := &Group{ID: 10, Platform: platform, RateMultiplier: 1, ProfitControlEnabled: true, ProfitMinMargin: 0.2}
			account := healthyAccount(1)
			account.Platform = platform
			account.Type = AccountTypeAPIKey
			account.RateMultiplier = floatPtr(0.5)
			svc := NewProfitPreviewService(&groupAccountsRepo{byGroup: map[int64][]Account{10: {account}}}, &fallbackGroupRepo{groups: map[int64]*Group{10: group}})
			preview, err := svc.ProfitControlPreview(context.Background(), 10, floatPtr(0.2), floatPtr(0))
			require.NoError(t, err)
			require.Equal(t, 0.0, preview.Floor)
			require.InDelta(t, 0.8, preview.Ceiling, 1e-12)
			require.Equal(t, 1, preview.Passing)
			_, err = svc.ProfitControlPreview(context.Background(), 10, nil, floatPtr(0.4))
			require.Error(t, err)
		})
	}
	group := profitControlTestGroup(10, 0.2, 0)
	group.RateMultiplier = 0.7
	svc := NewProfitPreviewService(&groupAccountsRepo{byGroup: map[int64][]Account{}}, &fallbackGroupRepo{groups: map[int64]*Group{10: group}})
	for _, tc := range []struct {
		floor float64
		empty bool
	}{{0.56, false}, {0.5601, true}} {
		preview, err := svc.ProfitControlPreview(context.Background(), 10, nil, &tc.floor)
		require.NoError(t, err)
		require.Equal(t, tc.empty, preview.RangeEmpty)
	}
}

func TestRatefloorReviewInvalidFreshProbe(t *testing.T) {
	at := time.Now()
	for _, value := range []any{-1.0, "NaN", "+Inf", "invalid-timezone"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			account := profitPreviewProbeAccount(1, UpstreamBillingProbeStatusOK, 0.5, at.Add(-time.Minute), time.Minute)
			account.RateMultiplier = floatPtr(0.5)
			data := account.Extra[UpstreamBillingProbeExtraKey].(map[string]any)["data"].(map[string]any)
			if value == "invalid-timezone" {
				data["peak_rate_enabled"] = true
				data["peak_start"] = "00:00"
				data["peak_end"] = "23:59"
				data["peak_rate_multiplier"] = 2.0
				data["timezone"] = "Invalid/FixtureZone"
			} else {
				data["resolved_rate_multiplier"] = value
			}
			rate, source, valid := openAIProfitGateCostSource(account, at)
			require.True(t, valid, "unusable probe falls back to stored rate")
			require.Equal(t, "stored", source)
			require.Equal(t, 0.5, rate)
			require.Equal(t, "pass", profitRangeVerdict(rate, valid, nil, 0.4, false))
			require.Equal(t, openAIProfitFilterReasonFloor, profitRangeVerdict(rate, valid, nil, 0.6, false))
			rate, source, valid = openAIProfitGateCostSource(account, at.Add(time.Hour))
			require.True(t, valid)
			require.Equal(t, "stored", source)
			require.Equal(t, 0.5, rate)
		})
	}
}

func TestRatefloorPrecheckDoesNotDuplicateVeto(t *testing.T) {
	gid := int64(7)
	account := selectionFixtureAccount(1, 1)
	account.RateMultiplier = floatPtr(0.9)
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:range-count": 1}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}, cache: cache}
	st := resilience.NewRequestState()
	st.EnableGatewayRequest(time.Now())
	ctx := resilience.WithRequestState(context.Background(), st)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, ceilingOn: true, threshold: 0.8, floor: 0.4})
	require.Nil(t, svc.tryStickySessionHit(ctx, &gid, PlatformOpenAI, "range-count", "m", nil, false, 1, ""))
	meta, _ := st.ClaimGatewayRequest(false)
	require.Len(t, meta.ProfitBasis.Vetoes, 1)
	require.Equal(t, openAIProfitFilterReasonThreshold, meta.ProfitBasis.Vetoes[0].Verdict)
}

func TestRatefloorAdmittedBasisFreezesAtSend(t *testing.T) {
	for _, sentAgain := range []bool{false, true} {
		t.Run(fmt.Sprint(sentAgain), func(t *testing.T) {
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			ctx := resilience.WithRequestState(context.Background(), st)
			ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 7, platform: PlatformOpenAI, floor: 0.4, sell: 1, pricingAt: time.Now()})
			a := selectionFixtureAccount(1, 1)
			a.RateMultiplier = floatPtr(0.5)
			veto, _ := openAIProfitControlVetoReason(ctx, &a)
			require.False(t, veto)
			st.NoteGatewaySend(a.ID)
			a.RateMultiplier = floatPtr(0.6)
			veto, _ = openAIProfitControlVetoReason(ctx, &a)
			require.False(t, veto)
			if sentAgain {
				st.NoteGatewaySend(a.ID)
			}
			st.CompleteGatewayForward(false, true)
			meta, _ := st.ClaimGatewayRequest(false)
			require.NotNil(t, meta.ProfitBasis)
			require.NotNil(t, meta.ProfitBasis.Admitted)
			want := 0.5
			if sentAgain {
				want = 0.6
			}
			require.Equal(t, want, *meta.ProfitBasis.Admitted.Upstream, "an unsent same-account candidate must not replace the actual send basis")
		})
	}
}

func TestRatefloorEarlyReleaseRecordsBasis(t *testing.T) {
	for _, excluded := range []bool{false, true} {
		t.Run(map[bool]string{false: "soft", true: "excluded"}[excluded], func(t *testing.T) {
			owner := selectionFixtureAccount(1, 1)
			owner.RateMultiplier = floatPtr(0.1)
			until := time.Now().Add(time.Minute)
			var exclude map[int64]struct{}
			if excluded {
				exclude = map[int64]struct{}{1: {}}
			} else {
				owner.TempUnschedulableUntil = &until
				owner.TempUnschedulableReason = "transient:synthetic"
			}
			replacement := selectionFixtureAccount(2, 2)
			replacement.RateMultiplier = floatPtr(0.6)
			f := newSelectionRuntimeFixture(t, owner, replacement)
			f.cache.sessionBindings["openai:floor-evidence"] = 1
			gid := int64(7)
			st := resilience.NewRequestState()
			st.EnableGatewayRequest(time.Now())
			ctx := resilience.WithRequestState(context.Background(), st)
			ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, floor: 0.4, ceilingOn: true, threshold: 0.8, sell: 1, pricingAt: time.Now()})
			selected, _, err := f.svc.SelectAccountWithSchedulerForCapability(ctx, &gid, "", "floor-evidence", "m", exclude, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true, PlatformOpenAI)
			require.NoError(t, err)
			require.EqualValues(t, 2, selected.Account.ID)
			defer selected.ReleaseFunc()
			require.True(t, pendingOpenAIStickyChange(ctx).ownerReleased)
			st.NoteGatewaySend(selected.Account.ID)
			CompleteOpenAIStickyChange(ctx, 2, true, false)
			st.CompleteGatewayForward(true, false)
			meta, _ := st.ClaimGatewayRequest(false)
			require.Len(t, meta.ProfitBasis.Vetoes, 1)
			decision := meta.ProfitBasis.Vetoes[0]
			require.EqualValues(t, 1, decision.AccountID)
			require.Equal(t, openAIProfitFilterReasonFloor, decision.Verdict)
			require.Equal(t, "stored", decision.Source)
			require.Equal(t, 1.0, decision.Sell)
			require.Equal(t, 0.1, *decision.Upstream)
			require.Equal(t, 0.4, *decision.Floor)
			require.Equal(t, 0.8, *decision.Ceiling)
			require.EqualValues(t, 2, f.cache.sessionBindings["openai:floor-evidence"])
		})
	}
}
