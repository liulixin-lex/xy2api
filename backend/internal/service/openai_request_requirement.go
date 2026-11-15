package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"time"
)

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

type openAISelectionNoWaitKey struct{}

func WithOpenAISelectionNoWait(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAISelectionNoWaitKey{}, true)
}

func openAISelectionNoWait(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	noWait, _ := ctx.Value(openAISelectionNoWaitKey{}).(bool)
	return noWait
}

// OpenAIQueueReselectionAllowed preserves the existing hard owner/migration
// guards when the HTTP admission path considers one local free-slot retry.
func OpenAIQueueReselectionAllowed(ctx context.Context) bool {
	return ctx != nil && !openAINoStickyMigration(ctx) && !openAISelectionExempt(ctx)
}

// The scheduling recheck may be capped; the provider's original deadline is not.
func accountRetryNotBeforeBlocked(ctx context.Context, accountID int64) bool {
	hint, ok := resilience.StateFrom(ctx).AccountRetryNotBefore(accountID)
	return ok && time.Now().Before(hint.NotBefore)
}
