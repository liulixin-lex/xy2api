package resilience

import "context"

// NewCollabChildState shares only immutable ingress identity and settings.
// Budgets, retry advice, selection eligibility and terminal ownership are fresh.
func NewCollabChildState(parent *RequestState) *RequestState {
	child := &RequestState{}
	if parent != nil {
		child.requestID = parent.GatewayRequestID()
		child.collabSettings.Store(parent.CollabSettingsSnapshot())
		child.groupID = parent.GroupID()
	}
	return child
}

type collabAttemptKey struct{}

// Explicit attempt metadata follows an optional send through handler, health
// and billing. Optional work never penalizes accounts or settles the customer;
// the final accepted request settles once through the existing request owner.
type CollabAttempt struct {
	Seq       CollabSendSequence
	Optional  bool
	Abandoned bool
}

func WithCollabAttempt(ctx context.Context, attempt CollabAttempt) context.Context {
	return context.WithValue(ctx, collabAttemptKey{}, attempt)
}
func CollabAttemptFrom(ctx context.Context) CollabAttempt {
	if ctx == nil {
		return CollabAttempt{}
	}
	attempt, _ := ctx.Value(collabAttemptKey{}).(CollabAttempt)
	return attempt
}
func (a CollabAttempt) AccountEffectsAllowed() bool     { return !a.Optional }
func (a CollabAttempt) CustomerSettlementAllowed() bool { return !a.Optional && !a.Abandoned }
