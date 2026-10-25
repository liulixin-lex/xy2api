package service

import (
	"crypto/sha256"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"time"
)

// CaptureCachePolicy freezes one policy before the first cache decision.
func (s *OpenAIGatewayService) CaptureCachePolicy(c *gin.Context) *CachePolicySnapshot {
	if s == nil || s.cachePolicies == nil {
		return nil
	}
	return CaptureCachePolicy(c, s.cachePolicies)
}

// CaptureCachePolicyTurn advances only the policy owned by the next WS turn.
func (s *OpenAIGatewayService) CaptureCachePolicyTurn(c *gin.Context) *CachePolicySnapshot {
	if s == nil || s.cachePolicies == nil {
		return nil
	}
	if c != nil {
		if _, exists := c.Get(cacheTraceSampledGinKey); exists {
			c.Set(cacheTraceSampledGinKey, nil)
		}
	}
	return CaptureCachePolicyTurn(c, s.cachePolicies)
}

const cacheTraceSampledGinKey = "cacheTraceSampled"

// Freeze the observation cohort too: a fallback may change the serving-scope
// session digest, but must not change whether this request was sampled.
func cacheTraceRequestSampled(c *gin.Context, p *CachePolicySnapshot, session string) bool {
	percent := p.SamplePercent()
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	if value, exists := c.Get(cacheTraceSampledGinKey); exists {
		if sampled, ok := value.(bool); ok {
			return sampled
		}
	}
	sampled := sha256SumCacheCohort(session) < percent
	c.Set(cacheTraceSampledGinKey, sampled)
	return sampled
}

func cacheTraceChainSampled(p *CachePolicySnapshot, session string) bool {
	// A separate domain makes chain_percent conditional on the first cohort.
	return p != nil && (p.ChainPercent() >= 100 || p.ChainPercent() > 0 && cacheTraceChainCohort(session) < p.ChainPercent())
}

func ProvideOpenAIGatewayServiceWithCachePolicies(
	accountRepo AccountRepository,
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
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	policies *CachePolicyService,
	counters *CacheTraceCounters,
) *OpenAIGatewayService {
	s := NewOpenAIGatewayService(accountRepo, usageLogRepo, usageBillingRepo, userRepo, userSubRepo, userGroupRateRepo, cache, cfg, schedulerSnapshot, concurrencyService, billingService, rateLimitService, billingCacheService, httpUpstream, deferredService, openAITokenProvider, grokTokenProvider, resolver, channelService, balanceNotifyService, settingService, userPlatformQuotaRepo)
	s.cachePolicies = policies
	s.cacheCounters = counters
	return s
}

func (s *OpenAIGatewayService) cacheTraceCaps() CachePolicyCaps {
	if s != nil && s.cachePolicies != nil {
		return s.cachePolicies.caps
	}
	caps := CachePolicyCaps{1, 64, 32, 1024, 3, 512}
	if s != nil && s.cfg != nil {
		c := s.cfg.Gateway.CacheTrace
		if c.MaxSessions > 0 {
			caps.MaxSessions = min(c.MaxSessions, 1024)
		}
		if c.ChainsPerSession > 0 {
			caps.ChainsPerSession = min(c.ChainsPerSession, cachePrefixHistoryCount)
		}
		if c.MaxCheckpoints > 0 {
			caps.MaxCheckpoints = min(c.MaxCheckpoints, cachePrefixMaxBlocks)
		}
	}
	return caps
}

// Reduce retained checkpoints without changing their implicit block indices.
// Worker scratch remains bounded by the compile-time maximum. The final hash
// always survives, preserving comparisons after a sparse tail or short cap.
func limitCachePrefixCheckpoints(chain cachePrefixChain, limit int) cachePrefixChain {
	if len(chain.hashes) <= limit {
		return chain
	}
	if limit < 64 {
		hashes := make([]uint64, limit)
		copy(hashes, chain.hashes)
		chain.hashes = hashes
	} else {
		factor := 1
		for 64+(len(chain.hashes)-64)/factor > limit {
			factor *= 2
		}
		hashes := make([]uint64, 64, limit)
		copy(hashes, chain.hashes[:64])
		for i := 63 + factor; i < len(chain.hashes); i += factor {
			hashes = append(hashes, chain.hashes[i])
		}
		chain.hashes = hashes
		chain.stride = max(1, chain.stride) * factor
	}
	chain.truncated = true
	return chain
}

// Keep the trace state bound at every allocation site, including T3's timing
// state installed before a worker has produced a prefix. Caller owns telemetry.mu.
func (s *OpenAIGatewayService) limitCacheTraceSessionsLocked(telemetry *openAICacheTelemetry, session string) {
	current, exists := telemetry.sessions[session]
	if !exists || current.traceChains != nil {
		return
	}
	count, oldestKey := 0, ""
	var oldest time.Time
	for key, entry := range telemetry.sessions {
		if entry.traceChains == nil {
			continue
		}
		count++
		if oldestKey == "" || entry.at.Before(oldest) {
			oldestKey, oldest = key, entry.at
		}
	}
	if count >= s.cacheTraceCaps().MaxSessions && oldestKey != "" {
		evicted := telemetry.sessions[oldestKey]
		evicted.traceChains = nil
		telemetry.sessions[oldestKey] = evicted
	}
}

// Session digests fit this stack buffer; domain separation must not allocate on
// a budget-rejected attempt. The trace also caches the cohort before capture.
func cacheTraceChainCohort(session string) int {
	const domain = "cache-trace-chain\x00"
	var raw [128]byte
	if len(domain)+len(session) > len(raw) {
		return sha256SumCacheCohort(domain + session)
	}
	n := copy(raw[:], domain)
	n += copy(raw[n:], session)
	digest := sha256.Sum256(raw[:n])
	return int(digest[0]) * 100 / 256
}
