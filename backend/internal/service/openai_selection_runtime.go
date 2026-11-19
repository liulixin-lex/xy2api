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
	request := selectionRequest{Session: req.SessionHash, ManualOnly: openAISelectionManualOnly(ctx), TokenBilled: req.UseUpstreamTokenCost, RequireCompact: req.RequireCompact, Loads: loads, OAuthRateMultiplier: s.openAIOAuthSchedulingRateMultiplier(ctx)}
	trial, _ := ctx.Value(selectionTrialContextKey{}).(*selectionTrial)
	if trial == nil {
		trial = s.newSelectionTrial(ctx, req, req.StickyAccountID)
	}
	request.Owner = trial.owner
	request.Borrow = trial.borrowing(ctx)
	request.Room = func(id int64, model string) bool { return trial.room(ctx, id, model) }
	return request
}

func (s *OpenAIGatewayService) sortSelectionAccounts(ctx context.Context, accounts []*Account, req OpenAIAccountScheduleRequest, loads map[int64]*AccountLoadInfo) {
	view, now := s.selectionView(), s.selectionNow()
	request := s.selectionRequest(ctx, req, loads)
	keys := make(map[int64]selectionKey, len(accounts))
	for _, account := range accounts {
		keys[account.ID] = buildSelectionKey(view, derefGroupID(req.GroupID), req.RequestedModel, account, request, now)
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		// Compact capability routing predates quality ranking and applies only
		// to RequireCompact: confirmed support precedes unknown, then the quality key.
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
			observeSelectionFailure(ctx, a.ID, "excluded", nil)
			continue
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, a) {
			stats.exclude("account_scheduling_threshold")
			observeSelectionFailure(ctx, a.ID, "account_scheduling_threshold", nil)
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
			observeSelectionFailure(ctx, a.ID, "transport_incompatible", nil)
			continue
		}
		observeSelectionReady(ctx, a.ID)
		candidates = append(candidates, a)
	}
	if req.Platform == PlatformGrok {
		candidatesOut := candidates[:0]
		for _, a := range candidates {
			if len(s.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*a})) > 0 {
				candidatesOut = append(candidatesOut, a)
			} else {
				stats.exclude("quota_auto_pause")
				observeSelectionFailure(ctx, a.ID, "quota_auto_pause", nil)
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
	var fresh *Account
	if s.accountRepo != nil {
		var err error
		fresh, err = s.accountRepo.GetByID(ctx, account.ID)
		if err != nil || fresh == nil {
			observeSelectionFailure(ctx, account.ID, string(SelectionReasonDependencyError), err)
			return nil
		}
	} else {
		fresh = s.resolveFreshSchedulableOpenAIAccount(ctx, account, req.Platform, req.RequestedModel, false, req.RequiredCapability)
	}
	fresh, reason := s.recheckSelectionAdmission(ctx, req, fresh)
	if reason != "" {
		return nil
	}
	return fresh
}

// recheckSelectionAdmission is the shared normal-selection and post-slot gate.
// New eligibility conditions belong here so queued requests see them as well.
func (s *OpenAIGatewayService) recheckSelectionAdmission(ctx context.Context, req OpenAIAccountScheduleRequest, latest *Account) (*Account, string) {
	if latest == nil {
		return nil, "account_nil"
	}
	if latest.IsGrok() && len(s.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*latest})) == 0 {
		return latest, "quota_auto_pause"
	}
	if _, excluded := req.ExcludedIDs[latest.ID]; excluded {
		return latest, "excluded"
	}
	if resilienceLastResortGate(ctx) {
		// Re-evaluate the current penalty; rescue may bypass soft restrictions
		// but must never revive a newly disabled or hard-paused account.
		now := time.Now()
		if tier, _, _ := s.resilienceOpenAIPenaltyOf(latest, req.RequestedModel, now); tier == resilience.TierHard {
			return latest, "not_schedulable"
		}
		latest = withoutSoftPenalties(*latest, now)
	}
	if s.recheckLatestOpenAIAccount(ctx, latest, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability) == nil {
		return latest, "selection_ineligible"
	}
	if ok, reason := s.isAccountRequestCompatibleReason(ctx, latest, req); !ok {
		return latest, reason
	}
	if !s.isAccountTransportCompatible(latest, req.RequiredTransport) {
		return latest, "transport_incompatible"
	}
	return latest, ""
}

func selectionLoadRequests(accounts []*Account) []AccountWithConcurrency {
	requests := make([]AccountWithConcurrency, 0, len(accounts))
	for _, a := range accounts {
		requests = append(requests, AccountWithConcurrency{ID: a.ID, MaxConcurrency: a.EffectiveLoadFactor()})
	}
	return requests
}

func (s *OpenAIGatewayService) selectionAvailable(ctx context.Context, accounts []*Account, loads map[int64]*AccountLoadInfo) []*Account {
	s.selectionFullMarks.observe(accounts, loads, s.selectionNow())
	available := make([]*Account, 0, len(accounts))
	for _, a := range accounts {
		if load := loads[a.ID]; load == nil || load.LoadRate < 100 {
			available = append(available, a)
		} else {
			observeSelectionFailure(ctx, a.ID, "concurrency_full", nil)
		}
	}
	return available
}

// The protocol guard precedes membership and Redis reads. Report only the
// owner and configured policy; neither a target nor an idle TTL was observed.
func (s *OpenAIGatewayService) logPinnedSwitchVeto(ctx context.Context, req OpenAIAccountScheduleRequest, owner *Account) {
	view, now := s.selectionView(), s.selectionNow()
	if owner == nil || !selectionLive(view, now) {
		return
	}
	group := derefGroupID(req.GroupID)
	change := &openAIStickyChange{groupID: group, model: req.RequestedModel, kind: "migrate", expected: StickySessionBinding{AccountID: owner.ID},
		fromPriority: new(owner.Priority), voluntary: &voluntarySwitchEvidence{sessionOn: resilience.Current().SessionOn()}}
	if !selectionMigrateOwner(view, group, owner, req.RequestedModel, req.SessionHash, now) {
		idle, enabled := view.TakeoverIdle(group)
		if !enabled || idle < 0 {
			return
		}
		change.kind = "takeover"
		change.voluntary.idleSetting = new(int64(idle / time.Second))
	}
	change.logVeto(ctx, nil, "protocol_ineligible")
}

func (s *OpenAIGatewayService) selectSessionOwner(ctx context.Context, req OpenAIAccountScheduleRequest, owner *Account, members func() ([]Account, error)) *AccountSelectionResult {
	if req.Platform != PlatformOpenAI || req.SessionHash == "" || openAISelectionExempt(ctx) || openAISelectionManualOnly(ctx) {
		return nil
	}
	if openAINoStickyMigration(ctx) {
		s.logPinnedSwitchVeto(ctx, req, owner)
		return nil
	}
	cache, ok := s.cache.(OpenAIStickyVersionedCache)
	if !ok || s.resilience == nil {
		return nil
	}
	now, view := s.selectionNow(), s.selectionView()
	gid := derefGroupID(req.GroupID)
	migrate := selectionMigrateOwner(view, gid, owner, req.RequestedModel, req.SessionHash, now)
	bindings, _ := s.cache.(gatewaySessionBindingReader)
	session := selectionSession{qualityOwnsSticky: s.firstOutputQualityOwnsSticky, view: s.selectionView, now: s.selectionNow, slots: s, cache: cache, store: s.resilience.store, cacheKey: s.openAISessionCacheKey, bindings: bindings, marks: &s.selectionFullMarks, bindingTTL: s.openAIStickyBindingTTL(req.GroupID), resetAt: s.resilience.sessionResetAt, leases: s.selectionTrialLeases()}
	bindingKey := s.openAISessionCacheKey(req.SessionHash)
	if pending := pendingOpenAIStickyChange(ctx); pending != nil && pending.groupID == gid && pending.session == req.SessionHash {
		bindingKey = pending.key
	}
	if !migrate && !session.takeoverReady(ctx, view, gid, bindingKey, owner.ID, now, nil) {
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
		if a.ID != owner.ID && a.Priority < owner.Priority && (selectionState(view, gid, a, req.RequestedModel).Placement(selectionAuto(view, gid, now), now) == schedquality.PlaceFront) && !s.selectionFullMarks.Recently(a.ID, now) {
			possible = true
			break
		}
	}
	if !possible {
		return nil
	}
	// Reuse the ordinary pass's exclusion facts only for veto attribution.
	if excluded, _ := ctx.Value(firstOutputExcludedKey{}).(map[int64]struct{}); len(excluded) != 0 {
		for i := range accounts {
			a := &accounts[i]
			if _, hit := excluded[a.ID]; hit && a.Platform == req.Platform && (req.RequestedModel == "" || a.IsModelSupported(req.RequestedModel)) {
				session.excludedMembers = append(session.excludedMembers, a)
			}
		}
	}
	candidates, _ := s.selectionCandidates(ctx, req, accounts)
	var loads map[int64]*AccountLoadInfo
	if s.concurrencyService != nil && s.schedulingConfig().LoadBatchEnabled {
		loads, _ = s.concurrencyService.GetAccountsLoadBatchFresh(ctx, selectionLoadRequests(candidates))
	}
	candidates = s.selectionAvailable(ctx, candidates, loads)
	session.recheck = func(ctx context.Context, a *Account) *Account {
		fresh := s.recheckSelectionAccount(ctx, req, a)
		if fresh == nil {
			return nil
		}
		latest, vetoed, _ := s.SelectionAdmissionVeto(ctx, fresh)
		if vetoed {
			return nil
		}
		return latest
	}
	selection := session.selectOwner(ctx, gid, req.SessionHash, req.RequestedModel, owner, candidates, s.selectionRequest(ctx, req, loads))
	return selection
}

func (s *OpenAIGatewayService) listSelectionMembers(ctx context.Context, groupID *int64, platform string) ([]Account, error) {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.schedulerSnapshot != nil {
		accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, false)
		if err != nil {
			observeSelectionFailure(ctx, 0, "dependency_error", err)
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
		observeSelectionFailure(ctx, 0, "dependency_error", err)
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	return accounts, nil
}
