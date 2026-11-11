package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) selectionView() schedquality.View {
	if s.selectionBoard != nil && s.selectionBoard.view != nil {
		if view := s.selectionBoard.view(); view != nil {
			return view
		}
	}
	return emptySelectionView{}
}

func (s *OpenAIGatewayService) selectionNow() time.Time {
	if s.selectionBoard != nil && s.selectionBoard.now != nil {
		return s.selectionBoard.now()
	}
	return time.Now()
}

func (s *OpenAIGatewayService) selectionRequest(ctx context.Context, req OpenAIAccountScheduleRequest, loads map[int64]*AccountLoadInfo) selectionRequest {
	return selectionRequest{ManualOnly: openAISelectionManualOnly(ctx), TokenBilled: req.UseUpstreamTokenCost, RequireCompact: req.RequireCompact, SessionHash: req.SessionHash, Loads: loads, Recent: s.recentSessionSuccess(), OAuthRateMultiplier: s.openAIOAuthSchedulingRateMultiplier(ctx)}
}

func (s *OpenAIGatewayService) sortSelectionAccounts(ctx context.Context, accounts []*Account, req OpenAIAccountScheduleRequest, loads map[int64]*AccountLoadInfo) {
	view, now := s.selectionView(), s.selectionNow()
	request := s.selectionRequest(ctx, req, loads)
	keys := make(map[int64]selectionKey, len(accounts))
	for _, account := range accounts {
		keys[account.ID] = buildSelectionKey(view, derefGroupID(req.GroupID), req.RequestedModel, account, request, now)
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		if req.RequireCompact {
			a, b := openAICompactSupportTier(accounts[i]), openAICompactSupportTier(accounts[j])
			if a != b {
				return a > b
			}
		}
		return selectionLess(keys[accounts[i].ID], keys[accounts[j].ID])
	})
}

// selectionCandidates is shared by new placement and owner migration. Compact
// support is rechecked from the DB after ordering, including stale tier zero.
func (s *OpenAIGatewayService) selectionCandidates(ctx context.Context, req OpenAIAccountScheduleRequest, members []Account) ([]*Account, openAISelectionFilterStats) {
	stats := openAISelectionFilterStats{pool: len(members)}
	candidates := make([]*Account, 0, len(members))
	for i := range members {
		a := &members[i]
		if _, excluded := req.ExcludedIDs[a.ID]; excluded {
			stats.exclude("excluded")
			continue
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, a) {
			stats.exclude("account_scheduling_threshold")
			continue
		}
		if reason := openAICompatibleAccountEligibilityFailureReason(ctx, a, req.Platform, req.RequestedModel, false, req.RequiredCapability, s); reason != "" {
			stats.exclude(reason)
			continue
		}
		if ok, reason := s.isAccountRequestCompatibleReason(ctx, a, req); !ok {
			stats.exclude(reason)
			continue
		}
		if !s.isAccountTransportCompatible(a, req.RequiredTransport) {
			stats.exclude("transport_incompatible")
			continue
		}
		candidates = append(candidates, a)
	}
	if req.Platform == PlatformGrok {
		candidatesOut := candidates[:0]
		for _, a := range candidates {
			if len(s.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*a})) > 0 {
				candidatesOut = append(candidatesOut, a)
			}
		}
		candidates = candidatesOut
	}
	return candidates, stats
}

func (s *OpenAIGatewayService) recheckSelectionAccount(ctx context.Context, req OpenAIAccountScheduleRequest, account *Account) *Account {
	if account == nil {
		return nil
	}
	if _, excluded := req.ExcludedIDs[account.ID]; excluded {
		return nil
	}
	fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, account, req.Platform, req.RequestedModel, false, req.RequiredCapability)
	if fresh == nil {
		return nil
	}
	fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
	if fresh == nil || !s.isAccountRequestCompatible(ctx, fresh, req) || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) {
		return nil
	}
	return fresh
}

func selectionLoadRequests(accounts []*Account) []AccountWithConcurrency {
	requests := make([]AccountWithConcurrency, 0, len(accounts))
	for _, a := range accounts {
		requests = append(requests, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.EffectiveLoadFactor()})
	}
	return requests
}

func (s *OpenAIGatewayService) selectionAvailable(accounts []*Account, loads map[int64]*AccountLoadInfo) []*Account {
	s.selectionFullMarks.observe(accounts, loads, s.selectionNow())
	available := make([]*Account, 0, len(accounts))
	for _, a := range accounts {
		if load := loads[a.ID]; load == nil || load.LoadRate < 100 {
			available = append(available, a)
		}
	}
	return available
}

func (s *OpenAIGatewayService) selectSessionOwner(ctx context.Context, req OpenAIAccountScheduleRequest, owner *Account, members func() ([]Account, error)) *AccountSelectionResult {
	if req.Platform != PlatformOpenAI || req.SessionHash == "" || openAINoStickyMigration(ctx) || openAISelectionExempt(ctx) || openAISelectionManualOnly(ctx) {
		return nil
	}
	cache, ok := s.cache.(OpenAIStickyRebindCache)
	if !ok {
		return nil
	}
	now, view := s.selectionNow(), s.selectionView()
	gid := derefGroupID(req.GroupID)
	migrate := schedquality.StandingOf(selectionState(view, gid, owner, req.RequestedModel), selectionAuto(view, now), now) == schedquality.StandBack
	bindings, _ := s.cache.(gatewaySessionBindingReader)
	session := selectionSession{view: s.selectionView, now: s.selectionNow, slots: s, cache: cache, cacheKey: s.openAISessionCacheKey, bindings: bindings, marks: &s.selectionFullMarks, bindingTTL: s.openAIStickyBindingTTL(req.GroupID)}
	if !migrate && !session.takeoverReady(ctx, view, gid, s.openAISessionCacheKey(req.SessionHash), owner.ID, now) {
		return nil
	}
	accounts, err := members()
	if err != nil {
		return nil
	}
	// Check membership, priority and view before hard admission or batch loads.
	possible := migrate
	for i := range accounts {
		a := &accounts[i]
		if a.Platform != req.Platform || req.RequestedModel != "" && !a.IsModelSupported(req.RequestedModel) {
			continue
		}
		if a.ID != owner.ID && a.Priority < owner.Priority && schedquality.CanTakeOver(selectionState(view, gid, a, req.RequestedModel), selectionAuto(view, now), now) && !s.selectionFullMarks.Recently(a.ID, now) {
			possible = true
			break
		}
	}
	if !possible {
		return nil
	}
	candidates, _ := s.selectionCandidates(ctx, req, accounts)
	var loads map[int64]*AccountLoadInfo
	if s.concurrencyService != nil && s.schedulingConfig().LoadBatchEnabled {
		loads, _ = s.concurrencyService.GetAccountsLoadBatchFresh(ctx, selectionLoadRequests(candidates))
	}
	candidates = s.selectionAvailable(candidates, loads)
	session.recheck = func(ctx context.Context, a *Account) *Account {
		fresh := s.recheckSelectionAccount(ctx, req, a)
		if fresh == nil {
			return nil
		}
		latest, vetoed, _ := s.SelectionAdmissionVetoLatest(ctx, fresh)
		if vetoed {
			return nil
		}
		return latest
	}
	selection := session.selectOwner(ctx, gid, req.SessionHash, req.RequestedModel, owner, candidates, s.selectionRequest(ctx, req, loads))
	if selection != nil {
		resilience.StateFrom(ctx).NoteStickyBypassReason("quality_migration")
		resilience.StateFrom(ctx).NoteStickyBypassBindingChange()
	}
	return selection
}

func (s *OpenAIGatewayService) listSelectionMembers(ctx context.Context, groupID *int64, platform string) ([]Account, error) {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.schedulerSnapshot != nil {
		accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, false)
		if err != nil {
			return accounts, err
		}
		return accounts, nil
	}
	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, platform)
	}
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	return accounts, nil
}
