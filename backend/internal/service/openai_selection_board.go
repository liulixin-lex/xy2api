package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// OpenAISelectionBoard uses the gateway's admission, view and ordering.
// It never acquires slots or records full observations.
type OpenAISelectionBoard struct {
	service *OpenAIGatewayService
	leases  SchedTrialLeases
	view    func() schedquality.View
	now     func() time.Time
}

var _ SchedSelectionBoard = (*OpenAISelectionBoard)(nil)

func NewOpenAISelectionBoard(service *OpenAIGatewayService, view func() schedquality.View, leases ...SchedTrialLeases) *OpenAISelectionBoard {
	board := &OpenAISelectionBoard{service: service, view: view, now: time.Now}
	if len(leases) > 0 {
		board.leases = leases[0]
	}
	if service != nil {
		service.selectionBoard = board
		if service.resilience != nil {
			service.resilience.sessionResetAt = func(accountID int64) time.Time { return service.selectionView().ResetAt(accountID) }
		}
	}
	return board
}

func (b *OpenAISelectionBoard) Board(ctx context.Context, groupID int64, model string) ([]SchedBoardRow, *SchedAdminSupply, error) {
	s := b.service
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, "", "", "", "", groupID, "")
	if err != nil {
		return nil, nil, err
	}
	// Preview a default Responses request against the selected group's policy,
	// independent of an administrator's serving/origin group snapshots.
	group := ServingPricingGroup(ctx)
	if s.schedulerSnapshot != nil {
		group, err = s.schedulerSnapshot.GetGroupByIDLite(ctx, groupID)
		if err != nil {
			return nil, nil, err
		}
		if group == nil {
			return nil, nil, ErrGroupNotFound
		}
	}
	if group != nil {
		if group.ID != groupID {
			return nil, nil, ErrGroupNotFound
		}
		ctx = context.WithValue(ctx, ctxkey.Group, group)
		key := &APIKey{GroupID: &groupID, Group: group}
		ctx = WithBillingAPIKeySnapshot(ctx, key)
		ctx = WithServingPricingKeySnapshot(ctx, key)
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	ctx = WithOpenAITierInput(ctx, body, APIProtocolResponses, "")
	ctx = resilience.WithCapabilityRequest(ctx, "responses", true)
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, &groupID)
	ctx, _ = s.WithOpenAIRequestPricingContext(ctx, &groupID)
	var view schedquality.View = emptySelectionView{}
	if b.view != nil {
		if current := b.view(); current != nil {
			view = current
		}
	}
	now := b.now()
	supply := &SchedAdminSupply{At: schedAdminStamp(now)}
	// Preview the same endpoint capability used for ordinary text Responses requests.
	req := OpenAIAccountScheduleRequest{
		GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: model,
		RequiredCapability: OpenAIEndpointCapabilityChatCompletions,
		RequiredTransport:  OpenAIUpstreamTransportHTTPSSE,
		RequirePrivacySet:  s.openAIGroupRequiresPrivacySet(ctx, &groupID),
	}
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
	selectionReq := s.selectionRequest(ctx, req, loads)
	// A dashboard preview represents a new session. Dispatch-time Room refreshes
	// the lease key and reports full seats. Preview only counts active members.
	selectionReq.Room = func(id int64, attemptModel string) bool {
		if b.leases == nil {
			return false
		}
		room, err := b.leases.PeekRoom(ctx, id, attemptModel)
		return err == nil && room
	}
	selectionReq.TokenBilled, selectionReq.OAuthRateMultiplier = true, multiplier
	// Dispatch and ranking both reject a shadow whose parent could not be
	// loaded. Only the supply snapshot needs to distinguish a failed read.
	parentReadsKnown := true
	lookupParent := func(id int64) *Account {
		parent, err := s.lookupShadowParentAccount(ctx, id)
		if err != nil && !errors.Is(err, ErrAccountNotFound) {
			parentReadsKnown = false
		}
		return parent
	}
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
			ok, reason = s.accountRequestCompatibleWithParent(ctx, account, req, lookupParent)
		}
		if channelRestricted {
			ok, reason = false, "channel_pricing_restricted"
		}
		if openAIConfiguredModelFailureReason(ctx, account, model) == "" {
			supply.Declared++
			if account.IsActive() && account.Schedulable {
				supply.Enabled++
				// Use this board request's admission result, independent of occupancy.
				if ok {
					supply.Available++
				}
			}
		}
		key := buildSelectionKey(view, groupID, model, account, selectionReq, now)
		row := SchedBoardRow{AccountID: account.ID, Priority: key.Priority, Rate: key.Rate, Concurrency: account.Concurrency}
		row.Name, row.Layer, row.Pinned = account.Name, "front", key.Pinned
		row.LoadRate, row.LastUsedAt = key.LoadRate, account.LastUsedAt
		state := selectionState(view, groupID, account, model)
		override, _ := state.EffectiveOverride(now)
		row.Exempt = override.Mode == schedquality.OverrideExempt
		placement := state.Placement(selectionAuto(view, groupID, now), now)
		if placement == schedquality.PlaceRear {
			row.Layer = "rear"
		}
		if placement == schedquality.PlaceTrial {
			row.Layer = "trial"
			if key.Tier == 1 {
				row.Layer = "trial_full"
			}
		}
		row.Lines = map[string]string{}
		hasRear := state.TTFO.Identity == schedquality.IdentityRear || state.Error.Identity == schedquality.IdentityRear || state.Cache.Identity == schedquality.IdentityRear
		for name, line := range map[string]schedquality.LineView{"ttfo": state.TTFO, "error": state.Error, "cache": state.Cache} {
			identity := "unknown"
			if line.Identity == schedquality.IdentityPass {
				identity = "pass"
			}
			if line.Identity == schedquality.IdentityRear {
				identity = "rear"
			}
			if line.Trial {
				identity = "trial"
			}
			row.Lines[name] = identity
			var until time.Time
			if hasRear && line.Identity == schedquality.IdentityRear {
				until = line.Until
			}
			if !hasRear && line.Trial {
				until = line.TrialUntil
			}
			if !until.IsZero() && (row.Until == nil || until.After(*row.Until)) {
				at := until
				row.Until = &at
			}
		}
		if strings.HasPrefix(reason, "profit_") {
			row.Excluded = reason
		}

		if !ok {
			row.Layer = "stopped"
			if reason == "account_scheduling_threshold" {
				row.Until = threshold.Until
			} else {
				row.Until = selectionRejectUntil(ctx, s, account, model, reason, now)
			}
		}
		if load := loads[account.ID]; load != nil {
			row.InFlight = load.CurrentConcurrency
		}
		ordered = append(ordered, keyedRow{row, key})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		iStopped, jStopped := ordered[i].row.Layer == "stopped", ordered[j].row.Layer == "stopped"
		if iStopped != jStopped {
			return !iStopped
		}
		if iStopped {
			return ordered[i].row.AccountID < ordered[j].row.AccountID
		}
		return selectionLess(ordered[i].key, ordered[j].key)
	})
	rows := make([]SchedBoardRow, 0, len(ordered))
	for i, item := range ordered {
		item.row.Rank = i + 1
		rows = append(rows, item.row)
	}
	if !parentReadsKnown {
		supply = nil
	}
	return rows, supply, nil
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
