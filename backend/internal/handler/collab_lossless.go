package handler

import (
	"context"
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
	gate := &service.CollabLosslessGate{Eligible: func(account *service.Account) (eligible bool) {
		parent := c.Request.Context()
		budget := resilience.StateFrom(parent).Budget()
		idle := 90*time.Second - time.Since(seam.started)
		if deadline, ok := parent.Deadline(); ok {
			idle = min(idle, time.Until(deadline))
		}
		remaining := time.Duration(0)
		if budget != nil {
			if budget.MaxAttempts() == 1 {
				return false
			}
			remaining = budget.Remaining(time.Now())
		}
		duration := min(time.Minute-seam.optionalSpent, idle-remaining)
		if duration < time.Second {
			return false
		}
		started := time.Now()
		defer func() {
			elapsed := time.Since(started)
			seam.optionalSpent += elapsed
			budget.CompensateCollabEligibility(elapsed)
			seam.losslessDuration = duration - elapsed
			if recover() != nil || seam.losslessDuration < time.Second {
				eligible = false
			}
			if !eligible {
				seam.losslessDuration = 0
			}
		}()
		// Eligibility is optional preparation too. Slow load stores must yield
		// promptly, and their elapsed time belongs to the shared optional bound.
		ctx, cancel := context.WithTimeout(parent, min(duration, 250*time.Millisecond))
		defer cancel()
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		if !ok {
			return false
		}
		users, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
		if err != nil || ctx.Err() != nil || users[subject.UserID] != nil && users[subject.UserID].WaitingCount > 0 {
			return false
		}
		loads, err := h.concurrencyHelper.concurrencyService.GetAccountsLoadBatchFresh(ctx, []service.AccountWithConcurrency{{ID: account.ID, MaxConcurrency: account.Concurrency}})
		return err == nil && ctx.Err() == nil && loads[account.ID] != nil && loads[account.ID].WaitingCount == 0 && account.Concurrency > 0 && loads[account.ID].CurrentConcurrency < account.Concurrency
	}}

	c.Request = c.Request.WithContext(service.WithCollabLosslessGate(c.Request.Context(), gate))
}

func (s *collabSeam) tryLossless(c *gin.Context) bool {
	if s == nil || s.losslessDuration <= 0 {
		return false
	}
	started := time.Now()
	writer, keys := c.Writer, c.Copy().Keys
	header := c.Writer.Header().Clone()
	s.idleDeadline = service.CollabClientIdleDeadline(c, s.started)
	restoreHeartbeat := service.SuspendCollabKeepalive(c)
	c.Delete(ginKeyPreOutputKeepaliveStop)
	parent := resilience.StateFrom(c.Request.Context())
	w := s.writer
	mark := w.beginPreemption()
	if mark == nil {
		collabSeamSetKeys(c, keys)
		restoreHeartbeat()
		return false
	}
	delivered := s.takeOver(c)
	if !delivered {
		w.restorePreemption(mark)
		collabSeamSetHeader(w.Header(), header)
		collabSeamSetKeys(c, keys)
		c.Writer = writer
		restoreHeartbeat()
		if budget := parent.Budget(); budget != nil {
			budget.CompensateCollabPreempt(time.Since(started))
		}
	}
	s.losslessDuration = 0
	s.idleDeadline = time.Time{}
	return delivered
}
