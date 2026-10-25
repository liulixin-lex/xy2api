package service

import "github.com/liulixin-lex/xy2api/internal/config"

func ProvideGatewayServiceWithCacheTrace(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	identityService *IdentityService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	claudeTokenProvider *ClaudeTokenProvider,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
	digestStore *DigestSessionStore,
	settingService *SettingService,
	tlsFPProfileService *TLSFingerprintProfileService,
	channelService *ChannelService,
	resolver *ModelPricingResolver,
	compositeResolver *CompositeRouteResolver,
	balanceNotifyService *BalanceNotifyService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	engine *OpenAIGatewayService,
) *GatewayService {
	s := NewGatewayService(accountRepo, groupRepo, usageLogRepo, usageBillingRepo, userRepo, userSubRepo, userGroupRateRepo, cache, cfg, schedulerSnapshot, concurrencyService, billingService, rateLimitService, billingCacheService, identityService, httpUpstream, deferredService, claudeTokenProvider, sessionLimitCache, rpmCache, digestStore, settingService, tlsFPProfileService, channelService, resolver, compositeResolver, balanceNotifyService, userPlatformQuotaRepo)
	s.providerTraceEngine = engine
	return s
}

func ProvideGeminiMessagesCompatServiceWithCacheTrace(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	cache GatewayCache,
	schedulerSnapshot *SchedulerSnapshotService,
	tokenProvider *GeminiTokenProvider,
	rateLimitService *RateLimitService,
	httpUpstream HTTPUpstream,
	antigravityGatewayService *AntigravityGatewayService,
	cfg *config.Config,
	engine *OpenAIGatewayService,
) *GeminiMessagesCompatService {
	s := NewGeminiMessagesCompatService(accountRepo, groupRepo, cache, schedulerSnapshot, tokenProvider, rateLimitService, httpUpstream, antigravityGatewayService, cfg)
	s.providerTraceEngine = engine
	return s
}
