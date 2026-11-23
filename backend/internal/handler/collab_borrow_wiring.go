package handler

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
)

func (h *OpenAIGatewayHandler) tryCollabBorrow(c *gin.Context, account *service.Account, body []byte, model string, settle func(*service.Account, *service.OpenAIForwardResult)) (delivered bool) {
	defer func() {
		if recover() != nil {
			resilience.Count("collab_wiring_panic", 0, 0, "borrow_handler")
		}
	}()
	parentRequest := c.Request
	parent := resilience.StateFrom(parentRequest.Context())
	if !parent.CollabSettingsSnapshot().CollabRedispatchBorrowOn() || c.Writer.Written() {
		return false
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		return false
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		return false
	}
	budget := parent.Budget()
	if budget != nil && budget.MaxAttempts() == 1 {
		return false
	}
	remaining := time.Duration(0)
	if budget != nil {
		remaining = budget.Remaining(time.Now())
	}
	margin := time.Minute
	if deadline, ok := parentRequest.Context().Deadline(); ok {
		margin = time.Until(deadline) - remaining
	}
	duration := min(15*time.Second, margin)
	if duration < time.Second {
		return false
	}
	started := time.Now()
	defer func() {
		if !delivered && budget != nil {
			budget.CompensateCollabPreempt(time.Since(started))
		}
	}()
	ctx, cancel := context.WithTimeout(parentRequest.Context(), duration)
	defer cancel()
	ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true})
	observer := service.NewLazyCollabSendObserver(body, service.CollabBodyFactsForRequest(ctx))
	ctx = service.WithCollabSendObserver(ctx, observer)
	ctx, stopWatch := watchCollabTakeover(ctx, duration, observer.Accepted, func(query context.Context) bool {
		loads, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(query, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
		return err != nil || loads[subject.UserID] != nil && loads[subject.UserID].WaitingCount > 0
	})
	defer stopWatch()
	users, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
	if err != nil || users[subject.UserID] != nil && users[subject.UserID].WaitingCount > 0 {
		return false
	}
	loads, err := h.concurrencyHelper.concurrencyService.GetAccountsLoadBatchFresh(ctx, []service.AccountWithConcurrency{{ID: account.ID, MaxConcurrency: account.Concurrency}})
	if err != nil || loads[account.ID] == nil || loads[account.ID].WaitingCount > 0 || account.Concurrency <= 0 || loads[account.ID].CurrentConcurrency >= account.Concurrency {
		return false
	}
	selection := h.gatewayService.SelectOpenAICollabBorrow(ctx, c, key.GroupID, account, model)
	if selection == nil {
		return false
	}
	if selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	child := resilience.NewCollabChildState(parent)
	ctx = resilience.WithRequestState(service.WithCollabAttemptIsolation(ctx), child)
	keys, writer := c.Copy().Keys, c.Writer
	header := writer.Header().Clone()
	flag := &atomic.Bool{}
	flag.Store(true)
	w := newCollabSeamWriter(writer, flag)
	mark := w.beginPreemption()
	if mark == nil {
		return false
	}
	seam := &collabSeam{writer: w, original: writer, started: started}
	c.Writer = w
	seam.rescue = func(c *gin.Context, _ *collabSeam, _ *collabWithheld) {
		c.Request = parentRequest.Clone(ctx)
		service.IsolateCollabAttempt(c)
		result, forwardErr := h.gatewayService.Forward(ctx, c, selection.Account, body)
		if w.current() == collabSeamPassing {
			c.Request = parentRequest.WithContext(resilience.WithCollabAttempt(parentRequest.Context(), resilience.CollabAttempt{Optional: true, Delivered: true}))
			parent.NoteGatewaySend(selection.Account.ID)
			parent.AdoptCollabDelivery(selection.Account.ID, child)
			// The borrowed response is the one the client received: its turn-state
			// origin and response binding are committed as on a ladder delivery.
			if persist := h.gatewayService.CommitCollabResponseIdentity(c, selection.Account, result, ""); persist != nil {
				defer persist()
			}
			recordOpenAIForwardUsage(c, selection.Account, result)
			recordForwardRecovery(c, forwardErr, result != nil && result.ClientDisconnect, gatewayOpenAICompleted(result))
			settle(selection.Account, result)
		}
	}
	wireCollabDeliveryRescue(seam)
	delivered = seam.takeOver(c)
	c.Request = parentRequest
	if !delivered {
		w.restorePreemption(mark)
		collabSeamSetHeader(writer.Header(), header)
		collabSeamSetKeys(c, keys)
	}
	c.Writer = writer
	return delivered
}
