package service

import (
	"context"
	"math"
	"strings"
)

// The public view is explicitly projected by the DTO and export paths.
type openAITierChargeDetails struct {
	newPricing           bool
	Version              int      `json:"version"`
	ClientPresence       string   `json:"client_presence"`
	ClientTier           string   `json:"client_tier,omitempty"`
	OutboundTier         string   `json:"outbound_tier"`
	ObservedTier         string   `json:"observed_tier,omitempty"`
	SettledTier          string   `json:"settled_tier"`
	Source               string   `json:"source,omitempty"`
	Reason               string   `json:"reason,omitempty"`
	PolicyVersion        int      `json:"policy_version"`
	OriginGroupID        int64    `json:"origin_group_id,omitempty"`
	ServingGroupID       int64    `json:"serving_group_id,omitempty"`
	BillingModel         string   `json:"billing_model,omitempty"`
	PriceSource          string   `json:"price_source,omitempty"`
	PriceRevision        string   `json:"price_revision,omitempty"`
	MultiplierSource     string   `json:"multiplier_source,omitempty"`
	TierMultiplier       *float64 `json:"tier_multiplier,omitempty"`
	FreeTier             string   `json:"free_tier,omitempty"`
	StandardActualCost   *float64 `json:"standard_actual_cost,omitempty"`
	OutboundActualCost   float64  `json:"outbound_actual_cost"`
	ActualCost           float64  `json:"actual_cost"`
	CapReason            string   `json:"cap_reason,omitempty"`
	ResponseTierEvidence []string `json:"response_tier_evidence,omitempty"`
	ResponseTierConflict bool     `json:"response_tier_conflict,omitempty"`
}

func (s *OpenAIGatewayService) tierBillingService(ctx context.Context) *BillingService {
	if p := openAITierPricesFromContext(ctx); p != nil {
		return p.billing
	}
	return s.billingService
}

func (d *OpenAITierDecision) waives(tier string) bool {
	return (tier == OpenAIFastTierPriority && d.FreeFast) || (tier == OpenAIFastTierUltrafast && d.FreeUltrafast)
}

func settleOpenAITierCharge(result *OpenAIForwardResult, account *Account, key *APIKey, resolution ServiceTierBillingResolution, cost *CostBreakdown, quote func(string) (*CostBreakdown, error)) (*openAITierChargeDetails, error) {
	if cost == nil {
		return nil, nil
	}
	d := result.TierDecision
	eligible := openAITierTextRoute(account, firstNonEmpty(result.UpstreamModel, result.Model))
	groupExplicit := key != nil && key.Group != nil && groupSupportsOpenAIFast(key.Group.Platform) && key.Group.OpenAITierPolicyVersion >= OpenAITierPolicyExplicit
	if d == nil && groupExplicit && eligible {
		g := key.Group
		d = &OpenAITierDecision{ClientPresence: "unknown", OutboundTier: resolution.Requested, PolicyVersion: g.OpenAITierPolicyVersion,
			ServingGroupID: g.ID, FastDisabled: g.OpenAIFastMode == OpenAITierModeForceOff, UltrafastDisabled: g.OpenAIUltrafastMode == OpenAITierModeForceOff,
			FreeFast: g.FreeOpenAIFast, FreeUltrafast: g.FreeOpenAIUltrafast}
	}
	source := decodePricingSource(cost.PricingSource)
	_, hasUltraPrice := source.Overrides["ultrafast_multiplier"]
	hasUltraPrice = eligible && hasUltraPrice && (resolution.Requested == OpenAIFastTierUltrafast || resolution.Billing == OpenAIFastTierUltrafast)
	explicit := d != nil && d.PolicyVersion >= OpenAITierPolicyExplicit
	legacyWaiver := groupBillsOpenAIFastAtStandard(key, account, resolution.Billing)
	if d != nil {
		legacyWaiver = account != nil && account.IsOpenAI() && d.FreeFast && resolution.Billing == OpenAIFastTierPriority
	}
	if groupExplicit && !eligible && !explicit {
		return nil, nil
	}
	if !explicit && !hasUltraPrice {
		if legacyWaiver {
			standardCost, err := quote("")
			if err != nil && !isUsagePricingUnavailableError(err) {
				return nil, err
			}
			if standardCost != nil && err == nil {
				cost.ActualCost = standardCost.ActualCost
			}
		}
		if d == nil || (resolution.Requested == "" && resolution.Observed == "") {
			return nil, nil
		}
	} else if d == nil {
		// A legacy caller may use an independently upgraded channel card.
		d = &OpenAITierDecision{ClientPresence: "unknown", OutboundTier: resolution.Requested, FreeFast: legacyWaiver}
	}
	detail := &openAITierChargeDetails{
		newPricing:           explicit || hasUltraPrice,
		Version:              1,
		ClientPresence:       d.ClientPresence,
		ClientTier:           d.ClientTier,
		OutboundTier:         resolution.Requested,
		ObservedTier:         resolution.Observed,
		SettledTier:          resolution.Billing,
		ResponseTierConflict: result.ResponseTierConflict,
		ResponseTierEvidence: result.ResponseTierEvidence,
		Source:               d.Source,
		Reason:               d.Reason,
		PolicyVersion:        d.PolicyVersion,
		OriginGroupID:        d.OriginGroupID,
		ServingGroupID:       d.ServingGroupID,
		BillingModel:         cost.billingModel,
		PriceSource:          source.Source,
		PriceRevision:        source.Revision,
		TierMultiplier:       cost.tierMultiplier,
		OutboundActualCost:   cost.ActualCost,
	}
	if cost.tierPriceSource != "" {
		detail.PriceSource, detail.PriceRevision = cost.tierPriceSource, cost.tierPriceRevision
	}
	keyName := ""
	switch resolution.Billing {
	case OpenAIFastTierPriority:
		keyName = "fast_multiplier"
	case OpenAIFastTierUltrafast:
		keyName = "ultrafast_multiplier"
	}
	if keyName != "" && (cost.BillingMode == "" || cost.BillingMode == string(BillingModeToken)) {
		detail.MultiplierSource = "model_default"
		if override, ok := source.Overrides[keyName]; ok {
			detail.MultiplierSource, _, _ = strings.Cut(override, ":")
		} else if cost.tierMultiplier == nil && cost.billingModel != "" {
			detail.MultiplierSource = "priority_unit_prices"
		}
	}
	if !explicit && !hasUltraPrice {
		if legacyWaiver {
			detail.FreeTier = resolution.Billing
		}
		return detail, nil
	}
	// Only the chosen charge changes. TotalCost, account quota and the effective unit
	// prices remain those of the settled tier. Quotes share the dispatch snapshot.
	standardCap := (resolution.Observed == OpenAIFastTierPriority && d.FastDisabled) ||
		(resolution.Observed == OpenAIFastTierUltrafast && d.UltrafastDisabled)
	if d.waives(resolution.Requested) || d.waives(resolution.Billing) || standardCap {
		standard, err := quote("")
		if err != nil {
			if isUsagePricingUnavailableError(err) && cost.ActualCost == 0 {
				return detail, nil
			}
			return nil, err
		}
		detail.StandardActualCost = &standard.ActualCost
	}
	customerPrice := func(tier string, charge float64) float64 {
		if d.waives(tier) {
			return math.Min(charge, *detail.StandardActualCost)
		}
		return charge
	}
	settledPrice := customerPrice(resolution.Billing, cost.ActualCost)
	if d.waives(resolution.Billing) {
		detail.FreeTier = resolution.Billing
	}
	outboundPrice := settledPrice
	if resolution.Downgraded {
		outbound, err := quote(resolution.Requested)
		if err != nil {
			if isUsagePricingUnavailableError(err) && cost.ActualCost == 0 {
				return detail, nil
			}
			return nil, err
		}
		outboundPrice = customerPrice(resolution.Requested, outbound.ActualCost)
		if outboundPrice < settledPrice {
			settledPrice = outboundPrice
			detail.CapReason = "outbound_quote"
			if d.waives(resolution.Requested) {
				detail.FreeTier = resolution.Requested
			}
		}
	}
	if standardCap {
		settledPrice = math.Min(settledPrice, *detail.StandardActualCost)
		detail.CapReason = "disabled_response_tier"
	}
	cost.ActualCost = settledPrice
	detail.OutboundActualCost = outboundPrice
	return detail, nil
}
