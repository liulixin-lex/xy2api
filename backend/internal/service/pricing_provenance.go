package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"maps"
	"strings"
)

// PricingProvenance freezes the selected price card and the effective units used
// for settlement. Overrides name only price fields; they never contain request data.
type PricingProvenance struct {
	ModelKey        string              `json:"model_key,omitempty"`
	Source          string              `json:"source,omitempty"`
	Match           string              `json:"match,omitempty"`
	Revision        string              `json:"revision,omitempty"`
	EffectivePrices map[string]float64  `json:"effective_prices,omitempty"`
	Overrides       map[string]string   `json:"overrides,omitempty"`
	Components      []PricingProvenance `json:"components,omitempty"`
}

func pricingContentRevision(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func pricingSourceJSON(source PricingProvenance) json.RawMessage {
	raw, _ := json.Marshal(source)
	return raw
}
func decodePricingSource(raw json.RawMessage) PricingProvenance {
	var source PricingProvenance
	_ = json.Unmarshal(raw, &source)
	return source
}
func attachPricingSource(cost *CostBreakdown, raw json.RawMessage) {
	if cost == nil || len(raw) == 0 {
		return
	}
	source := decodePricingSource(raw)
	effective := decodePricingSource(cost.PricingSource)
	if len(effective.EffectivePrices) > 0 {
		source.EffectivePrices = effective.EffectivePrices
	}
	cost.PricingSource = pricingSourceJSON(source)
}
func scalePricingSource(cost *CostBreakdown, multiplier float64) {
	if len(cost.PricingSource) == 0 {
		return
	}
	source := decodePricingSource(cost.PricingSource)
	for field, unit := range source.EffectivePrices {
		source.EffectivePrices[field] = unit * multiplier
	}
	cost.PricingSource = pricingSourceJSON(source)
}
func (s *BillingService) warnBillingPricingFallback(raw json.RawMessage) {
	source := decodePricingSource(raw)
	if source.Source == "builtin" || source.Match == "family" || source.Match == "default" || source.Match == "openai_variant" {
		key := source.ModelKey + ":" + source.Revision + ":" + source.Match
		if _, seen := s.fallbackWarnSeen.LoadOrStore(key, struct{}{}); !seen {
			log.Printf("[Billing] Using fallback pricing: model=%s source=%s match=%s revision=%s", source.ModelKey, source.Source, source.Match, source.Revision)
		}
	}
	for _, component := range source.Components {
		s.warnBillingPricingFallback(pricingSourceJSON(component))
	}
}
func configuredPricingSource(raw json.RawMessage, config *ChannelModelPricing, source string) json.RawMessage {
	base := decodePricingSource(raw)
	if base.ModelKey == "" {
		base.ModelKey = strings.Join(config.Models, ",")
		base.Source = source
		base.Revision = pricingContentRevision(config)
		base.Match = "configured"
	}
	base.Overrides = maps.Clone(base.Overrides)
	if base.Overrides == nil {
		base.Overrides = map[string]string{}
	}
	origin := source + ":" + pricingContentRevision(config)
	for name, value := range map[string]*float64{"input": config.InputPrice, "output": config.OutputPrice, "cache_write": config.CacheWritePrice, "cache_write_1h": config.CacheWrite1hPrice, "cache_read": config.CacheReadPrice, "image_input": config.ImageInputPrice, "image_output": config.ImageOutputPrice, "per_request": config.PerRequestPrice, "fast_multiplier": config.FastMultiplier, "ultrafast_multiplier": config.UltrafastMultiplier, "flex_multiplier": config.FlexMultiplier} {
		if value != nil {
			base.Overrides[name] = origin
		}
	}
	if len(config.ReasoningEffortMultipliers) > 0 {
		base.Overrides["reasoning_effort_multipliers"] = origin
	}
	return pricingSourceJSON(base)
}
func intervalPricingSource(resolved *ResolvedPricing, contextTokens int) json.RawMessage {
	source := decodePricingSource(resolved.PricingSource)
	if iv := FindMatchingInterval(resolved.Intervals, contextTokens); iv != nil {
		source.Overrides = maps.Clone(source.Overrides)
		if source.Overrides == nil {
			source.Overrides = map[string]string{}
		}
		origin := resolved.Source + ":interval:" + pricingContentRevision(iv)
		for name, value := range map[string]*float64{"input": iv.InputPrice, "output": iv.OutputPrice, "cache_write": iv.CacheWritePrice, "cache_write_1h": iv.CacheWrite1hPrice, "cache_read": iv.CacheReadPrice, "input_multiplier": iv.InputMultiplier, "output_multiplier": iv.OutputMultiplier, "cache_write_multiplier": iv.CacheWriteMultiplier, "cache_read_multiplier": iv.CacheReadMultiplier} {
			if value != nil {
				source.Overrides[name] = origin
			}
		}
	}
	return pricingSourceJSON(source)
}
func tokenEffectivePrices(p *ModelPricing, input, output, read, write, writeMult, tier float64) json.RawMessage {
	imageIn, imageOut, imageRead := p.ImageInputPricePerToken, p.ImageOutputPricePerToken, p.ImageCacheReadPricePerToken
	if imageIn == 0 {
		imageIn = input
	}
	if imageOut == 0 && !p.ImageOutputPriceExplicit {
		imageOut = output
	}
	if imageRead <= 0 {
		imageRead = read
	}
	return pricingSourceJSON(PricingProvenance{EffectivePrices: map[string]float64{"input": input * tier, "output": output * tier, "cache_read": read * tier, "cache_write": write * writeMult * tier, "cache_write_5m": p.CacheCreation5mPrice * writeMult * tier, "cache_write_1h": p.CacheCreation1hPrice * writeMult * tier, "image_input": imageIn * tier, "image_output": imageOut * tier, "image_cache_read": imageRead * tier}})
}

func requestPricingProvenance(resolved *ResolvedPricing, input CostInput, unit float64) PricingProvenance {
	source := decodePricingSource(resolved.PricingSource)
	source.EffectivePrices = map[string]float64{"per_request": unit}
	source.Overrides = maps.Clone(source.Overrides)
	if source.Overrides == nil {
		source.Overrides = map[string]string{}
	}
	source.Overrides["request_tiers"] = resolved.Source + ":" + pricingContentRevision(resolved.RequestTiers)
	return source
}
