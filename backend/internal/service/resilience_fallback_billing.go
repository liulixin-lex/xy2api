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

// fallbackForcedFastKey carries a turn's flag fixed by FreezeFallbackForcedFast.
type fallbackForcedFastKey struct{}

// FreezeFallbackForcedFast fixes the flag noteFallbackForcedFast recorded for
// the current WebSocket turn when that turn's usage task is created. The
// connection's request state follows the latest turn, so by the time the
// asynchronous task runs, the next response.create may have rewritten it.
// The returned function applies the fixed value to the task's context.
func FreezeFallbackForcedFast(ctx context.Context) func(context.Context) context.Context {
	st := resilience.StateFrom(ctx)
	if origin, ok := st.FallbackOrigin().(*APIKey); !ok || origin == nil {
		return func(taskCtx context.Context) context.Context { return taskCtx }
	}
	forced := st.FallbackForcedFast()
	return func(taskCtx context.Context) context.Context {
		return context.WithValue(taskCtx, fallbackForcedFastKey{}, forced)
	}
}

// fallbackForcedFastChargesStandard reports a priority request whose
// priority only the serving fallback group's forced Fast caused. A value
// fixed for an asynchronous turn record wins over the connection's state.
func fallbackForcedFastChargesStandard(ctx context.Context, serviceTier string) bool {
	switch normalizeBillingServiceTier(serviceTier) {
	case OpenAIFastTierPriority, "fast":
		if forced, ok := ctx.Value(fallbackForcedFastKey{}).(bool); ok {
			return forced
		}
		return resilience.StateFrom(ctx).FallbackForcedFast()
	}
	return false
}
