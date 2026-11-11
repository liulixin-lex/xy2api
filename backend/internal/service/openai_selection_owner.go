package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, bool, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.cache == nil {
		return nil, false, nil
	}

	accountID := req.StickyAccountID
	noteBypass := func(reason string) {
		resilience.StateFrom(ctx).NoteStickyBypassReason(reason)
	}
	clearBinding := func(reason string) {
		noteBypass(reason)
		if !req.PreserveStickyBinding {
			_ = s.deleteStickySessionAccountIDIfOwner(ctx, req.GroupID, sessionHash, accountID)
		}
	}
	if accountID <= 0 {
		var err error
		accountID, err = s.getStickySessionAccountID(ctx, req.GroupID, sessionHash)
		if err != nil || accountID <= 0 {
			if err != nil {
				noteBypass("owner_lookup_failed")
			}
			return nil, false, nil
		}
	}
	if accountID <= 0 {
		return nil, false, nil
	}
	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		clearBinding("owner_unavailable")
		return nil, false, nil
	}
	if _, excluded := req.ExcludedIDs[accountID]; excluded {
		noteBypass("owner_excluded")
		return nil, false, nil
	}
	// xy2api resilience: a soft-penalised owner keeps the binding while this
	// request is served elsewhere (design §4.2).
	if s.resilienceKeepOpenAISoftOwner(ctx, account, req.Platform, req.RequestedModel) {
		noteBypass("soft_owner_preserved")
		return nil, false, nil
	}
	if shouldClearStickySession(account, req.RequestedModel) {
		clearBinding("owner_should_clear")
		return nil, false, nil
	}
	if account.Platform != NormalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible() || !account.IsSchedulable() {
		clearBinding("owner_not_schedulable")
		return nil, false, nil
	}
	if compatible, reason := s.isAccountRequestCompatibleReason(ctx, account, req); !compatible {
		noteBypass(reason)
		return nil, false, nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		clearBinding("transport_incompatible")
		return nil, false, nil
	}
	account = s.recheckSelectedOpenAIAccountFromDB(ctx, account, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
	if account == nil {
		clearBinding("owner_recheck_failed")
		return nil, false, nil
	}
	if !s.openAIAccountMatchesSchedulingGroup(account, req.GroupID) {
		clearBinding("owner_group_mismatch")
		return nil, false, nil
	}
	if compatible, reason := s.isAccountRequestCompatibleReason(ctx, account, req); !compatible {
		clearBinding(reason)
		return nil, false, nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		clearBinding("transport_incompatible")
		return nil, false, nil
	}
	// Free-tier soft gate: sticky session must not pin an over-quota free OAuth account.
	// Admin QueryQuota / import probes do not use this path.
	if account != nil && len(s.filterGrokFreeQuotaAccountsForOpenAI(ctx, []Account{*account})) == 0 {
		clearBinding("grok_free_quota_soft_gate")
		return nil, false, nil
	}
	// Team+model cool: sticky must not pin a sibling under the same team 429 window.
	now := time.Now()
	upstreamModel := canonicalOpenAIAccountSchedulingModel(account, req.RequestedModel)
	if account != nil && isGrokTeamModelRateLimited(account, upstreamModel, now) {
		clearBinding("grok_team_model_rate_limited")
		return nil, false, nil
	}
	if account != nil && isGrokModelQuotaBlocked(account.ID, upstreamModel, now) {
		clearBinding("grok_model_quota_blocked")
		return nil, false, nil
	}
	result, acquireErr := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr != nil && req.DisableStickyEscape {
		noteBypass("sticky_capacity_error")
		return nil, false, acquireErr
	}
	if acquireErr == nil && result != nil && result.Acquired {
		if !req.PreserveStickyBinding {
			_ = s.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.openAIWSSessionStickyTTL(), accountID)
		}
		return attachSelectionProfitGate(ctx, &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}), false, nil
	}

	if NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		if s.concurrencyService != nil {
			cfg := s.schedulingConfig()
			return attachSelectionProfitGate(ctx, &AccountSelectionResult{
				Account: account,
				WaitPlan: &AccountWaitPlan{
					AccountID: account.ID, MaxConcurrency: account.Concurrency,
					Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting,
				},
			}), false, nil
		}
		return nil, false, nil
	}

	if s.concurrencyService != nil {
		noteBypass("concurrency_full")
		if acquireErr != nil {
			req.SkipStickyCapacityRetry = true
		}
		selection, err := s.selectOpenAIStickyCapacity(ctx, account, req)
		return selection, false, err
	}
	return nil, false, nil
}

// selectLegacyAccountByPreviousResponse resolves the protocol owner and reuses
// the shared admission checks before returning its slot or wait plan.
func (s *OpenAIGatewayService) selectLegacyAccountByPreviousResponse(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
) (*AccountSelectionResult, bool, error) {
	if strings.TrimSpace(previousResponseID) == "" || platform != PlatformOpenAI {
		return nil, false, nil
	}
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		return nil, false, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}
	selection, err := s.selectAccountByPreviousResponseIDForCapability(withOpenAISelectionExempt(ctx), groupID, previousResponseID, requestedModel, excludedIDs, requiredCapability, requireCompact)
	if err != nil {
		return nil, false, err
	}
	if selection == nil || selection.Account == nil {
		return nil, false, nil
	}
	account := selection.Account
	scheduler := s
	compatible, _ := scheduler.isAccountRequestCompatibleReason(withOpenAISelectionExempt(ctx), account, OpenAIAccountScheduleRequest{
		GroupID:                 groupID,
		Platform:                platform,
		RequestedModel:          requestedModel,
		RequiredTransport:       requiredTransport,
		RequiredCapability:      requiredCapability,
		RequiredImageCapability: requiredImageCapability,
		RequireCompact:          requireCompact,
		ExcludedIDs:             excludedIDs,
		RequirePrivacySet:       s.openAIGroupRequiresPrivacySet(ctx, groupID),
	})
	if !s.openAIAccountMatchesSchedulingGroup(account, groupID) || !compatible || !scheduler.isAccountTransportCompatible(account, requiredTransport) {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, false, nil
	}
	if sessionHash != "" {
		_ = s.bindOpenAIStickySessionDuringSelection(ctx, groupID, sessionHash, account.ID)
	}
	return selection, true, nil
}
