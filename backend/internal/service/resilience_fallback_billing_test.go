//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// Fallback requests use the serving group for prices and retain the
// authenticated group for authorization and limits.

type fallbackGroupRateRepoStub struct {
	UserGroupRateRepository
	rates map[int64]float64
}

func (s *fallbackGroupRateRepoStub) GetByUserAndGroup(_ context.Context, _, groupID int64) (*float64, error) {
	if r, ok := s.rates[groupID]; ok {
		return &r, nil
	}
	return nil, nil
}

func fallbackBillingFixture(platform string) (ctx context.Context, authenticated, moved *APIKey) {
	originID := int64(10)
	origin := &Group{ID: 10, Name: "primary", Platform: platform, RateMultiplier: 1.0, Status: StatusActive, Hydrated: true}
	fallback := &Group{ID: 20, Name: "fallback", Platform: platform, RateMultiplier: 3.0, Status: StatusActive, Hydrated: true}
	authenticated = &APIKey{ID: 77, UserID: 5, GroupID: &originID, Group: origin, User: &User{ID: 5}}
	moved = CloneAPIKeyWithGroup(authenticated, fallback)
	st := resilience.NewRequestState()
	st.MarkFallback(originID)
	ctx = resilience.WithRequestState(context.Background(), st)
	RememberAvailabilityFallbackOrigin(ctx, authenticated)
	ctx = context.WithValue(ctx, ctxkey.Group, fallback)
	return ctx, authenticated, moved
}

func TestBillingAPIKeyIsTheKeyAsAuthenticated(t *testing.T) {
	ctx, authenticated, moved := fallbackBillingFixture(PlatformAnthropic)
	require.Same(t, authenticated, BillingAPIKey(ctx, moved))
	require.Same(t, moved, BillingAPIKey(context.Background(), moved), "no fallback: the key itself")

	other := *moved
	other.ID = 78
	require.Same(t, &other, BillingAPIKey(ctx, &other), "another key is never swapped")

	plain := resilience.WithRequestState(context.Background(), resilience.NewRequestState())
	require.Same(t, moved, BillingAPIKey(plain, moved))
	require.Nil(t, BillingAPIKey(ctx, nil))
}

type fallbackPricingGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
	ids    []int64
}

func (r *fallbackPricingGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	r.ids = append(r.ids, id)
	return r.groups[id], nil
}

func TestRefreshServingPricingKeyUsesServingGroupID(t *testing.T) {
	originID, servingID := int64(10), int64(20)
	originGroup := &Group{ID: originID, Platform: PlatformOpenAI, RateMultiplier: 0.5, Status: StatusActive, Hydrated: true}
	initialServingGroup := &Group{ID: servingID, Platform: PlatformOpenAI, RateMultiplier: 3, Status: StatusActive, Hydrated: true}
	latestServingGroup := &Group{ID: servingID, Platform: PlatformOpenAI, RateMultiplier: 4, Status: StatusActive, Hydrated: true}
	origin := &APIKey{ID: 7, GroupID: &originID, Group: originGroup}
	serving := CloneAPIKeyWithGroup(origin, initialServingGroup)
	groups := &fallbackPricingGroupRepo{groups: map[int64]*Group{originID: originGroup, servingID: latestServingGroup}}
	svc := &OpenAIGatewayService{schedulerSnapshot: NewSchedulerSnapshotService(nil, nil, nil, groups, nil)}

	refreshed := svc.RefreshServingPricingKey(context.Background(), serving)
	require.NotSame(t, serving, refreshed)
	require.Equal(t, []int64{servingID}, groups.ids, "refresh uses the connection's serving group ID")
	require.Equal(t, servingID, *refreshed.GroupID)
	require.Same(t, latestServingGroup, refreshed.Group)
	require.InDelta(t, 3.0, serving.Group.RateMultiplier, 1e-12, "the connection snapshot is not mutated")
}

func TestGatewayRecordUsageUnderFallbackUsesServingGroupPrices(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rates      map[int64]float64
		peak       bool
		want       float64
		wantActual float64
	}{
		{"user_rate_of_serving_group", map[int64]float64{10: 0.5, 20: 2.0}, false, 2.0, 2.0},
		{"serving_group_rate", nil, false, 3.0, 3.0},
		{"serving_group_peak_rate", nil, true, 6.0, 6.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, moved := fallbackBillingFixture(PlatformAnthropic)
			if tc.peak {
				moved.Group.SubscriptionType = SubscriptionTypeSubscription
				moved.Group.PeakRateEnabled = true
				moved.Group.PeakStart = "00:00"
				moved.Group.PeakEnd = "23:59"
				moved.Group.PeakRateMultiplier = 2.0
			}
			usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.userGroupRateRepo = &fallbackGroupRateRepoStub{rates: tc.rates}
			svc.userGroupRateResolver = nil // rebuilt from the repository above

			err := svc.RecordUsage(ctx, &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: "fallback_gateway_" + tc.name,
					Usage:     ClaudeUsage{InputTokens: 100, OutputTokens: 50},
					Model:     "claude-sonnet-4",
					Duration:  time.Second,
				},
				APIKey:    moved,
				User:      moved.User,
				Account:   &Account{ID: 1, Platform: PlatformAnthropic},
				PricingAt: time.Now(),
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.InDelta(t, tc.want, log.RateMultiplier, 1e-12)
			require.InDelta(t, log.TotalCost*tc.wantActual, log.ActualCost, 1e-12)
			require.Equal(t, int64(20), *log.GroupID, "group_id stays the serving group")
			require.Equal(t, int64(10), *log.OriginGroupID)
		})
	}
}

func TestOpenAIRecordUsageUnderFallbackUsesServingGroupPrices(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rates map[int64]float64
		want  float64
	}{
		{"user_rate_of_serving_group", map[int64]float64{10: 0.5, 20: 2.0}, 2.0},
		{"serving_group_rate", nil, 3.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, moved := fallbackBillingFixture(PlatformOpenAI)
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &fallbackGroupRateRepoStub{rates: tc.rates})

			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "fallback_openai_" + tc.name,
					Usage:     OpenAIUsage{InputTokens: 15, OutputTokens: 4},
					Model:     "gpt-5.1",
					Duration:  time.Second,
				},
				APIKey:  moved,
				User:    moved.User,
				Account: &Account{ID: 3001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.InDelta(t, tc.want, log.RateMultiplier, 1e-12)
			require.InDelta(t, log.TotalCost*tc.want, log.ActualCost, 1e-12)
			require.Equal(t, int64(20), *log.GroupID)
			require.Equal(t, int64(10), *log.OriginGroupID)
		})
	}
}

func TestOpenAIRecordUsageUnderFallbackUsesServingImageAndVideoPrices(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		wantTotal      float64
		wantRate       float64
	}{
		{"image_unit_price_and_multiplier", PlatformOpenAI, 0.2, 2},
		{"video_per_second_price_and_multiplier", PlatformGrok, 0.4, 1.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, authenticated, moved := fallbackBillingFixture(tc.platform)
			originImage, servingImage := 0.01, 0.2
			originVideo, servingVideo := 0.01, 0.04
			authenticated.Group.ImageRateIndependent = true
			authenticated.Group.ImageRateMultiplier = 0.25
			authenticated.Group.ImagePrice2K = &originImage
			authenticated.Group.VideoRateIndependent = true
			authenticated.Group.VideoRateMultiplier = 0.5
			authenticated.Group.VideoPrice720P = &originVideo
			moved.Group.ImageRateIndependent = true
			moved.Group.ImageRateMultiplier = 2
			moved.Group.ImagePrice2K = &servingImage
			moved.Group.VideoRateIndependent = true
			moved.Group.VideoRateMultiplier = 1.5
			moved.Group.VideoPrice720P = &servingVideo

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &fallbackGroupRateRepoStub{})
			result := &OpenAIForwardResult{RequestID: "fallback_media_" + tc.name, Model: "gpt-image-1", Duration: time.Second}
			if tc.platform == PlatformGrok {
				result.Model = "grok-imagine-video"
				result.VideoCount = 1
				result.VideoResolution = "720p"
				result.VideoDurationSeconds = 10
			} else {
				result.ImageCount = 1
				result.ImageSize = "2K"
			}
			require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: result, APIKey: moved, User: moved.User,
				Account: &Account{ID: 3010, Platform: tc.platform, Type: AccountTypeAPIKey},
			}))
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.InDelta(t, tc.wantTotal, log.TotalCost, 1e-12)
			require.InDelta(t, tc.wantTotal*tc.wantRate, log.ActualCost, 1e-12)
			require.InDelta(t, tc.wantRate, log.RateMultiplier, 1e-12)
			require.Equal(t, int64(20), *log.GroupID)
			require.Equal(t, int64(10), *log.OriginGroupID)
		})
	}
}

func TestOpenAIRecordUsageUnderFallbackUsesServingLongContextRule(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		originEnabled, servingEnabled bool
		wantApplied                   bool
	}{
		{"serving_group_enabled", false, true, true},
		{"only_origin_group_enabled", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, authenticated, moved := fallbackBillingFixture(PlatformOpenAI)
			authenticated.Group.LongContextPricingEnabled = tc.originEnabled
			moved.Group.LongContextPricingEnabled = tc.servingEnabled
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			swapInOpenAILadderCatalog(t, svc)
			require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "fallback_long_context_" + tc.name,
					Usage:     OpenAIUsage{InputTokens: 300000, OutputTokens: 2000},
					Model:     "gpt-5.4-2026-03-05", Duration: time.Second,
				},
				APIKey: moved, User: moved.User,
				Account: &Account{ID: 3021, Platform: PlatformOpenAI},
			}))
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, tc.wantApplied, usageRepo.lastLog.LongContextBillingApplied)
		})
	}
}

type fallbackRPMCacheStub struct {
	mu     sync.Mutex
	counts map[int64]int
}

func (s *fallbackRPMCacheStub) IncrementUserGroupRPM(_ context.Context, _, groupID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[int64]int{}
	}
	s.counts[groupID]++
	return s.counts[groupID], nil
}
func (s *fallbackRPMCacheStub) IncrementUserRPM(context.Context, int64) (int, error) { return 1, nil }
func (s *fallbackRPMCacheStub) GetUserGroupRPM(context.Context, int64, int64) (int, error) {
	return 0, nil
}
func (s *fallbackRPMCacheStub) GetUserRPM(context.Context, int64) (int, error) { return 0, nil }

// The group RPM limit and its per-user counter are the origin group's.
func TestCheckBillingEligibilityUnderFallbackLimitsTheOriginGroup(t *testing.T) {
	ctx, authenticated, moved := fallbackBillingFixture(PlatformAnthropic)
	authenticated.Group.RPMLimit = 1
	cache := &fallbackRPMCacheStub{}
	svc := NewBillingCacheService(&balanceEligibilityCacheStub{balance: 10}, nil, nil, nil, cache, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	require.NoError(t, svc.CheckBillingEligibility(ctx, moved.User, moved, moved.Group, nil, PlatformAnthropic))
	require.ErrorIs(t, svc.CheckBillingEligibility(ctx, moved.User, moved, moved.Group, nil, PlatformAnthropic), ErrGroupRPMExceeded)
	require.Equal(t, map[int64]int{10: 2}, cache.counts)
}

// The reservation estimate uses serving-group prices.
func TestInflightEstimateUnderFallbackPricesLikeTheServingGroup(t *testing.T) {
	ctx, authenticated, moved := fallbackBillingFixture(PlatformAnthropic)
	svc := newInflightEstimateGateway(t, nil)
	req := InflightEstimateRequest{Model: "claude-sonnet-4-5", BodyBytes: 4000, MaxTokens: 1000}

	underFallback, ok := svc.EstimateInflightReservation(ctx, moved, req)
	require.True(t, ok)
	asOrigin, _ := svc.EstimateInflightReservation(context.Background(), authenticated, req)
	asServing, _ := svc.EstimateInflightReservation(context.Background(), moved, req)
	require.Greater(t, asServing, asOrigin, "fixture: the serving group is dearer")
	require.InDelta(t, asServing, underFallback, 1e-12)
}

// Q9: the serving group's channel mapping decides the billing model and
// the actual serving channel remains recorded on the usage row.
func TestGatewayRecordUsageUnderFallbackResolvesTheBillingModelLikeTheServingGroup(t *testing.T) {
	originChannel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{10}, BillingModelSource: BillingModelSourceChannelMapped,
		ModelMapping: map[string]map[string]string{"anthropic": {"my-alias": "claude-sonnet-4-5"}}}
	servingChannel := Channel{ID: 2, Status: StatusActive, GroupIDs: []int64{20}, BillingModelSource: BillingModelSourceUpstream,
		ModelMapping: map[string]map[string]string{"anthropic": {"my-alias": "claude-opus-4-1"}}}
	cs := newTestChannelService(&mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) { return []Channel{originChannel, servingChannel}, nil },
		getGroupPlatformsFn: func(context.Context, []int64) (map[int64]string, error) {
			return map[int64]string{10: "anthropic", 20: "anthropic"}, nil
		},
	})
	record := func(ctx context.Context, key *APIKey, group int64, upstream string) *UsageLog {
		usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
		svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		svc.channelService = cs
		svc.resolver = NewModelPricingResolver(cs, svc.billingService)
		mapping := cs.ResolveChannelMapping(context.Background(), group, "my-alias")
		require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{
			Result: &ForwardResult{
				RequestID: "fallback_billing_model",
				Usage:     ClaudeUsage{InputTokens: 1000, OutputTokens: 500},
				Model:     upstream,
				Duration:  time.Second,
			},
			APIKey:             key,
			User:               key.User,
			Account:            &Account{ID: 1, Platform: PlatformAnthropic},
			ChannelUsageFields: mapping.ToUsageFields("my-alias", upstream),
		}))
		require.NotNil(t, usageRepo.lastLog)
		return usageRepo.lastLog
	}

	ctx, authenticated, moved := fallbackBillingFixture(PlatformAnthropic)
	// The handler resolves the serving group's channel for routing.
	underFallback := record(ctx, moved, 20, "claude-opus-4-1")
	asOrigin := record(context.Background(), authenticated, 10, "claude-sonnet-4-5")
	asServing := record(context.Background(), moved, 20, "claude-opus-4-1")

	require.NotEqual(t, asOrigin.TotalCost, asServing.TotalCost, "fixture: the two channels bill different models")
	require.InDelta(t, asServing.TotalCost, underFallback.TotalCost, 1e-12)
	require.InDelta(t, asServing.ActualCost, underFallback.ActualCost, 1e-12)
	require.Equal(t, int64(2), *underFallback.ChannelID, "the serving channel stays recorded")
}

// A live call served by a fallback group records the group it was made for,
// and its sideband belongs to the caller's group whichever group serves the
// sideband request.
func TestLiveCallUnderFallbackKeepsTheCallerGroup(t *testing.T) {
	record := &LiveCallRecord{
		CallID:        "call_fallback",
		CallHash:      hashLiveCallID("call_fallback"),
		AccountID:     11,
		APIKeyID:      77,
		UserID:        5,
		GroupID:       20,
		OriginGroupID: 10,
		LeaseID:       "lease-fallback",
		Model:         "gpt-live-test",
		CreatedAt:     time.Now().Add(-time.Second),
		ExpiresAt:     time.Now().Add(time.Hour),
		Controller:    LiveControllerPending,
	}
	store := &liveTestStore{}
	require.NoError(t, store.SaveLiveCall(context.Background(), record, time.Hour))
	usageRepo := &liveTestUsageRepo{}
	svc := &OpenAIGatewayService{cache: store, concurrencyService: NewConcurrencyService(&liveTestConcurrencyCache{}), usageLogRepo: usageRepo}

	caller, serving := int64(10), int64(20)
	_, err := svc.GetLiveCallForIdentity(context.Background(), record.CallID, LiveCallIdentity{APIKeyID: 77, UserID: 5, GroupID: &caller})
	require.NoError(t, err, "the caller's group owns the call")
	_, err = svc.GetLiveCallForIdentity(context.Background(), record.CallID, LiveCallIdentity{APIKeyID: 77, UserID: 5, GroupID: &serving})
	require.ErrorIs(t, err, ErrLiveIdentityMismatch)

	svc.finalizeLiveCall(record)
	usageRepo.mu.Lock()
	defer usageRepo.mu.Unlock()
	require.Len(t, usageRepo.logs, 1)
	require.Equal(t, int64(20), *usageRepo.logs[0].GroupID)
	require.NotNil(t, usageRepo.logs[0].OriginGroupID)
	require.Equal(t, int64(10), *usageRepo.logs[0].OriginGroupID)
}

// liveOriginGroupID is what CreateLiveCall stores on a new record.
func TestLiveOriginGroupIDComesFromTheRequestState(t *testing.T) {
	ctx, _, _ := fallbackBillingFixture(PlatformOpenAI)
	require.Equal(t, int64(10), liveOriginGroupID(ctx))
	require.Zero(t, liveOriginGroupID(context.Background()))
}

// Batch image eligibility remains with the origin group while price and
// serving accounts come from the fallback group.
func TestBatchImageSubmitUnderFallbackSplitsAuthorizationAndPricing(t *testing.T) {
	svc, repo, _, _, _ := newTestBatchImagePublicService(true)
	origin, serving := int64(10), int64(20)
	svc.GroupRepo = &publicBatchImageGroupRepo{groups: map[int64]*Group{
		origin: {ID: origin, Platform: PlatformGemini, RateMultiplier: 1.0, AllowImageGeneration: true, AllowBatchImageGeneration: true,
			BatchImageDiscountMultiplier: 1, BatchImageHoldMultiplier: 1},
		serving: {ID: serving, Platform: PlatformGemini, RateMultiplier: 3.0, AllowImageGeneration: true, AllowBatchImageGeneration: false,
			BatchImageDiscountMultiplier: 1, BatchImageHoldMultiplier: 1},
	}}

	_, err := svc.Submit(context.Background(), BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &serving, AuthorizationGroupID: &origin, PricingGroupID: &serving}, validBatchImageSubmitRequest(), "")
	require.ErrorIs(t, err, ErrBatchImageNoAccountAvailable, "a serving group without the capability is unavailable, not an origin permission error")
	require.NotErrorIs(t, err, ErrBatchImageGroupDisabled)
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.NotContains(t, infraerrors.Message(err), "serving")
	require.Empty(t, repo.jobs)
}

// Users see their errors under their key's group, not a serving fallback
// group (PM decision Q4).
func TestUserErrorRequestShowsTheKeyGroup(t *testing.T) {
	served := int64(20)
	e := &OpsErrorLog{GroupID: &served, GroupName: "GPT-企业专线", APIKeyGroupName: "GPT-不降智", StatusCode: 502}
	require.Equal(t, "GPT-不降智", ToUserErrorRequest(e).GroupName)

	e.APIKeyGroupName = ""
	require.Equal(t, "GPT-企业专线", ToUserErrorRequest(e).GroupName, "a key without a group keeps the recorded group")
}

// The fallback group's Fast and Fast-at-standard rules are also the user's
// pricing policy; the origin group's Fast setting has no pricing effect.
func TestOpenAIRecordUsageUnderFallbackAppliesServingFastRules(t *testing.T) {
	inputPrice, outputPrice, fastMultiplier := 0.001, 0.002, 3.0
	standardTotal := 100*inputPrice + 50*outputPrice
	for _, tc := range []struct {
		name                    string
		servingFree, originFree bool
		observed                string
		wantActual              float64
	}{
		{"serving_priority_price", false, true, "priority", standardTotal * fastMultiplier * 3},
		{"serving_fast_at_standard", true, false, "priority", standardTotal * 3},
		{"serving_forced_fast_upstream_default", false, false, "default", standardTotal * fastMultiplier * 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, authenticated, moved := fallbackBillingFixture(PlatformOpenAI)
			moved.Group.ForceOpenAIFast = true
			moved.Group.FreeOpenAIFast = tc.servingFree
			authenticated.Group.FreeOpenAIFast = tc.originFree
			moved.Group.ModelPricing = []ChannelModelPricing{{
				Models: []string{"gpt-5.6-sol"}, BillingMode: BillingModeToken,
				InputPrice: &inputPrice, OutputPrice: &outputPrice, FastMultiplier: &fastMultiplier,
			}}
			account := &Account{ID: 3020, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &fallbackGroupRateRepoStub{})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			priority := "priority"
			require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "fallback_serving_fast_" + tc.name,
					Usage:     OpenAIUsage{InputTokens: 100, OutputTokens: 50},
					Model:     "gpt-5.6-sol", Duration: time.Second,
					ServiceTier: &priority, UpstreamResponseServiceTier: tc.observed,
				},
				APIKey: moved, User: moved.User, Account: account,
			}))
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.InDelta(t, standardTotal*fastMultiplier, log.TotalCost, 1e-10)
			require.InDelta(t, tc.wantActual, log.ActualCost, 1e-10)
		})
	}
}
