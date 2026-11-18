package service

import (
	"context"
	"net/http/httptrace"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Only sendSchedHTTP attaches this handle to the built request.
type schedHTTPAttemptKey struct{}

// A redirect or a transport retry can use another connection (including a
// reused one) within the same Do. The installed timer spans both wire sends.
func (a *schedAttemptCapture) withHTTPDispatchTrace(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, schedHTTPAttemptKey{}, a)
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.gotHTTPConn {
			a.invalidateRepeatedSendLocked()
		}
		a.gotHTTPConn = true
	}})
}

func (a *schedAttemptCapture) invalidateRepeatedSendLocked() {
	a.repeatedHTTPSend = true
	a.ownerHit, a.fullTierFirstOutputTimeout = false, false
}

func (a *schedAttemptCapture) markHTTPDispatch(seq uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.httpSelection.HTTPStreamText || a.done || seq == 0 || a.state.CurrentAttempt() != a {
		return
	}
	if a.dispatch.AttemptSeq != 0 {
		if seq != a.dispatch.AttemptSeq {
			a.invalidateRepeatedSendLocked()
		}
		return
	}
	group, session := a.state.SessionIdentity()
	if group != a.dispatch.GroupID || session != a.dispatch.Session {
		return
	}
	a.dispatch.AttemptSeq, a.dispatch.DispatchAt = seq, time.Now()
	a.ownerHit = a.state.FirstHTTPDispatch(seq) && a.httpSelection.StickyWarm && a.dispatch.Session != ""
}

// The header guard's context remains attached to the response reader. Cancel
// it without waiting for Body.Close, which may wait on the active read.
func (a *schedAttemptCapture) cancelFirstOutputUpstream() {
	if a != nil && a.firstOutputCancel != nil {
		a.firstOutputCancel()
	}
}

// onClaim stops the upstream before any synchronous downstream publication.
// Only a winning guard calls this. The recorder reads the two booleans under
// the capture lock; the final request outcome cannot create either fact.
func claimOpenAIFirstOutputCutoff(a *schedAttemptCapture, plan resilience.TimeoutPlan, firedAt time.Time, onClaim context.CancelFunc) (resilience.OwnerFirstOutputCutoff, bool) {
	if a == nil || !plan.FullFirstOutputTier() || a.accountID != plan.AccountID() || resilienceTextBudget(a.ctx) == nil {
		return resilience.OwnerFirstOutputCutoff{}, false
	}
	a.mu.Lock()
	if a.fullTierFirstOutputTimeout {
		fact := a.dispatch
		a.mu.Unlock()
		return fact, false
	}
	if a.repeatedHTTPSend || a.dispatch.AttemptSeq == 0 || firedAt.Before(plan.Start().Add(plan.Effective())) || firedAt.Before(a.dispatch.DispatchAt) ||
		a.first || a.terminal || a.httpStatus >= 400 || a.done || a.client || a.ctx.Err() != nil || a.state.ClientCanceled() || !a.state.IsCurrentHTTPDispatch(a, a.dispatch.AttemptSeq) {
		a.mu.Unlock()
		return resilience.OwnerFirstOutputCutoff{}, false
	}
	a.fullTierFirstOutputTimeout = true
	fact := a.dispatch
	fact.FiredAt, fact.Effective = firedAt, plan.Effective()
	a.dispatch = fact
	recorded := a.ownerHit && a.state.RecordOwnerCutoff(fact)
	a.mu.Unlock()
	if !recorded {
		return resilience.OwnerFirstOutputCutoff{}, false
	}

	if onClaim != nil {
		onClaim()
	}

	// follow: use this first publication to record avoidance outside the attempt lock.
	return fact, true
}
