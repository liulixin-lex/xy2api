package service

import "context"

// ChatRequestRequirement preserves the original request intent for selection.
type ChatRequestRequirement struct {
	OriginalModel, DispatchModel, ReasoningEffort string
	HasTools                                      bool
	ToolField                                     string
}

type chatRequestRequirementKey struct{}

func WithChatRequestRequirement(ctx context.Context, req ChatRequestRequirement) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, chatRequestRequirementKey{}, req)
}

func ChatRequestRequirementFromContext(ctx context.Context) (ChatRequestRequirement, bool) {
	if ctx == nil {
		return ChatRequestRequirement{}, false
	}
	req, ok := ctx.Value(chatRequestRequirementKey{}).(ChatRequestRequirement)
	return req, ok
}
