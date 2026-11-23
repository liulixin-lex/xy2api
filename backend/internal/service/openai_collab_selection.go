package service

import "context"

type collabSelectionKey struct{}

// SelectCollabAccount uses the normal scheduler and its complete post-slot
// admission checks. No session is supplied: optional attempts cannot bind.
func (s *OpenAIGatewayService) SelectCollabAccount(ctx context.Context, group *int64, model string, capability OpenAIEndpointCapability, preferred int64, excluded map[int64]bool) (*AccountSelectionResult, error) {
	ctx = context.WithValue(WithOpenAISelectionNoWait(ctx), collabSelectionKey{}, preferred)
	ctx = context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
	blocked := map[int64]struct{}{}
	for id, yes := range excluded {
		if yes {
			blocked[id] = struct{}{}
		}
	}
	selection, _, err := s.SelectAccountWithSchedulerForCapability(ctx, group, "", "", model, blocked, OpenAIUpstreamTransportHTTPSSE, capability, false, false, true, PlatformOpenAI)
	return selection, err
}
