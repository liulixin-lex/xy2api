package resilience

import "time"

// CollabLocalEndingFacts is a snapshot taken at the saved error endpoint, after
// ordinary repair. PriorRejections excludes this request's terminal rejection.
// Settings is the immutable ingress snapshot; no records or clocks are read here.
type CollabLocalEndingFacts struct {
	Settings                  *Settings
	GroupID                   int64
	Route                     CollabRoute
	RequestKind               CollabRequestKind
	CompleteHistory           bool
	ContinuationPointer       bool
	EvidenceGrade             CollabEvidenceGrade
	LatestRejectionCiphertext bool
	SemanticOutputDelivered   bool
	TerminalClaimed           bool
	WriteFailed               bool
	ClientCanceled            bool
	SavedErrorPending         bool
	SavedErrorRetryable       bool
	TreeLocalEndings          int
	UnreplaceableCallArgs     bool
	Delivery                  CollabDeliveryKey
	Rejection                 CollabDigest
	PriorRejections           []CollabTerminalRejectionCount
	Now                       time.Time
}

// CollabLocalEndingEligible never selects a new error: false returns control to
// the existing floor. A created frame or heartbeat alone is not semantic output.
func CollabLocalEndingEligible(f CollabLocalEndingFacts) bool {
	if !f.Settings.CollabLocalEndingOn(f.GroupID) ||
		f.Route != CollabRouteResponsesHttp || f.RequestKind != CollabRequestKindNormal ||
		!f.CompleteHistory || f.ContinuationPointer ||
		f.EvidenceGrade != CollabEvidenceGradeVerified || !f.LatestRejectionCiphertext ||
		f.SemanticOutputDelivered || f.TerminalClaimed || f.WriteFailed || f.ClientCanceled ||
		!f.SavedErrorPending || f.TreeLocalEndings >= CollabTreeLocalEndingLimit ||
		f.UnreplaceableCallArgs {
		return false
	}
	return collabLocalEndingRetryPermitted(f.SavedErrorRetryable, f.Delivery, f.Rejection, f.PriorRejections, f.Now)
}

// CollabLocalEndingText selects only fixed notice text. The event is a newly
// generated opaque number, never a request ID or text copied from the request.
// Unknown progress cannot justify claiming that work has not started.
func CollabLocalEndingText(event string, role CollabRole, execution CollabExecution, redispatchFailed, noPlaintextRoute bool) string {
	prefix := "Gateway notice [" + event + "]. "
	if redispatchFailed || noPlaintextRoute {
		return prefix + "The same task could not be delivered. This turn ended here. Repeating this delivery has not resolved the issue. Another execution path or a report of the missing input is needed."
	}
	switch role {
	case CollabRoleChild:
		if execution == CollabExecutionNotStarted {
			return prefix + "The task content could not be read. This turn ended before work began. A readable copy is needed to retry."
		}
		return prefix + "This turn ended because the task content could not be read. Earlier work may have taken effect. Existing results need to be checked before another attempt."
	case CollabRoleRoot:
		return prefix + "Some required input could not be read, so this turn ended here. This reply does not confirm task completion. Providing the missing content in plain text may allow another attempt."
	default:
		return prefix + "Some input could not be read, so this turn ended here. Earlier work may have taken effect. A readable copy of the missing content is needed to continue."
	}
}
