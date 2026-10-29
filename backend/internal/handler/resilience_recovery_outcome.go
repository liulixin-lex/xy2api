package handler

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Handlers record the actual forward result, independently of HTTP status and
// usage submission (both may describe a partially delivered failure).
func recordForwardRecovery(c *gin.Context, err error, disconnected, completed bool) {
	if c == nil || c.Request == nil {
		return
	}
	ctx := c.Request.Context()
	outcome := "upstream_failure"
	switch {
	case disconnected || ctx.Err() != nil:
		outcome = "client_cancel"
	case err == nil && completed:
		outcome = "completed"
	default:
		var failure *service.UpstreamFailoverError
		var rejection *service.OpenAIRequestRejectionError
		if errors.As(err, &rejection) || (errors.As(err, &failure) && failure != nil && (!failure.ShouldRetryNextAccount() || resilienceContentRejected(failure))) {
			outcome = "terminal_rejection"
		}
	}
	resilience.StateFrom(ctx).SetRecoveryOutcome(outcome)
}

func recordSelectionExhausted(ctx context.Context) {
	outcome := "selection_exhausted"
	if ctx.Err() != nil {
		outcome = "client_cancel"
	} else if b := requestBudget(ctx); b != nil && !b.CanAttempt(time.Now()) {
		outcome = "budget_exhausted"
	}
	resilience.StateFrom(ctx).SetRecoveryOutcome(outcome)
}

func recoveryOutcome(c *gin.Context) *string {
	st := resilience.StateFrom(c.Request.Context())
	outcome := st.RecoveryOutcome()
	// A client may cancel after a timeout was switched away from or while a
	// detached upstream is draining. This final request reason wins.
	if st.ClientGone() || (outcome != nil && *outcome == "client_cancel") {
		v := "client_cancel"
		return &v
	}
	if outcome != nil && *outcome != "completed" && st.EndToEndFirstOutputMs() != nil {
		v := "post_output_failure"
		return &v
	}
	return outcome
}

func recordExhaustedRecovery(c *gin.Context, failure *service.UpstreamFailoverError) {
	if c == nil || c.Request == nil {
		return
	}
	st := resilience.StateFrom(c.Request.Context())
	if st.RecoveryOutcome() == nil {
		recordForwardRecovery(c, failure, false, false)
	}
	if c.Request.Context().Err() != nil {
		st.SetRecoveryOutcome("client_cancel")
		return
	}
	if resilienceContentRejected(failure) || (failure != nil && !failure.ShouldRetryNextAccount()) {
		st.SetRecoveryOutcome("terminal_rejection")
	} else if b := requestBudget(c.Request.Context()); b != nil && b.Exhausted() != "" {
		st.SetRecoveryOutcome("budget_exhausted")
	}
}
