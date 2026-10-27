package service

// OpenAIAttemptModel resolves the request model once to the scheduling key
// shared by first-output guards, the model breaker and latency holds.
func OpenAIAttemptModel(account *Account, requestedModel string) string {
	return canonicalOpenAIAccountSchedulingModel(account, requestedModel)
}

// OpenAIAttemptWarm reports whether the current selection is a warm sticky
// attempt. The session-sticky layer is the only layer that proves the selected
// account is the existing owner; guardian and previous-response hits do not.
func OpenAIAttemptWarm(decision OpenAIAccountScheduleDecision, selection *AccountSelectionResult, priorUpstreamFailure bool) bool {
	return selection != nil && decision.StickySessionHit && decision.Layer == openAIAccountScheduleLayerSessionSticky && !selection.StickyCapacitySpillover && !priorUpstreamFailure
}
