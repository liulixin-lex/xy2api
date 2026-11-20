package service

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// One input per HTTP request or WS turn. Later slot rechecks use the same quote
// but compare it against the refreshed account rate.
type openAITierProfitKey struct{}
type openAITierProfitInput struct {
	requested, effort string
	quotes            sync.Map // Latest candidate basis for existing slot rechecks.
	prices            sync.Map // Reused across repeated evaluations of the same frozen price inputs.
}
type openAITierProfitCandidate struct {
	gate      *openAIProfitControlGate
	accountID int64
}
type openAITierProfitPriceKey struct {
	candidate                         openAITierProfitCandidate
	group                             int64
	model, billingModel, tier, effort string
	waived, longContext, official     bool
}
type openAITierProfitQuote struct {
	ceiling float64
	source  string
	valid   bool
}

func withOpenAITierProfitInput(ctx context.Context, body []byte) context.Context {
	requested := gjson.GetBytes(body, "model").String()
	input := &openAITierProfitInput{requested: requested}
	if effort := CanonicalRequestedReasoningEffort(body, requested); effort != nil {
		input.effort = *effort
	}
	return context.WithValue(ctx, openAITierProfitKey{}, input)
}

func openAITierProcurementAccount(_ *OpenAIForwardResult, details *openAITierChargeDetails, account *Account) *Account {
	if details == nil || !details.newPricing ||
		(details.OutboundTier != OpenAIFastTierPriority && details.OutboundTier != OpenAIFastTierUltrafast &&
			details.SettledTier != OpenAIFastTierPriority && details.SettledTier != OpenAIFastTierUltrafast) {
		return nil
	}
	return account
}

func openAITierCatalogPurchase(ctx context.Context, bs *BillingService, account *Account, model, tier string, at time.Time, longContext bool) CostInput {
	resolver := NewModelPricingResolver(nil, bs)
	resolved := resolver.Resolve(ctx, PricingInput{Model: model, PricingAt: at})
	resolved.longContextPricingEnabled = longContext
	if resolved.BasePricing != nil && tier == OpenAIFastTierUltrafast && account != nil && account.IsOpenAIApiKey() && officialOpenAITierEndpoint(account) && officialOpenAIUltraModel(model) {
		// The published promotion ends at midnight Pacific on March 1 (PST).
		mult := 6.0
		if (model == "gpt-6.2-astra" || model == "gpt-6.2-sol") && at.Before(time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)) {
			mult = 3
		}
		p := *resolved.BasePricing
		p.UltrafastMultiplierOverride = &mult
		resolved.BasePricing = &p
	}
	return CostInput{Ctx: ctx, Model: model, RateMultiplier: 1, PricingAt: at, ServiceTier: tier, Resolver: resolver, Resolved: resolved}
}

func (s *OpenAIGatewayService) openAITierCandidateProfitReason(ctx context.Context, account *Account, model string, d *OpenAITierDecision) string {
	gate, _ := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
	if s == nil || gate == nil || !gate.ceilingOn || account == nil || account.PinnedIn(gate.groupID) || d == nil {
		return ""
	}
	state, _ := ctx.Value(openAITierProfitKey{}).(*openAITierProfitInput)
	key := openAITierProfitCandidate{gate, account.ID}
	clearQuote := func() string {
		if state != nil {
			state.quotes.Delete(key)
		}
		return ""
	}
	if d.OutboundTier != OpenAIFastTierPriority && d.OutboundTier != OpenAIFastTierUltrafast {
		return clearQuote()
	}
	if d.OutboundTier == OpenAIFastTierPriority && (d.PolicyVersion < OpenAITierPolicyExplicit || (!d.FreeFast && d.Source != "group")) {
		return clearQuote()
	}
	prices := d.prices
	var err error
	if prices == nil {
		prices, err = s.tierPriceSnapshot(ctx)
	}
	quote := openAITierProfitQuote{source: "tier_quote_unavailable"}
	if prices != nil && err == nil {
		_, group := openAITierGroups(ctx)
		if prices.group != nil {
			group = prices.group
		}
		if group == nil {
			return ""
		}
		ctx = context.WithValue(WithResolvedTargetPlatform(ctx, PlatformOpenAI), openAITierPricesKey{}, prices)
		billingModel := model
		var channel *Channel
		var effort string
		if prices.channels != nil {
			channel = prices.channels.channelByGroupID[group.ID]
		}
		if channel != nil && !channel.IsActive() {
			channel = nil
		}
		if state != nil {
			effort = state.effort
			if channel != nil && state.requested != "" {
				m := resolveMapping(&channelLookup{cache: prices.channels, channel: channel, platform: PlatformOpenAI}, group.ID, state.requested)
				if m.BillingModelSource == BillingModelSourceRequested {
					billingModel = state.requested
				}
				if m.BillingModelSource == BillingModelSourceChannelMapped && m.Mapped {
					billingModel = m.MappedModel
				}
			}
		}
		if d.forwardedEffort != nil {
			effort = *d.forwardedEffort
		} else if state != nil && effort != "" {
			// The candidate body has not yet been rewritten by the group effort policy.
			body, _ := json.Marshal(map[string]any{"model": state.requested, "reasoning": map[string]string{"effort": effort}})
			if rewritten, changed, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body); err == nil && changed {
				effort = gjson.GetBytes(rewritten, "reasoning.effort").String()
			}
		}
		resolver := s.resolver
		if resolver == nil {
			resolver = NewModelPricingResolver(nil, prices.billing)
		}
		resolved := resolver.Resolve(ctx, PricingInput{Model: billingModel, Group: group, GroupID: &group.ID, PricingAt: prices.at})
		if d.PolicyVersion < OpenAITierPolicyExplicit && (d.OutboundTier != OpenAIFastTierUltrafast || resolved.channelPricing == nil || resolved.channelPricing.UltrafastMultiplier == nil) {
			return clearQuote()
		}
		credential := prices.credential
		if credential == nil && (!account.IsShadow() || s.accountRepo != nil) {
			credential, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		}
		if credential != nil && err == nil {
			sell := CostInput{Ctx: ctx, Model: billingModel, Group: group, GroupID: &group.ID, RateMultiplier: 1, PricingAt: prices.at, Resolver: resolver, Resolved: resolved, ServiceTier: d.OutboundTier, ReasoningEffort: effort, LongContextBillingEnabled: openAILongContextBillingGate(credential)}
			purchase := openAITierCatalogPurchase(ctx, prices.billing, credential, model, d.OutboundTier, prices.at, accountStatsLongContextPricingEnabled(sell.LongContextBillingEnabled))
			purchase.ReasoningEffort = effort
			k := openAITierProfitPriceKey{candidate: key, group: group.ID, model: model, billingModel: billingModel,
				tier: d.OutboundTier, effort: effort, waived: d.waives(d.OutboundTier),
				longContext: accountStatsLongContextPricingEnabled(sell.LongContextBillingEnabled),
				official:    credential.IsOpenAIApiKey() && officialOpenAITierEndpoint(credential)}
			if state != nil {
				if prior, ok := state.prices.Load(k); ok {
					if cached, valid := prior.(openAITierProfitQuote); valid {
						state.quotes.Store(key, cached)
						return openAITierProfitVerdict(ctx, gate, account, cached)
					}
				}
			}
			quote = quoteOpenAITierProfit(prices.billing, gate, sell, purchase, channel, account.ID, group.ID, k.waived)
			if state != nil {
				state.prices.Store(k, quote)
			}
		}
	}
	if state != nil {
		state.quotes.Store(key, quote)
	}
	return openAITierProfitVerdict(ctx, gate, account, quote)
}

func openAITierProfitVerdict(ctx context.Context, gate *openAIProfitControlGate, account *Account, q openAITierProfitQuote) string {
	rate, source, ok := openAIProfitGateCostSource(account, gate.pricingAt)
	var ceiling *float64
	if q.valid && q.ceiling < math.MaxFloat64 {
		ceiling = &q.ceiling
	}
	verdict := profitRangeVerdict(rate, ok, ceiling, gate.floor, account.PinnedIn(gate.groupID))
	if !q.valid && (verdict == "pass" || verdict == openAIProfitFilterReasonThreshold) {
		verdict = "profit_tier_quote_unavailable"
	}
	effective := *gate
	effective.ceilingOn = ceiling != nil
	if ceiling != nil {
		effective.threshold = *ceiling
	}
	recordProfitDecision(ctx, &effective, account.ID, rate, source+"/"+q.source, ok, verdict)
	if profitVerdictAdmitted(verdict) {
		return ""
	}
	openAIProfitControlObserverInstance.recordVeto(gate.groupID, gate.platform, q.ceiling, verdict)
	return verdict
}

// Buy and sell can have different ladders. Compare each independently billed
// component on both sides of every price breakpoint before any usage is sent.
func quoteOpenAITierProfit(bs *BillingService, gate *openAIProfitControlGate, sell, buy CostInput, channel *Channel, accountID, groupID int64, waived bool) openAITierProfitQuote {
	q := openAITierProfitQuote{source: "catalog", ceiling: math.MaxFloat64}
	if sell.Resolved.Mode != BillingModeToken || buy.Resolved.Mode != BillingModeToken {
		return q
	}
	rule := matchedAccountStatsPrice(channel, accountID, groupID, PlatformOpenAI, buy.Model)
	mirror := channel != nil && channel.ApplyPricingToAccountStats
	if mirror {
		q.source = "mirror"
	}
	if rule != nil {
		q.source = "account_rule"
		if rule.BillingMode != "" && rule.BillingMode != BillingModeToken {
			return q
		}
	}
	contexts := map[int]bool{1: true}
	for _, side := range []CostInput{sell, buy} {
		r := *side.Resolved
		if side.LongContextBillingEnabled != nil && *side.LongContextBillingEnabled && !r.longContextAccountOverrideBlocked {
			r.longContextPricingEnabled = true
		}
		for _, b := range bs.contextPricingBreakpoints(side.Resolver, &r, side.Model).bounds {
			contexts[b+1] = true
		}
	}
	for n := range contexts {
		// Output length is unknown at dispatch. For this input context, the
		// total usage may fall in any contract interval reachable by adding output.
		var contracts []*ChannelModelPricing
		if rule != nil {
			sizes := map[int]bool{n: true}
			for _, iv := range rule.Intervals {
				if iv.MinTokens >= n {
					sizes[iv.MinTokens+1] = true
				}
				if iv.MaxTokens != nil && *iv.MaxTokens >= n {
					sizes[*iv.MaxTokens+1] = true
				}
			}
			for total := range sizes {
				card := *accountStatsPriceAt(rule, total)
				card.Intervals = nil
				contracts = append(contracts, &card)
			}
		}
		probes := []struct {
			tokens UsageTokens
			amount func(*CostBreakdown) float64
		}{
			{UsageTokens{InputTokens: n}, func(c *CostBreakdown) float64 { return c.InputCost }},
			{UsageTokens{InputTokens: n, OutputTokens: 1}, func(c *CostBreakdown) float64 { return c.OutputCost }},
			{UsageTokens{InputTokens: n, ImageInputTokens: n}, func(c *CostBreakdown) float64 { return c.ImageInputCost }},
			{UsageTokens{InputTokens: n, OutputTokens: 1, ImageOutputTokens: 1}, func(c *CostBreakdown) float64 { return c.ImageOutputCost }},
			{UsageTokens{CacheReadTokens: n}, func(c *CostBreakdown) float64 { return c.CacheReadCost }},
			{UsageTokens{CacheReadTokens: n, ImageCacheReadTokens: n}, func(c *CostBreakdown) float64 { return c.CacheReadCost }},
			{UsageTokens{CacheCreationTokens: n}, func(c *CostBreakdown) float64 { return c.CacheCreationCost }},
			{UsageTokens{CacheCreationTokens: n, CacheCreation5mTokens: n}, func(c *CostBreakdown) float64 { return c.CacheCreationCost }},
			{UsageTokens{CacheCreationTokens: n, CacheCreation1hTokens: n}, func(c *CostBreakdown) float64 { return c.CacheCreationCost }},
		}
		for _, probe := range probes {
			sell.Tokens, buy.Tokens = probe.tokens, probe.tokens
			retail, err := bs.CalculateCostUnified(sell)
			if err != nil {
				return q
			}
			gross := probe.amount(retail)
			net := gross
			if waived {
				standard := sell
				standard.ServiceTier = ""
				base, err := bs.CalculateCostUnified(standard)
				if err != nil {
					return q
				}
				net = math.Min(net, probe.amount(base))
			}
			cost := gross
			if !mirror {
				upstream, err := bs.CalculateCostUnified(buy)
				if err != nil {
					cost = math.NaN()
				} else {
					cost = probe.amount(upstream)
				}
			}
			if len(contracts) > 0 {
				t := probe.tokens
				if t.OutputTokens != 0 {
					t.InputTokens = 0
				}
				fallback := cost
				cost = 0
				for _, card := range contracts {
					c := fallback
					if v := accountStatsContractCost(card, t, 1); v != nil {
						c = *v * reasoningEffortBillingMultiplier(buy.ReasoningEffort, rule.ReasoningEffortMultipliers)
					}
					cost = math.Max(cost, c)
				}
			}
			if math.IsNaN(net) || math.IsNaN(cost) || math.IsInf(net, 0) || math.IsInf(cost, 0) || net < 0 || cost < 0 {
				return q
			}
			if cost > 0 {
				q.ceiling = math.Min(q.ceiling, gate.threshold*(net/cost))
			}
		}
	}
	q.valid = true
	return q
}
