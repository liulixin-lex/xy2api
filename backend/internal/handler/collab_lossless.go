package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (h *OpenAIGatewayHandler) armCollabLossless(c *gin.Context, seam *collabSeam) {
	if seam == nil || seam.rescue == nil || !resilience.StateFrom(c.Request.Context()).CollabSettingsSnapshot().CollabLosslessFirstOn() {
		return
	}
	gate := &service.CollabLosslessGate{Eligible: func(account *service.Account) bool {
		ctx := c.Request.Context()
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		if !ok {
			return false
		}
		users, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
		if err != nil || users[subject.UserID] != nil && users[subject.UserID].WaitingCount > 0 {
			return false
		}
		loads, err := h.concurrencyHelper.concurrencyService.GetAccountsLoadBatchFresh(ctx, []service.AccountWithConcurrency{{ID: account.ID, MaxConcurrency: account.Concurrency}})
		if err != nil || loads[account.ID] == nil || loads[account.ID].WaitingCount > 0 || account.Concurrency <= 0 || loads[account.ID].CurrentConcurrency >= account.Concurrency {
			return false
		}
		idle := 90*time.Second - time.Since(seam.started)
		if deadline, ok := ctx.Deadline(); ok {
			idle = min(idle, time.Until(deadline))
		}
		remaining := time.Duration(0)
		if budget := resilience.StateFrom(ctx).Budget(); budget != nil {
			if budget.MaxAttempts() == 1 {
				return false
			}
			remaining = budget.Remaining(time.Now())
		}
		seam.losslessDuration = min(time.Minute, idle-remaining)
		return seam.losslessDuration >= time.Second
	}}
	c.Request = c.Request.WithContext(service.WithCollabLosslessGate(c.Request.Context(), gate))
}

func (s *collabSeam) tryLossless(c *gin.Context) bool {
	if s == nil || s.losslessDuration <= 0 {
		return false
	}
	started := time.Now()
	parent := resilience.StateFrom(c.Request.Context())
	w := s.writer
	w.mu.Lock()
	if w.current() != collabSeamWatching {
		w.mu.Unlock()
		return false
	}
	w.held = &collabWithheld{status: w.Status(), committed: w.ResponseWriter.Written(), header: w.Header().Clone(), commitHeader: w.Header().Clone()}
	w.set(collabSeamHolding)
	w.mu.Unlock()
	delivered := s.takeOver(c)
	if !delivered {
		w.discard()
		w.mu.Lock()
		w.quiet = false
		w.takeover.Store(false)
		w.mu.Unlock()
		parent.Budget().CompensateCollabPreempt(time.Since(started))
		s.rescue = nil // optional quotas are request-wide, including legacy fallback
	}
	s.losslessDuration = 0
	return delivered
}
