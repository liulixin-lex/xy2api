package handler

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// resilienceReportAttemptFailure reports a failed upstream attempt to the
// session failure handling of the request (design §4.1). An attempt the
// stream handler already reported (a failure after output, design §7.6)
// counts once, even when its error reaches a failover loop.
func resilienceReportAttemptFailure(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError) {
	st := resilience.StateFrom(ctx)
	if st == nil || failoverErr == nil || failoverErr.Reason == service.PlannedFirstOutputTimeoutReason || st.StreamFailureReported(accountID) {
		return
	}
	st.SessionFailure(ctx, accountID, service.ClassifyFailoverError(failoverErr).Category)
}

// resilienceContentRejected reports a failed attempt that the upstream
// refused for the request content (design §2.2 CLIENT_ERROR): a retry on the
// same account or a switch is refused the same way, so the failover loop ends
// and the refusal goes back to the client.
func resilienceContentRejected(failoverErr *service.UpstreamFailoverError) bool {
	return failoverErr != nil && resilience.Current().FailureOn() &&
		service.ClassifyFailoverError(failoverErr).ContentPolicy()
}
