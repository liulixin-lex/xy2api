package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// OpenAISelectionBoard shares the gateway's admission and the selection chain's
// view and full marks. It never acquires slots or records full observations.
type OpenAISelectionBoard struct {
	service *OpenAIGatewayService
	view    func() schedquality.View
	now     func() time.Time
	marks   *fullMarks
}

var _ SchedSelectionBoard = (*OpenAISelectionBoard)(nil)

func NewOpenAISelectionBoard(service *OpenAIGatewayService, view func() schedquality.View) *OpenAISelectionBoard {
	return &OpenAISelectionBoard{service: service, view: view, now: time.Now, marks: &fullMarks{}}
}

func (b *OpenAISelectionBoard) Board(ctx context.Context, groupID int64, model string) ([]SchedBoardRow, error) {
	s := b.service
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
	if err != nil {
		return nil, err
	}
	ctx = resilience.WithCapabilityRequest(ctx, "responses", true)
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, &groupID)
	ctx = s.withOpenAIProfitControlGate(ctx, &groupID)
	var view schedquality.View = emptySelectionView{}
	if b.view != nil {
		if current := b.view(); current != nil {
			view = current
		}
	}
	now := b.now()
	req := OpenAIAccountScheduleRequest{GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: model, RequiredTransport: OpenAIUpstreamTransportHTTPSSE, RequirePrivacySet: s.openAIGroupRequiresPrivacySet(ctx, &groupID)}
	loads := make(map[int64]*AccountLoadInfo)
	if s.concurrencyService != nil {
		loadReq := make([]AccountWithConcurrency, 0, len(accounts))
		for i := range accounts {
			loadReq = append(loadReq, AccountWithConcurrency{ID: accounts[i].ID, MaxConcurrency: accounts[i].EffectiveLoadFactor()})
		}
		if current, err := s.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadReq); err == nil {
			loads = current
		}
	}
	multiplier := s.openAIOAuthSchedulingRateMultiplier(ctx)
	selectionReq := selectionRequest{TokenBilled: true, Loads: loads, OAuthRateMultiplier: multiplier}
	checker := &defaultOpenAIAccountScheduler{service: s}
	channelRestricted := s.checkChannelPricingRestriction(ctx, &groupID, model)
	thresholds := s.settingService.GetAccountSchedulingThresholds(ctx)
	type keyedRow struct {
		row SchedBoardRow
		key selectionKey
	}
	ordered := make([]keyedRow, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		reason := openAICompatibleAccountEligibilityFailureReason(ctx, account, req.Platform, model, false, req.RequiredCapability, s)
		ok := reason == ""
		threshold := EvaluateAccountSchedulingThreshold(account, thresholds, now)
		if ok && threshold.ShouldPause {
			ok, reason = false, "account_scheduling_threshold"
		}
		if ok {
			// The shared check includes per-account upstream channel restrictions.
			ok, reason = checker.isAccountRequestCompatibleReason(ctx, account, req)
		}
		if channelRestricted {
			ok, reason = false, "channel_pricing_restricted"
		}
		key := buildSelectionKey(view, groupID, model, account, selectionReq, now)
		row := SchedBoardRow{AccountID: account.ID, Eligible: ok, Reject: reason, Standing: schedquality.Standing(key.Standing), Priority: key.Priority, Rate: key.Rate, Concurrency: account.Concurrency}
		if !ok {
			if reason == "account_scheduling_threshold" {
				row.RejectUntil = threshold.Until
			} else {
				row.RejectUntil = selectionRejectUntil(ctx, s, account, model, reason, now)
			}
		}
		currentlyFull := false
		if load := loads[account.ID]; load != nil {
			row.InFlight = load.CurrentConcurrency
			currentlyFull = account.Concurrency > 0 && load.CurrentConcurrency >= account.Concurrency
		}
		if at := b.marks.last(account.ID); !at.IsZero() {
			row.RecentlyFullSeconds = max(0, int(now.Sub(at)/time.Second))
		}
		idle, takeover := view.TakeoverIdle(groupID)
		row.CanTakeOver = ok && takeover && idle >= 0 && schedquality.CanTakeOver(selectionState(view, groupID, account, model), selectionAuto(view, now), now) && !currentlyFull && !b.marks.Recently(account.ID, now)
		ordered = append(ordered, keyedRow{row, key})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].row.Eligible != ordered[j].row.Eligible {
			return ordered[i].row.Eligible
		}
		if !ordered[i].row.Eligible {
			return ordered[i].row.AccountID < ordered[j].row.AccountID
		}
		return selectionLess(ordered[i].key, ordered[j].key)
	})
	rows := make([]SchedBoardRow, 0, len(ordered))
	for i, item := range ordered {
		if item.row.Eligible {
			item.row.Rank = i + 1
		}
		rows = append(rows, item.row)
	}
	return rows, nil
}

// selectionRejectUntil describes an existing veto; admission stays in the
// scheduler's checks. Match their first failure rather than the latest deadline.
func selectionRejectUntil(ctx context.Context, s *OpenAIGatewayService, a *Account, model, reason string, now time.Time) *time.Time {
	var until time.Time
	if strings.HasPrefix(reason, "quota_auto_pause_") {
		if at, ok := openAICodexWindowResetAt(a.Extra, strings.TrimPrefix(reason, "quota_auto_pause_")); ok {
			until = at
		}
	}
	switch reason {
	case "not_schedulable":
		if !a.IsActive() || !a.Schedulable || (a.AutoPauseOnExpired && a.ExpiresAt != nil && !now.Before(*a.ExpiresAt)) {
			return nil
		}
		for _, at := range []*time.Time{a.OverloadUntil, a.RateLimitResetAt, a.TempUnschedulableUntil} {
			if at != nil && now.Before(*at) {
				until = *at
				break
			}
		}
	case "model_rate_limited":
		if iqGateBlocksRequest(a, model, false, s.openAICodexTicketOutboundModel(a, model, false)) {
			return nil
		}
		if remaining := a.GetRateLimitRemainingTimeWithContext(ctx, model); remaining > 0 {
			until = now.Add(remaining)
		}
	case "runtime_blocked":
		// IQ and ticket gates have no timed expiry. Same-site precedes the model breaker.
		outbound := s.openAICodexTicketOutboundModel(a, model, false)
		if iqGateBlocksRequest(a, model, false, outbound) || s.openAICodexTicketBlocksAccount(a, outbound) {
			return nil
		}
		if s.resilience != nil {
			until = s.resilience.sameSiteHold.until(a, now)
		}
		if until.IsZero() {
			until = s.openAIModelBreakerEnd(a, model, now)
		}
	case "proxy_stream_quarantined":
		if a.ProxyID != nil {
			circuit := s.getOpenAIProxyStreamCircuit()
			circuit.mu.Lock()
			until = circuit.entries[*a.ProxyID].blockedUntil
			circuit.mu.Unlock()
		}
	}
	if until.IsZero() || !now.Before(until) {
		return nil
	}
	return &until
}
