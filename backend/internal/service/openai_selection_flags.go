package service

import (
	"context"
	"log/slog"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) recordOpenAISelectionFlags(ctx context.Context, groupID *int64, model string, selection *AccountSelectionResult) {
	state := resilience.StateFrom(ctx)
	if selection == nil || selection.Account == nil {
		state.SetSchedSelectionFlags(0, false, false)
		return
	}
	s.recordOpenAISelectionFlagsForModel(ctx, groupID, selection.Account, OpenAIAttemptModel(selection.Account, model))
}

func (s *OpenAIGatewayService) recordOpenAISelectionFlagsForModel(ctx context.Context, groupID *int64, account *Account, model string) {
	state := resilience.StateFrom(ctx)
	view, now := s.selectionView(), s.selectionNow()
	fallback := account.Platform == PlatformOpenAI && !openAISelectionManualOnly(ctx) &&
		view.StateOf(derefGroupID(groupID), account.ID, model).Placement(selectionAuto(view, derefGroupID(groupID), now), now) == schedquality.PlaceRear
	change := pendingOpenAIStickyChange(ctx)
	takeover := change != nil && change.groupID == derefGroupID(groupID) && change.target == account.ID && change.kind == "takeover"
	state.SetSchedSelectionFlags(account.ID, fallback, takeover)
}

// RefreshOpenAISelectionFlags recalculates per-turn fallback standing for an
// already selected account. WebSocket turns reuse their connection account but
// must use the current quality view when recording each request.
func (s *OpenAIGatewayService) RefreshOpenAISelectionFlags(ctx context.Context, groupID *int64, account *Account, model string) {
	var selection *AccountSelectionResult
	if account != nil {
		selection = &AccountSelectionResult{Account: account}
	}
	s.recordOpenAISelectionFlags(ctx, groupID, model, selection)
	if account != nil {
		group, session := resilience.StateFrom(ctx).SessionIdentity()
		if group == derefGroupID(groupID) && session != "" {
			s.newSelectionTrial(ctx, OpenAIAccountScheduleRequest{GroupID: groupID, SessionHash: session, RequestedModel: model}, account.ID).touch(ctx, account)
		}
	}
}

// PrepareOpenAISelectionTurn receives the already resolved sampling model.
// It neither repeats channel/account mapping nor creates a session on a borrow.
func (s *OpenAIGatewayService) PrepareOpenAISelectionTurn(ctx context.Context, groupID *int64, account *Account, model string, borrowed bool, slotRelease func()) func() {
	s.recordOpenAISelectionFlagsForModel(ctx, groupID, account, model)
	leases := s.selectionTrialLeases()
	view, now := s.selectionView(), s.selectionNow()
	if leases == nil || account.Platform != PlatformOpenAI || openAISelectionManualOnly(ctx) || view.StateOf(derefGroupID(groupID), account.ID, model).Placement(selectionAuto(view, derefGroupID(groupID), now), now) != schedquality.PlaceTrial {
		return slotRelease
	}
	group, session := resilience.StateFrom(ctx).SessionIdentity()
	if !borrowed && session != "" && group == derefGroupID(groupID) {
		if err := leases.Touch(ctx, account.ID, model, group, TrialLeaseSessionMember(session), now); err != nil {
			slog.WarnContext(ctx, "sched_quality.trial_touch_failed", "component", "sched_quality", "error", err)
		}
		return slotRelease
	}
	member := TrialLeaseRequestMember(resilience.StateFrom(ctx).GatewayRequestID())
	acquired, err := leases.Acquire(ctx, account.ID, model, derefGroupID(groupID), member, now)
	if err != nil {
		slog.WarnContext(ctx, "sched_quality.trial_acquire_failed", "component", "sched_quality", "error", err)
	}
	if !acquired || err != nil {
		return slotRelease
	}
	reservation := &selectionTrialReservation{leases: leases, account: account.ID, group: derefGroupID(groupID), model: model, member: member, request: true}
	return releaseSelectionTrialWithSlot(ctx, nil, reservation, slotRelease)
}
