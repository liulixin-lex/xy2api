package service

import "context"

// openAISelectionContext carries the effective scheduling group, independently
// of whether its profit gate is enabled, plus continuation/migration guards.
type openAISelectionContext struct {
	service *OpenAIGatewayService
	groupID int64
	exempt  bool
}
type openAISelectionContextKey struct{}
type openAISelectionExemptContextKey struct{}
type openAISelectionManualOnlyContextKey struct{}

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
	gate, _ := ctx.Value(openAISelectionContextKey{}).(*openAISelectionContext)
	if selection == nil || gate == nil {
		return
	}
	copy := *gate
	copy.exempt = openAISelectionExempt(ctx)
	selection.selectionContext = &copy
}
func contextWithSelectionContext(ctx context.Context, selection *AccountSelectionResult) context.Context {
	if selection == nil || selection.selectionContext == nil {
		return ctx
	}
	return context.WithValue(ctx, openAISelectionContextKey{}, selection.selectionContext)
}
