package service

import (
	"context"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// openAIResilienceEligibility reuses the scheduler's request gates while
// allowing the soft penalties that last-resort selection can relax.
type openAIResilienceEligibility struct {
	service          *OpenAIGatewayService
	ctx              context.Context
	req              OpenAIAccountScheduleRequest
	full             func(*Account) bool
	httpContinuation bool
}

func (s *OpenAIGatewayService) withResilienceOpenAIEligibility(ctx context.Context, groupID *int64, previousResponseID, requestedModel string, excludedIDs map[int64]struct{}, transport OpenAIUpstreamTransport, capability OpenAIEndpointCapability, imageCapability OpenAIImagesCapability, compact bool, platform string) context.Context {
	st := resilience.StateFrom(ctx)
	if st == nil {
		return ctx
	}
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, groupID)
	if imageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, groupID)
	}
	gate := &openAIResilienceEligibility{service: s, ctx: withResilienceLastResortGate(ctx), req: OpenAIAccountScheduleRequest{
		GroupID: groupID, Platform: NormalizeOpenAICompatiblePlatform(platform), RequestedModel: requestedModel,
		ExcludedIDs: cloneExcludedAccountIDs(excludedIDs), RequiredTransport: transport, RequiredCapability: capability,
		RequiredImageCapability: imageCapability, RequireCompact: compact,
		RequirePrivacySet: s.openAIGroupRequiresPrivacySet(ctx, groupID),
	}}
	gate.httpContinuation = strings.TrimSpace(previousResponseID) != "" && gate.req.Platform == PlatformOpenAI &&
		(transport == OpenAIUpstreamTransportAny || transport == OpenAIUpstreamTransportHTTPSSE)
	scheduler := &defaultOpenAIAccountScheduler{service: s}
	gate.full = func(candidate *Account) bool {
		return scheduler.isAccountRequestCompatible(gate.ctx, candidate, gate.req)
	}
	resilience.SetSelectionEligibility(st, gate)
	return ctx
}

func resilienceOpenAIEligibility(ctx context.Context, groupID int64, model string) *openAIResilienceEligibility {
	gate := resilience.SelectionEligibility[*openAIResilienceEligibility](resilience.StateFrom(ctx))
	if gate == nil || derefGroupID(gate.req.GroupID) != groupID || gate.req.RequestedModel != model {
		return nil
	}
	return gate
}

func (g *openAIResilienceEligibility) allows(account *Account, allowIO, recovery bool, now time.Time) bool {
	if account == nil || account.Platform != g.req.Platform || !account.IsOpenAICompatible() {
		return false
	}
	if g.httpContinuation && !account.IsOpenAIApiKey() {
		return false
	}
	// Recovery describes a later request, so this attempt's exclusions do not
	// prevent a penalised account from providing its known recovery time.
	if !recovery {
		if _, excluded := g.req.ExcludedIDs[account.ID]; excluded {
			return false
		}
	}
	candidate := withoutSoftPenalties(*account, now)
	// Callers evaluate the model's penalty separately (hard exclusion or
	// timed recovery). Do not let that timed limit mask the other gates.
	if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(g.ctx, candidate, g.req.Platform, "", g.req.RequireCompact, g.req.RequiredCapability); reason != "" {
		return false
	}
	if g.req.RequestedModel != "" && !candidate.IsModelSupported(g.req.RequestedModel) {
		return false
	}
	if !g.req.RequireCompact && g.req.RequiredImageCapability == "" && capabilityEligibilityReason(g.ctx, candidate, g.req.RequestedModel, now) != "" {
		return false
	}
	scheduler := &defaultOpenAIAccountScheduler{service: g.service}
	if !scheduler.isAccountTransportCompatible(candidate, g.req.RequiredTransport) {
		return false
	}
	if allowIO {
		return g.full(candidate)
	}
	// Terminal frames must not look up parents or channel restrictions. The
	// account/request-only gates still apply; recovery advice is capped at 10s.
	if g.req.RequirePrivacySet && !candidate.IsPrivacySet() {
		return false
	}
	if !accountSupportsOpenAICapabilities(candidate, g.req.RequiredCapability, g.req.RequiredImageCapability) {
		return false
	}
	vetoed, _ := openAIProfitControlVetoReason(g.ctx, candidate)
	return !vetoed
}
