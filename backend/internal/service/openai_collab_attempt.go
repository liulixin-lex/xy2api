package service

import (
	"context"
	"github.com/gin-gonic/gin"
	"sync/atomic"
)

// Selection may update the rejection scope's binding flag. Keep that mutable
// cell out of the legacy context even though the thread identity is shared.
func WithCollabAttemptIsolation(ctx context.Context) context.Context {
	if scope, ok := ctx.Value(openAITerminalRejectionScopeKey{}).(openAITerminalRejectionScope); ok {
		scope.selectionPreservesBinding = &atomic.Bool{}
		ctx = context.WithValue(ctx, openAITerminalRejectionScopeKey{}, scope)
	}
	return ctx
}

// IsolateCollabAttempt removes mutable forwarding state from the shallow gin
// snapshot. Ingress authentication/routing values remain read-only; attempts
// create fresh repair maps, restoration state, quotas and response observers.
func IsolateCollabAttempt(c *gin.Context) {
	for _, key := range []string{openAIM3ContextKey, openAIRequestRepairContextKey,
		openAICollabResponseKey, codexCollabAttemptContextKey, upstreamResponseModelObserverContextKey,
		openAIFCOReplayKey, openAIFCOAttemptKey, openAIResponsesRejectedFieldRetryBudgetContextKey,
		openAIWebSearchReplayContextKey, openAIStateRejectionAttemptKey, openAIStateRejectionBodyKey} {
		c.Delete(key)
	}
	previous := currentOpsUpstreamEvents(c)
	events := make([]*OpsUpstreamErrorEvent, len(previous))
	for i, event := range previous {
		if event != nil {
			copy := *event
			events[i] = &copy
		}
	}
	if previous != nil {
		c.Set(OpsUpstreamErrorsKey, events)
	}
}
