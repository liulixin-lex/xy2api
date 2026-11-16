package service

import "context"

// openAISelectionContext carries the effective scheduling group, independently
// of whether its profit gate is enabled, plus continuation/migration guards.
type openAISelectionContext struct {
	service     *OpenAIGatewayService
	groupID     int64
	exempt      bool
	request     *OpenAIAccountScheduleRequest
	lastResort  bool
	proxyBypass bool
}
type openAISelectionContextKey struct{}
type openAISelectionExemptContextKey struct{}
type openAISelectionManualOnlyContextKey struct{}

// WithOpenAISelectionManualOnly marks an uncollected handler entry point.
func WithOpenAISelectionManualOnly(ctx context.Context) context.Context {
	return withOpenAISelectionManualOnly(ctx)
}

// Uncollected endpoints use manual placement overrides only.
func withOpenAISelectionManualOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAISelectionManualOnlyContextKey{}, true)
}

func openAISelectionManualOnly(ctx context.Context) bool {
	manualOnly, _ := ctx.Value(openAISelectionManualOnlyContextKey{}).(bool)
	return manualOnly
}

func (s *OpenAIGatewayService) withOpenAISelectionContext(ctx context.Context, groupID *int64) context.Context {
	if _, suppressed := ctx.Value(openAIProfitControlSuppressCtxKey{}).(struct{}); suppressed {
		return ctx
	}
	var gate *openAISelectionContext
	if s != nil && groupID != nil && *groupID > 0 {
		gate = &openAISelectionContext{service: s, groupID: *groupID}
	}
	return context.WithValue(ctx, openAISelectionContextKey{}, gate)
}
func withOpenAISelectionExempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAISelectionExemptContextKey{}, true)
}
func openAISelectionExempt(ctx context.Context) bool {
	exempt, _ := ctx.Value(openAISelectionExemptContextKey{}).(bool)
	return exempt
}
func attachSelectionContext(ctx context.Context, selection *AccountSelectionResult) {
	if selection == nil {
		return
	}
	gate, _ := ctx.Value(openAISelectionContextKey{}).(*openAISelectionContext)
	if _, hasRequest := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest); gate == nil && !hasRequest {
		return
	}
	var copied openAISelectionContext
	if gate != nil {
		copied = *gate
	}
	if req, ok := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest); ok {
		// Preserve the effective route even with profit control disabled.
		if privacy, ok := ctx.Value(openAIGroupPrivacyRequirementContextKey{}).(openAIGroupPrivacyRequirement); ok && privacy.groupID == derefGroupID(req.GroupID) {
			req.RequirePrivacySet = privacy.required
		}
		if req.GroupID != nil {
			gid := *req.GroupID
			req.GroupID = &gid
		}
		if req.ExcludedIDs != nil {
			excluded := make(map[int64]struct{}, len(req.ExcludedIDs))
			for id := range req.ExcludedIDs {
				excluded[id] = struct{}{}
			}
			req.ExcludedIDs = excluded
		}
		copied.request = &req
	}
	copied.exempt = openAISelectionExempt(ctx)
	copied.lastResort = resilienceLastResortGate(ctx)
	copied.proxyBypass = openAIProxyStreamQuarantineBypassed(ctx)
	selection.selectionContext = &copied
}
func contextWithSelectionContext(ctx context.Context, selection *AccountSelectionResult) context.Context {
	if selection == nil || selection.selectionContext == nil {
		return ctx
	}
	gate := selection.selectionContext
	ctx = context.WithValue(ctx, openAISelectionContextKey{}, gate)
	if gate.request != nil {
		ctx = context.WithValue(ctx, openAISelectionRequestKey{}, *gate.request)
	}
	ctx = context.WithValue(ctx, resilienceLastResortGateKey{}, gate.lastResort)
	ctx = context.WithValue(ctx, openAIProxyStreamQuarantineBypassKey{}, gate.proxyBypass)
	return ctx
}

// WithOpenAISelectionRequestModel updates a persistent connection's request
// constraints for the current turn while retaining its group and capabilities.
func WithOpenAISelectionRequestModel(ctx context.Context, model string) context.Context {
	req, ok := ctx.Value(openAISelectionRequestKey{}).(OpenAIAccountScheduleRequest)
	if !ok {
		return ctx
	}
	req.RequestedModel = model
	return context.WithValue(ctx, openAISelectionRequestKey{}, req)
}
