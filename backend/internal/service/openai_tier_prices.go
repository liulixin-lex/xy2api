package service

import (
	"context"
	"sync"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/timezone"
)

// Price catalogs and channel indexes are published by replacement. Retaining their
// immutable maps fixes the price of a turn, including a model named only in its response.
type openAITierPrices struct {
	at       time.Time
	billing  *BillingService
	channels *channelCache
	group    *Group
	rate     *float64
}

// The same public billing alias can route to a GPT text model or a different
// provider. Prefer the dispatched route when applying the new card override.
type openAITierTextPricingKey struct{}

func openAITierPriceCard(ctx context.Context, model string, card *ChannelModelPricing) *ChannelModelPricing {
	if card == nil || card.UltrafastMultiplier == nil {
		return card
	}
	eligible := openAITierPricesFromContext(ctx) != nil
	if !eligible {
		platform := PlatformOpenAI
		if target, ok := ResolvedTargetPlatformFromContext(ctx); ok {
			platform = target
		}
		if mapped, ok := ResolvedUpstreamModelFromContext(ctx); ok {
			model = mapped
		}
		eligible = openAITierTextRoute(&Account{Platform: platform}, model)
		if ctx != nil {
			if actual, ok := ctx.Value(openAITierTextPricingKey{}).(bool); ok {
				eligible = actual
			}
		}
	}
	if eligible {
		return card
	}
	copy := *card
	copy.UltrafastMultiplier = nil
	return &copy
}

type openAITierPriceStoreKey struct{}
type openAITierPricesKey struct{}
type openAITierPriceStore struct {
	mu         sync.Mutex
	groups     map[int64]*Group
	groupRates map[[2]int64]float64 // user ID and serving group ID
	once       sync.Once
	prices     *openAITierPrices
	err        error
}

// Fallback may visit the same group more than once. Auth snapshots are immutable
// and replaced on invalidation. Keep the first one for this request or turn.
func freezeOpenAITierGroup(ctx context.Context, group *Group) *Group {
	if ctx == nil || group == nil {
		return group
	}
	store, _ := ctx.Value(openAITierPriceStoreKey{}).(*openAITierPriceStore)
	if store == nil {
		return group
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior := store.groups[group.ID]; prior != nil {
		return prior
	}
	if store.groups == nil {
		store.groups = make(map[int64]*Group)
	}
	store.groups[group.ID] = group
	return group
}

func openAITierPricesFromContext(ctx context.Context) *openAITierPrices {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(openAITierPricesKey{}).(*openAITierPrices)
	return p
}

func (s *OpenAIGatewayService) tierPriceSnapshot(ctx context.Context) (*openAITierPrices, error) {
	if p := openAITierPricesFromContext(ctx); p != nil {
		return p, nil
	}
	store, _ := ctx.Value(openAITierPriceStoreKey{}).(*openAITierPriceStore)
	if store == nil {
		store = new(openAITierPriceStore)
	}
	store.once.Do(func() {
		at, ok := openAIPricingAtFromContext(ctx)
		if !ok {
			at = timezone.Now()
		}
		b := s.billingService
		if b == nil {
			return
		}
		snapshot := &BillingService{cfg: b.cfg, fallbackPrices: b.fallbackPrices}
		if catalog := b.pricingService; catalog != nil {
			catalog.mu.RLock()
			snapshot.pricingService = &PricingService{cfg: catalog.cfg, pricingData: catalog.pricingData, pricingOrigins: catalog.pricingOrigins, localHash: catalog.localHash}
			catalog.mu.RUnlock()
		}
		p := &openAITierPrices{billing: snapshot, at: at}
		if s.resolver != nil && s.resolver.channelService != nil {
			p.channels, store.err = s.resolver.channelService.loadCache(ctx)
		}
		store.prices = p
	})
	return store.prices, store.err
}

func (s *OpenAIGatewayService) tierGroupRate(ctx context.Context, group *Group) *float64 {
	uid := openAIFastPolicyUserID(ctx)
	if uid <= 0 || group == nil {
		return nil
	}
	key := [2]int64{uid, group.ID}
	store, _ := ctx.Value(openAITierPriceStoreKey{}).(*openAITierPriceStore)
	if store != nil {
		store.mu.Lock()
		defer store.mu.Unlock()
		if rate, ok := store.groupRates[key]; ok {
			return &rate
		}
	}
	rate := s.ResolveUserGroupRateMultiplier(ctx, uid, group.ID, group.RateMultiplier)
	if store != nil {
		if store.groupRates == nil {
			store.groupRates = make(map[[2]int64]float64)
		}
		store.groupRates[key] = rate
	}
	return &rate
}

func (s *OpenAIGatewayService) prepareOpenAITierDispatch(ctx context.Context, account *Account, model string, decision *OpenAITierDecision) error {
	if decision == nil || !openAITierTextRoute(account, model) {
		return nil
	}
	prices, err := s.tierPriceSnapshot(ctx)
	if err != nil && decision.PolicyVersion >= OpenAITierPolicyExplicit {
		return openAITierUnavailableError("tier_pricing_unavailable")
	}
	if prices == nil || err != nil {
		return nil
	}
	p := *prices
	_, p.group = openAITierGroups(ctx)
	p.rate = s.tierGroupRate(ctx, p.group)
	decision.prices = &p
	if reason := s.openAITierCandidateProfitReason(ctx, account, model, decision); reason != "" {
		return openAITierUnavailableError(reason)
	}
	return nil
}
