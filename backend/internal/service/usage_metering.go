package service

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

// MeteredUsage freezes the final observed attempt before settlement adjustments.
// Presence and source are separate from amounts: an absent counter is not zero.
type MeteredUsage struct {
	InputTokens            int     `json:"input_tokens"`
	OutputTokens           int     `json:"output_tokens"`
	CacheReadTokens        int     `json:"cache_read_input_tokens"`
	CacheCreationTokens    int     `json:"cache_creation_input_tokens"`
	CacheCreation5mTokens  int     `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens  int     `json:"cache_creation_1h_tokens"`
	ImageInputTokens       int     `json:"image_input_tokens,omitempty"`
	ImageCacheReadTokens   int     `json:"image_cache_read_tokens,omitempty"`
	ImageOutputTokens      int     `json:"image_output_tokens,omitempty"`
	InputPresent           bool    `json:"input_present"`
	OutputPresent          bool    `json:"output_present"`
	CacheReadPresent       bool    `json:"cache_read_present"`
	CacheCreationPresent   bool    `json:"cache_creation_present"`
	CacheCreation5mPresent bool    `json:"cache_creation_5m_present"`
	CacheCreation1hPresent bool    `json:"cache_creation_1h_present"`
	Incomplete             bool    `json:"incomplete"`
	Inconsistent           bool    `json:"inconsistent"`
	RawPromptTokens        *int    `json:"raw_prompt_tokens,omitempty"`
	RawCacheReadTokens     *int    `json:"raw_cache_read_tokens,omitempty"`
	Observed               *bool   `json:"observed,omitempty"`
	Source                 *string `json:"source,omitempty"`
}

func freezeMeteredUsage(u ClaudeUsage, known, observed bool, source string) MeteredUsage {
	m := MeteredUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens, CacheCreationTokens: u.CacheCreationInputTokens,
		CacheCreation5mTokens: u.CacheCreation5mTokens, CacheCreation1hTokens: u.CacheCreation1hTokens,
		ImageOutputTokens: u.ImageOutputTokens, InputPresent: u.InputTokensPresent, OutputPresent: u.OutputTokensPresent,
		CacheReadPresent: u.CacheReadInputTokensPresent, CacheCreationPresent: u.CacheCreationInputTokensPresent,
		CacheCreation5mPresent: u.CacheCreation5mTokensPresent, CacheCreation1hPresent: u.CacheCreation1hTokensPresent,
		Incomplete: u.UsageIncomplete, Inconsistent: u.UsageInconsistent,
		RawPromptTokens: cloneUsageFactValue(u.RawPromptTokens), RawCacheReadTokens: cloneUsageFactValue(u.RawCacheReadTokens)}
	if known {
		m.Observed = &observed
	}
	if source != "" {
		m.Source = &source
	}
	return m
}

func (m MeteredUsage) tokens() UsageTokens {
	return UsageTokens{InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
		CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens,
		CacheCreationTokensPresent: m.CacheCreationPresent || m.CacheCreation5mPresent || m.CacheCreation1hPresent,
		CacheCreation5mTokens:      m.CacheCreation5mTokens, CacheCreation1hTokens: m.CacheCreation1hTokens,
		CacheCreation5mTokensPresent: m.CacheCreation5mPresent, CacheCreation1hTokensPresent: m.CacheCreation1hPresent,
		ImageInputTokens: m.ImageInputTokens, ImageCacheReadTokens: m.ImageCacheReadTokens, ImageOutputTokens: m.ImageOutputTokens}
}

type meteringAdjustmentKind struct {
	Kind               string `json:"kind"`
	InputToCacheTokens int    `json:"input_to_cache_tokens,omitempty"`
}

type meteringAdjustment struct {
	Version                int                      `json:"v"`
	RawUsage               MeteredUsage             `json:"raw_usage"`
	Adjustments            []meteringAdjustmentKind `json:"adjustments,omitempty"`
	CustomerGrossCost      float64                  `json:"customer_gross_cost"`
	CustomerCredit         float64                  `json:"customer_credit"`
	SupplierCostSource     string                   `json:"supplier_cost_source"`
	CostScope              string                   `json:"cost_scope"`
	FailedAttemptCostKnown bool                     `json:"failed_attempt_cost_known"`
}

type supplierCostEstimate struct {
	Amount *float64
	Source string
}

// Keep both customer calculations on one immutable catalog/channel snapshot.
// The original billing lookup has already loaded the channel cache; freezing it
// must never introduce another database lookup for optional metering evidence.
func freezeMeteringPricing(s *GatewayService) *GatewayService {
	if s.billingService == nil {
		return s
	}
	b := s.billingService
	bs := &BillingService{cfg: b.cfg, fallbackPrices: b.fallbackPrices}
	if catalog := b.pricingService; catalog != nil {
		catalog.mu.RLock()
		bs.pricingService = &PricingService{cfg: catalog.cfg, pricingData: catalog.pricingData, pricingOrigins: catalog.pricingOrigins,
			localHash: catalog.localHash, customFilesHash: catalog.customFilesHash}
		catalog.mu.RUnlock()
	}
	freezeChannel := func(cs *ChannelService) *ChannelService {
		if cs == nil {
			return nil
		}
		cached, _ := cs.cache.Load().(*channelCache)
		if cached == nil {
			return nil
		}
		copy := *cached
		copy.loadedAt = time.Now()
		frozen := &ChannelService{}
		frozen.cache.Store(&copy)
		return frozen
	}
	cs := freezeChannel(s.channelService)
	// Missing optional channel metadata cannot change the existing bill.
	if s.channelService != nil && cs == nil {
		return s
	}
	frozen := &GatewayService{billingService: bs, channelService: cs}
	if s.resolver != nil {
		resolverChannel := cs
		if s.resolver.channelService != s.channelService {
			resolverChannel = freezeChannel(s.resolver.channelService)
			if s.resolver.channelService != nil && resolverChannel == nil {
				return s
			}
		}
		frozen.resolver = NewModelPricingResolver(resolverChannel, bs)
	}
	return frozen
}

// supplierCostFromMetering is pure: all price inputs are already resolved. The
// mirror and fallback paths consume the UNADJUSTED total, as well as raw tokens.
func supplierCostFromMetering(tokens UsageTokens, count int, gross *float64, contract *ChannelModelPricing,
	mirror bool, catalog *CostBreakdown, effort string, rate float64) supplierCostEstimate {
	finish := func(amount float64, source string) supplierCostEstimate {
		amount *= rate
		if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) || amount >= 1e10 {
			return supplierCostEstimate{Source: "unknown"}
		}
		return supplierCostEstimate{Amount: &amount, Source: source}
	}
	if cost := meteredContractCost(contract, tokens, count); cost != nil {
		return finish(*cost*reasoningEffortBillingMultiplier(effort, contract.ReasoningEffortMultipliers), "contract")
	}
	if mirror && gross != nil {
		return finish(*gross, "mirror")
	}
	if catalog != nil {
		source := "catalog"
		p := decodePricingSource(catalog.PricingSource)
		if p.Source == "builtin" || p.Source == "fallback_file" || p.Match == "family" || p.Match == "default" || p.Match == "openai_variant" {
			source = "fallback"
		}
		return finish(catalog.TotalCost, source)
	}
	if gross != nil {
		return finish(*gross, "fallback")
	}
	return supplierCostEstimate{Source: "unknown"}
}

func meteredContractCost(card *ChannelModelPricing, tokens UsageTokens, count int) *float64 {
	if card == nil {
		return nil
	}
	if cost := calculateStatsCost(card, tokens, count); cost != nil {
		return cost
	}
	if card.BillingMode != "" && card.BillingMode != BillingModeToken {
		if card.PerRequestPrice != nil && *card.PerRequestPrice == 0 {
			return new(float64)
		}
		return nil
	}
	p := accountStatsPriceAt(card, tokens.InputTokens+tokens.OutputTokens+tokens.CacheReadTokens+tokens.CacheCreationTokens)
	cache5m, cache1h := normalizeCacheCreationBreakdown(tokens)
	if cache5m == 0 && cache1h == 0 {
		cache5m = tokens.CacheCreationTokens
	}
	cachePrice1h := p.CacheWrite1hPrice
	if cachePrice1h == nil {
		cachePrice1h = p.CacheWritePrice
	}
	used := false
	for _, part := range []struct {
		count int
		price *float64
	}{
		{tokens.InputTokens, p.InputPrice}, {tokens.OutputTokens, p.OutputPrice},
		{tokens.CacheReadTokens, p.CacheReadPrice}, {cache5m, p.CacheWritePrice}, {cache1h, cachePrice1h},
		{tokens.ImageOutputTokens, p.ImageOutputPrice},
	} {
		if part.count <= 0 {
			continue
		}
		used = true
		if part.price == nil || *part.price != 0 {
			return nil
		}
	}
	if used {
		return new(float64)
	}
	return nil
}

func knownMeteredTotal(cost *CostBreakdown) *float64 {
	if cost == nil {
		return nil
	}
	// A priced zero is free. A failed lookup's empty zero breakdown is unknown.
	if cost.TotalCost > 0 || len(cost.PricingSource) > 0 {
		return &cost.TotalCost
	}
	return nil
}

// Optional accounting must not add a cache load or a repository/network lookup.
// Reuse the channel snapshot populated by the existing billing path.
func estimateMeteredSupplierCost(ctx context.Context, cs *ChannelService, bs *BillingService,
	log *UsageLog, metered MeteredUsage, gross *CostBreakdown, accountID, groupID int64,
	model string, at time.Time, longContext bool, procurement *Account) supplierCostEstimate {
	if metered.Incomplete || metered.Inconsistent || (metered.Observed != nil && !*metered.Observed) {
		return supplierCostEstimate{Source: "unknown"}
	}
	var channel *Channel
	var platform string
	if cs != nil {
		cache, _ := cs.cache.Load().(*channelCache)
		if prices := openAITierPricesFromContext(ctx); prices != nil && prices.channels != nil {
			cache = prices.channels
		}
		if cache == nil {
			return supplierCostEstimate{Source: "unknown"}
		}
		channel, platform = cache.channelByGroupID[groupID], cache.groupPlatform[groupID]
		if channel != nil && !channel.IsActive() {
			channel = nil
		}
	}
	if procurement != nil {
		platform = PlatformOpenAI
	}
	contract := matchedAccountStatsPrice(channel, accountID, groupID, platform, model)
	mirror := channel != nil && channel.ApplyPricingToAccountStats
	var catalog *CostBreakdown
	// Preserve the historical channel requirement for the catalog branch.
	if bs != nil && model != "" && (channel != nil || procurement != nil) {
		buy := openAITierCatalogPurchase(ctx, bs, procurement, model, optionalStringValue(log.ServiceTier), at, longContext)
		buy.Tokens, buy.ReasoningEffort = metered.tokens(), optionalStringValue(log.ReasoningEffort)
		catalog, _ = bs.CalculateCostUnified(buy)
	}
	count := max(log.ImageCount, 1)
	rate := 1.0
	if log.AccountRateMultiplier != nil {
		rate = *log.AccountRateMultiplier
	}
	return supplierCostFromMetering(metered.tokens(), count, knownMeteredTotal(gross), contract, mirror, catalog, optionalStringValue(log.ReasoningEffort), rate)
}

func applyMeteringAdjustment(log *UsageLog, raw MeteredUsage, gross *CostBreakdown, supplier supplierCostEstimate, adjustments []meteringAdjustmentKind) {
	log.SupplierCostEstimate = supplier.Amount
	if len(adjustments) == 0 && supplier.Amount != nil {
		return
	}
	grossCost := log.ActualCost
	if gross != nil {
		grossCost = gross.ActualCost
	}
	log.MeteringAdjustment, _ = json.Marshal(meteringAdjustment{Version: 1, RawUsage: raw,
		Adjustments: adjustments, CustomerGrossCost: grossCost, CustomerCredit: grossCost - log.ActualCost,
		SupplierCostSource: supplier.Source, CostScope: "final_observed_attempt"})
}
