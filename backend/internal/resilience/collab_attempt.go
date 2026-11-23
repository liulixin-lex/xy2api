package resilience

import "context"

// NewCollabChildState shares only immutable ingress identity and settings.
// Budgets, retry advice, selection eligibility and terminal ownership are fresh.
func NewCollabChildState(parent *RequestState) *RequestState {
	child := &RequestState{}
	if parent != nil {
		child.collabParent = parent
		child.requestID = parent.GatewayRequestID()
		child.collabSettings.Store(parent.CollabSettingsSnapshot())
		child.groupID = parent.GroupID()
		child.edge = parent.Edge()
	}
	return child
}

// AdoptCollabDelivery clears delivery-only failure decisions after actual
// output. Account penalties and rejection counters remain owned by legacy.
func (s *RequestState) AdoptCollabDelivery(accountID int64, child *RequestState) bool {
	if s == nil || child == nil || child.collabParent != s || s.DeliveredBytes() == 0 {
		return false
	}
	child.mu.Lock()
	advice := child.retryAdvice
	child.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryAdvice, s.retryAdviceSet, s.retryAdviceProvider = advice, true, nil
	s.turnFailed = false
	delete(s.failedAccounts, accountID)
	return true
}

type collabAttemptKey struct{}

// Explicit attempt metadata follows an optional send through handler, health
// and billing. Optional work never penalizes accounts or settles the customer;
// the final accepted request settles once through the existing request owner.
type CollabAttempt struct {
	Seq       CollabSendSequence
	Action    CollabAction
	Optional  bool
	Abandoned bool
	Delivered bool
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
func (a CollabAttempt) AccountEffectsAllowed() bool { return !a.Optional }
func (a CollabAttempt) CustomerSettlementAllowed() bool {
	return (!a.Optional || a.Delivered) && !a.Abandoned
}
