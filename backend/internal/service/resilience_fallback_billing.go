package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

type billingAPIKeySnapshotKey struct{}
type servingPricingKeySnapshotKey struct{}

// RememberAvailabilityFallbackOrigin keeps the authenticated API key and its
// origin group for authorization and limit checks after a fallback move.
func RememberAvailabilityFallbackOrigin(ctx context.Context, apiKey *APIKey) {
	if apiKey == nil {
		return
	}
	resilience.StateFrom(ctx).SetFallbackOrigin(apiKey)
}

// BillingAPIKey returns the authenticated key used for authorization and
// limits. It intentionally remains the origin key when a fallback serves the
// request.
func BillingAPIKey(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil {
		return nil
	}
	if ctx != nil {
		if snapshot, ok := ctx.Value(billingAPIKeySnapshotKey{}).(*APIKey); ok && snapshot != nil && snapshot.ID == apiKey.ID {
			return snapshot
		}
	}
	st := resilience.StateFrom(ctx)
	origin, ok := st.FallbackOrigin().(*APIKey)
	if !ok || origin == nil || origin.ID != apiKey.ID || origin.GroupID == nil || *origin.GroupID != st.OriginGroupID() {
		return apiKey
	}
	return origin
}

// WithBillingAPIKeySnapshot freezes the origin authorization snapshot for a
// delayed usage task or a WebSocket turn.
func WithBillingAPIKeySnapshot(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil {
		return ctx
	}
	return context.WithValue(ctx, billingAPIKeySnapshotKey{}, apiKey)
}

// ServingPricingKey returns the key snapshot used for all price calculations.
// During fallback it follows the serving group in the request context; when a
// delayed caller still carries the authenticated key, a serving-group clone is
// created without changing the authorization identity.
func ServingPricingKey(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil || ctx == nil {
		return apiKey
	}
	if snapshot, ok := ctx.Value(servingPricingKeySnapshotKey{}).(*APIKey); ok && snapshot != nil && snapshot.ID == apiKey.ID {
		return snapshot
	}
	st := resilience.StateFrom(ctx)
	origin, ok := st.FallbackOrigin().(*APIKey)
	if !ok || origin == nil || origin.ID != apiKey.ID || origin.GroupID == nil || *origin.GroupID != st.OriginGroupID() {
		return apiKey
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if !IsGroupContextValid(group) {
		return apiKey
	}
	if apiKey.GroupID != nil && *apiKey.GroupID == group.ID && apiKey.Group == group {
		return apiKey
	}
	return CloneAPIKeyWithGroup(apiKey, group)
}

// WithServingPricingKeySnapshot freezes the serving pricing key for one
// delayed usage task or WebSocket turn.
func WithServingPricingKeySnapshot(ctx context.Context, apiKey *APIKey) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiKey == nil {
		return ctx
	}
	return context.WithValue(ctx, servingPricingKeySnapshotKey{}, apiKey)
}

// ServingPricingGroup returns the request's serving group, including the
// fallback group installed by middleware.
func ServingPricingGroup(ctx context.Context) *Group {
	if ctx == nil {
		return nil
	}
	if key, ok := ctx.Value(servingPricingKeySnapshotKey{}).(*APIKey); ok && key != nil && IsGroupContextValid(key.Group) {
		return key.Group
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if IsGroupContextValid(group) {
		return group
	}
	return nil
}

// GroupGate is the verdict of a group capability flag on a request.
type GroupGate uint8

const (
	GroupGateAllowed GroupGate = iota
	// GroupGateDenied is the origin group's permission error.
	GroupGateDenied
	// GroupGateNoAccount means the origin allows the capability but the
	// serving group does not; callers answer with a generic no-account error.
	GroupGateNoAccount
)

// FallbackGroupGate applies a group capability flag under an availability
// fallback: eligibility follows the origin key, capability the serving key.
func FallbackGroupGate(ctx context.Context, apiKey *APIKey, allows func(*APIKey) bool) GroupGate {
	if !allows(BillingAPIKey(ctx, apiKey)) {
		return GroupGateDenied
	}
	if !allows(ServingPricingKey(ctx, apiKey)) {
		return GroupGateNoAccount
	}
	return GroupGateAllowed
}

// ImageGenerationGate is FallbackGroupGate for allow_image_generation.
func ImageGenerationGate(ctx context.Context, apiKey *APIKey) GroupGate {
	return FallbackGroupGate(ctx, apiKey, func(key *APIKey) bool { return GroupAllowsImageGeneration(apiKeyGroup(key)) })
}
