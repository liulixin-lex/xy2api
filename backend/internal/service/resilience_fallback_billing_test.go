//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/stretchr/testify/require"
)

// An availability fallback group only routes a request. The user's price,
// limits and permissions are those of the group the request was made for,
// as if that group had served it (fallback-billing design §2.1, PM decision).
// The fixtures move the user's group 10 to group 20, priced three times
// higher.

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

func TestGatewayRecordUsageUnderFallbackBillsTheOriginGroup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rates map[int64]float64
		want  float64
	}{
		{"user_rate_of_the_origin_group", map[int64]float64{10: 0.5, 20: 2.0}, 0.5},
		{"origin_group_rate", nil, 1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, moved := fallbackBillingFixture(PlatformAnthropic)
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
				APIKey:  moved,
				User:    moved.User,
				Account: &Account{ID: 1, Platform: PlatformAnthropic},
			})
			require.NoError(t, err)
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.InDelta(t, tc.want, log.RateMultiplier, 1e-12)
			require.InDelta(t, log.TotalCost*tc.want, log.ActualCost, 1e-12)
			require.Equal(t, int64(20), *log.GroupID, "group_id stays the serving group")
			require.Equal(t, int64(10), *log.OriginGroupID)
		})
	}
}

func TestOpenAIRecordUsageUnderFallbackBillsTheOriginGroup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rates map[int64]float64
		want  float64
	}{
		{"user_rate_of_the_origin_group", map[int64]float64{10: 0.5, 20: 2.0}, 0.5},
		{"origin_group_rate", nil, 1.0},
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

// PM decision Q8: the serving group's ForceOpenAIFast decides the tier sent
// upstream; billing follows the origin group's rules. The origin group does
// not force Fast, so a downgraded response is billed at the observed tier.
func TestOpenAIRecordUsageUnderFallbackAppliesTheOriginFastRules(t *testing.T) {
	ctx, _, moved := fallbackBillingFixture(PlatformOpenAI)
	moved.Group.ForceOpenAIFast = true
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, &fallbackGroupRateRepoStub{})
	priority, observed := "priority", "default"

	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:                   "fallback_fast",
			Usage:                       OpenAIUsage{InputTokens: 15, OutputTokens: 4},
			Model:                       "gpt-5.1",
			Duration:                    time.Second,
			ServiceTier:                 &priority,
			UpstreamResponseServiceTier: observed,
		},
		APIKey:  moved,
		User:    moved.User,
		Account: &Account{ID: 3001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog.ServiceTier)
	require.Equal(t, "default", *usageRepo.lastLog.ServiceTier, "the origin group does not force Fast")
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

// The reservation estimate prices the request like its billing does.
func TestInflightEstimateUnderFallbackPricesLikeTheOriginGroup(t *testing.T) {
	ctx, authenticated, moved := fallbackBillingFixture(PlatformAnthropic)
	svc := newInflightEstimateGateway(t, nil)
	req := InflightEstimateRequest{Model: "claude-sonnet-4-5", BodyBytes: 4000, MaxTokens: 1000}

	underFallback, ok := svc.EstimateInflightReservation(ctx, moved, req)
	require.True(t, ok)
	asOrigin, _ := svc.EstimateInflightReservation(context.Background(), authenticated, req)
	asServing, _ := svc.EstimateInflightReservation(context.Background(), moved, req)
	require.Greater(t, asServing, asOrigin, "fixture: the serving group is dearer")
	require.InDelta(t, asOrigin, underFallback, 1e-12)
}

// PM decision Q9: the billing model is decided as the origin group would
// decide it. Here the origin group's channel bills the channel-mapped model
// (claude-sonnet-4-5); the serving group's channel maps the alias to another
// model and bills the upstream model.
func TestGatewayRecordUsageUnderFallbackResolvesTheBillingModelLikeTheOriginGroup(t *testing.T) {
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
	require.InDelta(t, asOrigin.TotalCost, underFallback.TotalCost, 1e-12)
	require.InDelta(t, asOrigin.ActualCost, underFallback.ActualCost, 1e-12)
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

// Batch images: price and permission are the origin group's; the serving
// group's accounts take the job.
func TestBatchImageSubmitUnderFallbackPricesTheOriginGroup(t *testing.T) {
	svc, repo, _, _, _ := newTestBatchImagePublicService(true)
	origin, serving := int64(10), int64(20)
	svc.GroupRepo = &publicBatchImageGroupRepo{groups: map[int64]*Group{
		origin: {ID: origin, Platform: PlatformGemini, RateMultiplier: 1.0, AllowImageGeneration: true, AllowBatchImageGeneration: true,
			BatchImageDiscountMultiplier: 1, BatchImageHoldMultiplier: 1},
		serving: {ID: serving, Platform: PlatformGemini, RateMultiplier: 3.0, AllowImageGeneration: true, AllowBatchImageGeneration: false,
			BatchImageDiscountMultiplier: 1, BatchImageHoldMultiplier: 1},
	}}

	got, err := svc.Submit(context.Background(), BatchImageOwner{UserID: 11, APIKeyID: 22, GroupID: &serving, BillingGroupID: &origin}, validBatchImageSubmitRequest(), "")
	require.NoError(t, err, "the origin group allows batch images")
	require.InDelta(t, 1.0, repo.jobs[got.ID].GroupRateMultiplier, 1e-12)
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
