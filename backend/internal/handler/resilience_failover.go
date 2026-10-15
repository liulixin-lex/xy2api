package handler

import (
	"context"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// resilienceReportAttemptFailure reports a failed upstream attempt to the
// session failure handling of the request (design §4.1).
func resilienceReportAttemptFailure(ctx context.Context, accountID int64, failoverErr *service.UpstreamFailoverError) {
	st := resilience.StateFrom(ctx)
	if st == nil || failoverErr == nil {
		return
	}
	st.SessionFailure(ctx, accountID, service.ClassifyFailoverError(failoverErr).Category)
}
