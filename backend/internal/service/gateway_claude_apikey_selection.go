package service

import (
	"context"
	"strings"
	"time"
)

type anthropicCompatDispatchModelKey struct{}

// WithAnthropicCompatDispatchModel freezes the same channel mapping that the
// handler applies to its outbound logical body, before account-level mapping.
func WithAnthropicCompatDispatchModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, anthropicCompatDispatchModelKey{}, model)
}

// Apply account-local profile constraints before selecting a candidate. A
// legitimate request can still use another account when this profile cannot.
func (s *GatewayService) anthropicCompatCandidateEligible(ctx context.Context, account *Account, model string) bool {
	profile, err := ResolveAnthropicCompatProfile(account)
	if err != nil {
		return false
	}
	if profile == nil {
		return true
	}
	scope := compatScope(ctx)
	if scope != nil && scope.Record == nil && scope.NativeCompatible {
		return true
	}
	if dispatch, ok := ctx.Value(anthropicCompatDispatchModelKey{}).(string); ok && dispatch != "" {
		model = dispatch
	}
	model = account.GetMappedModel(model)
	if !profile.SupportsModel(model) || account.AnthropicCompatIdentitySeed() == "" {
		return false
	}
	betas := strings.Join(profile.RequiredBetas, ",")
	if scope != nil {
		betas += "," + scope.ClientBeta
	}
	if override, ok := account.HeaderOverrideValue("anthropic-beta"); ok {
		betas += "," + override
	}
	policy := s.evaluateBetaPolicy(ctx, betas, account, model)
	if policy.blockErr != nil {
		return false
	}
	for _, beta := range strings.Split(betas, ",") {
		if _, filtered := policy.filterSet[strings.TrimSpace(beta)]; filtered {
			return false
		}
	}
	return true
}

// A provenance binding is a routing constraint, not a sticky preference. Use
// the regular snapshot, health, quota, channel and slot checks for its sole owner.
func (s *GatewayService) selectAnthropicCompatBoundAccount(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) (*Account, context.Context, bool, error) {
	scope := compatScope(ctx)
	if scope == nil || scope.Record == nil {
		return nil, ctx, false, nil
	}
	if _, found := excluded[scope.Record.AccountID]; found {
		return nil, ctx, true, compatUnavailable("The account bound to signed history is temporarily unavailable")
	}
	group, resolvedID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, ctx, true, err
	}
	groupID = resolvedID
	ctx = s.withGroupContext(ctx, group)
	ctx = s.withGatewayProfitControlGate(ctx, groupID)
	platform, forced, err := s.resolvePlatform(ctx, groupID, group, model)
	if err != nil {
		return nil, ctx, true, err
	}
	accounts, mixed, err := s.listSchedulableAccounts(ctx, groupID, platform, forced)
	if err != nil {
		return nil, ctx, true, compatUnavailable("The account bound to signed history cannot be loaded")
	}
	ctx = s.withWindowCostPrefetch(ctx, accounts)
	ctx = s.withRPMPrefetch(ctx, accounts)
	gates := s.newGatewayCandidateGates(ctx, groupID, platform, mixed, model)
	for i := range accounts {
		account := &accounts[i]
		if account.ID != scope.Record.AccountID {
			continue
		}
		if err := s.guardAnthropicCompatAccount(ctx, account); err != nil {
			return nil, ctx, true, err
		}
		reason, _ := gates.candidateReason(ctx, account, true, time.Now())
		if reason != "" || s.checkChannelPricingRestriction(ctx, groupID, model) {
			return nil, ctx, true, compatUnavailable("The account bound to signed history is temporarily unavailable")
		}
		hydrated, err := s.hydrateSelectedAccount(ctx, account)
		if err != nil {
			return nil, ctx, true, compatUnavailable("The account bound to signed history cannot be loaded")
		}
		if err := s.guardAnthropicCompatAccount(ctx, hydrated); err != nil {
			return nil, ctx, true, err
		}
		return hydrated, ctx, true, nil
	}
	return nil, ctx, true, compatUnavailable("The account bound to signed history is outside the available account pool")
}

func (s *GatewayService) selectAnthropicCompatBoundWithLoad(ctx context.Context, groupID *int64, model string, excluded map[int64]struct{}) (*AccountSelectionResult, bool, error) {
	account, ctx, handled, err := s.selectAnthropicCompatBoundAccount(ctx, groupID, model, excluded)
	if !handled || err != nil {
		return nil, handled, err
	}
	if s.concurrencyService == nil {
		result, err := s.newSelectionResult(ctx, account, true, func() {}, nil)
		return result, true, err
	}
	slot, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if err != nil {
		return nil, true, compatUnavailable("The account bound to signed history has no available slot")
	}
	if slot.Acquired {
		result, err := s.newSelectionResult(ctx, account, true, slot.ReleaseFunc, nil)
		return result, true, err
	}
	cfg := s.schedulingConfig()
	result, err := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{AccountID: account.ID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting})
	return result, true, err
}
