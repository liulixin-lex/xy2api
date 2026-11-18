package service

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
)

// Only sendSchedHTTP attaches this handle to the built request.
type schedHTTPAttemptKey struct{}

func (a *schedAttemptCapture) markHTTPDispatch(seq uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.httpSelection.HTTPStreamText || a.done || seq == 0 || a.dispatch.AttemptSeq != 0 || a.state.CurrentAttempt() != a {
		return
	}
	group, session := a.state.SessionIdentity()
	if group != a.dispatch.GroupID || session != a.dispatch.Session {
		return
	}
	a.dispatch.AttemptSeq, a.dispatch.DispatchAt = seq, time.Now()
	a.ownerHit = seq == 1 && a.httpSelection.StickyWarm && a.dispatch.Session != ""
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
	if a.dispatch.AttemptSeq == 0 || firedAt.Before(plan.Start().Add(plan.Effective())) || firedAt.Before(a.dispatch.DispatchAt) ||
		a.first || a.done || a.client || a.ctx.Err() != nil || a.state.ClientCanceled() || a.state.CurrentAttempt() != a {
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

	recordFirstOutputAvoid(a.ctx, fact)
	return fact, true
}
