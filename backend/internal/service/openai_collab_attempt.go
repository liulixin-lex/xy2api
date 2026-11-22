package service

import "github.com/gin-gonic/gin"

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
