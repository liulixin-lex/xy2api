package service

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
)

func (s *OpenAIGatewayService) recordOpenAISelectionFlags(ctx context.Context, groupID *int64, model string, selection *AccountSelectionResult) {
	state := resilience.StateFrom(ctx)
	if selection == nil || selection.Account == nil {
		state.SetSchedSelectionFlags(0, false, false)
		return
	}
	account := selection.Account
	view, now := s.selectionView(), s.selectionNow()
	fallback := account.Platform == PlatformOpenAI && !openAISelectionManualOnly(ctx) &&
		schedquality.StandingOf(selectionState(view, derefGroupID(groupID), account, model), selectionAuto(view, derefGroupID(groupID), now), now) == schedquality.StandBack
	change := pendingOpenAIStickyChange(ctx)
	takeover := change != nil && change.groupID == derefGroupID(groupID) && change.target == account.ID && change.kind == "takeover"
	state.SetSchedSelectionFlags(account.ID, fallback, takeover)
}
