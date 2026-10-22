package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// xy2api resilience: an availability fallback group only routes a request.
// The user's price, permissions and limits are those the group the request
// was made for would have applied had it served the request; the fallback
// group decides who serves it and how. Costs on the serving account stay with
// that account.

// RememberAvailabilityFallbackOrigin keeps the API key as authenticated, with
// its own group, for a request moved to an availability fallback group.
func RememberAvailabilityFallbackOrigin(ctx context.Context, apiKey *APIKey) {
	if apiKey == nil {
		return
	}
	resilience.StateFrom(ctx).SetFallbackOrigin(apiKey)
}

// BillingAPIKey returns the key whose group prices, permits and limits the
// request: the key as authenticated when an availability fallback group
// serves it, otherwise apiKey itself.
func BillingAPIKey(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil {
		return nil
	}
	st := resilience.StateFrom(ctx)
	origin, ok := st.FallbackOrigin().(*APIKey)
	if !ok || origin == nil || origin.ID != apiKey.ID || origin.GroupID == nil || *origin.GroupID != st.OriginGroupID() {
		return apiKey
	}
	return origin
}

// GroupGate is the verdict of a group capability flag on a request.
type GroupGate uint8

const (
	GroupGateAllowed GroupGate = iota
	// GroupGateDenied: the key's own group refuses; answered as the
	// permission error that group gives when it serves the request.
	GroupGateDenied
	// GroupGateNoAccount: the key's own group allows it but the availability
	// fallback group serving the request does not; answered as no available
	// account, without naming the fallback group.
	GroupGateNoAccount
)

// FallbackGroupGate applies a group capability flag under an availability
// fallback (PM decision P2-3): eligibility follows the group the key belongs
// to, capability the group that serves the request.
func FallbackGroupGate(ctx context.Context, apiKey *APIKey, allows func(*APIKey) bool) GroupGate {
	if !allows(BillingAPIKey(ctx, apiKey)) {
		return GroupGateDenied
	}
	if !allows(apiKey) {
		return GroupGateNoAccount
	}
	return GroupGateAllowed
}

// ImageGenerationGate is FallbackGroupGate for allow_image_generation.
func ImageGenerationGate(ctx context.Context, apiKey *APIKey) GroupGate {
	return FallbackGroupGate(ctx, apiKey, func(key *APIKey) bool { return GroupAllowsImageGeneration(apiKeyGroup(key)) })
}

// fallbackBillingGroup is the group whose price the user pays when the
// request was moved to an availability fallback group, or nil.
func fallbackBillingGroup(ctx context.Context) *Group {
	st := resilience.StateFrom(ctx)
	origin, ok := st.FallbackOrigin().(*APIKey)
	if !ok || origin == nil || origin.GroupID == nil || *origin.GroupID != st.OriginGroupID() || !IsGroupContextValid(origin.Group) {
		return nil
	}
	return origin.Group
}

// billingChannelFields returns the channel fields that decide the billing
// model. Under an availability fallback they are what the origin group's
// channel resolves for the requested model (PM decision Q9): a requested or
// channel-mapped billing model follows the origin group's channel, and with
// no origin mapping the origin group's default source applies. The serving
// group's mapping only routed the request; its channel stays on the usage
// row for the account-side records.
func billingChannelFields(ctx context.Context, channels *ChannelService, served, billing *APIKey, fields ChannelUsageFields) ChannelUsageFields {
	if billing == served || billing == nil || billing.GroupID == nil || fields.OriginalModel == "" {
		return fields
	}
	mapping := ChannelMappingResult{MappedModel: fields.OriginalModel}
	if channels != nil {
		mapping = channels.ResolveChannelMapping(ctx, *billing.GroupID, fields.OriginalModel)
	}
	out := fields
	out.BillingModelSource = mapping.BillingModelSource
	out.ChannelMappedModel = fields.OriginalModel
	if mapping.Mapped {
		out.ChannelMappedModel = mapping.MappedModel
	}
	return out
}

// noteFallbackForcedFast is called where a group's forced Fast rewrites a
// request to priority. When the request is under an availability fallback,
// its client did not ask for priority and its own group does not force
// Fast, only the fallback made it priority: the user is charged the standard
// tier and the platform bears the difference (PM ruling on Q8).
func noteFallbackForcedFast(ctx context.Context, account *Account, clientTier string) {
	st := resilience.StateFrom(ctx)
	origin, ok := st.FallbackOrigin().(*APIKey)
	if !ok || origin == nil {
		return
	}
	// A WebSocket connection keeps one state across turns: each forced
	// rewrite records its own outcome.
	switch normalizeBillingServiceTier(clientTier) {
	case OpenAIFastTierPriority, "fast":
		st.SetFallbackForcedFast(false)
	default:
		st.SetFallbackForcedFast(!groupForcesOpenAIFast(origin.Group, account))
	}
}

// fallbackForcedFastChargesStandard reports a priority request whose
// priority only the serving fallback group's forced Fast caused.
func fallbackForcedFastChargesStandard(ctx context.Context, serviceTier string) bool {
	switch normalizeBillingServiceTier(serviceTier) {
	case OpenAIFastTierPriority, "fast":
		return resilience.StateFrom(ctx).FallbackForcedFast()
	}
	return false
}
