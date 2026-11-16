//go:build unit

package service

import (
	"context"
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
	for _, floor := range []bool{true, false} {
		for _, stale := range []bool{true, false} {
			t.Run(map[bool]string{true: "floor", false: "ceiling"}[floor]+map[bool]string{true: " DB change", false: " snapshot"}[stale], func(t *testing.T) {
				gid := int64(7)
				now := time.Now()
				rate := 0.1
				if !floor {
					rate = 0.9
				}
				account := profitControlWSAccount(1, rate, now)
				account.Extra = nil
				account.GroupIDs = []int64{gid}
				account.AccountGroups = []AccountGroup{{GroupID: gid, AccountID: 1}}
				cached := account
				if stale {
					cached.RateMultiplier = floatPtr(0.5)
				}
				repo := schedulerTestOpenAIAccountRepo{accounts: []Account{account}}
				cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:range-sticky": 1}}
				svc := &OpenAIGatewayService{accountRepo: repo, cache: cache, rateLimitService: newOpenAISelectionRateLimitService()}
				// A scheduler snapshot makes the initial owner stale, while the repository
				// remains authoritative for the subsequent membership/profit recheck.
				if stale {
					svc.schedulerSnapshot = NewSchedulerSnapshotService(&openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: &cached}}, nil, repo, nil, nil)
				}
				gate := &openAIProfitControlGate{groupID: gid, platform: PlatformOpenAI, ceilingOn: true, threshold: 0.8, floor: 0.4, pricingAt: now}
				ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, gate)
				_, _, err := svc.selectBySessionHash(ctx, OpenAIAccountScheduleRequest{GroupID: &gid, Platform: PlatformOpenAI, SessionHash: "range-sticky", RequestedModel: "gpt-test"})
				require.NoError(t, err)
				if floor {
					require.Zero(t, cache.sessionBindings["openai:range-sticky"])
					require.NoError(t, svc.bindOpenAIStickySessionDuringSelection(ctx, &gid, "range-sticky", 2))
					require.Zero(t, cache.sessionBindings["openai:range-sticky"])
					require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &gid, "range-sticky", 2))
					require.Equal(t, int64(2), cache.sessionBindings["openai:range-sticky"])
				} else {
					require.Equal(t, int64(1), cache.sessionBindings["openai:range-sticky"])
				}
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
	for _, rate := range []float64{math.NaN(), math.Inf(1), -1, 0.12345, 1000000} {
		require.Error(t, ValidateMinUpstreamRate(PlatformOpenAI, &rate))
	}
	for _, platform := range []string{PlatformGrok, PlatformAnthropic, PlatformGemini, PlatformComposite} {
		require.Error(t, ValidateMinUpstreamRate(platform, floatPtr(0.4)))
	}
	require.NoError(t, ValidateMinUpstreamRate(PlatformOpenAI, floatPtr(0.1234)))
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
