package handler

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

func (h *OpenAIGatewayHandler) rescueCollab(c *gin.Context, seam *collabSeam, saved *collabWithheld) {
	if seam.observer == nil || seam.settle == nil {
		return
	}
	rejections := seam.observer.Rejections()
	if len(rejections) == 0 {
		return
	}
	last := rejections[len(rejections)-1]
	if last.Passthrough {
		return
	}
	parsed := service.ParseCollabBody(seam.body)
	if parsed.Shape.Ambiguous || parsed.Shape.ContinuationPointer {
		return
	}
	evidence := parsed.ResolveEvidence(last.Evidence(parsed.Shape))
	if evidence.Grade != resilience.CollabEvidenceGradeVerified && evidence.Grade != resilience.CollabEvidenceGradeClaimed {
		return
	}
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		return
	}
	parentRequest := c.Request
	parent := resilience.StateFrom(parentRequest.Context())
	started := seam.started
	if budget := parent.Budget(); budget != nil {
		started = budget.Started()
	}
	deadline, _ := parentRequest.Context().Deadline()
	retryable := saved.status == 408 || saved.status == 409 || saved.status == 429 || saved.status >= 500 || saved.committed
	if saved.header.Get("x-should-retry") == "false" {
		retryable = false
	}
	duration := service.CollabTakeoverDuration(time.Now(), started, retryable, saved.committed, deadline)
	if seam.losslessDuration > 0 {
		duration = min(duration, seam.losslessDuration)
	}
	if duration <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(parentRequest.Context())
	defer cancel()
	waitContext := ctx
	waiters := func() bool {
		loads, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(waitContext, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
		return err != nil || loads[subject.UserID] != nil && loads[subject.UserID].WaitingCount > 0
	}
	if waiters() {
		return
	}
	timer := time.AfterFunc(duration, func() {
		if seam.writer.current() != collabSeamPassing {
			cancel()
		}
	})
	defer timer.Stop()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-waitContext.Done():
				return
			case <-ticker.C:
				if seam.writer.current() == collabSeamPassing {
					return
				}
				if waiters() {
					cancel()
					return
				}
			}
		}
	}()
	child := resilience.NewCollabChildState(parent)
	settings := parent.CollabSettingsSnapshot().Clone()
	settings.Budget.MaxAttempts = 16
	child.SetBudget(resilience.NewBudget(ctx, &settings, resilience.KindText, time.Now()))
	ctx = resilience.WithRequestState(ctx, child)
	ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true})
	model := gjson.GetBytes(seam.body, "model").String()
	accounts := map[int64]*service.Account{}
	rejected := map[service.CollabRejectedBody]bool{}
	for _, rejection := range rejections {
		rejected[service.CollabRejectedBody{AccountID: rejection.AccountID, Digest: rejection.BodyDigest}] = true
	}
	seamKeys := c.Copy().Keys
	latestEvidence := evidence
	reset := func() {
		seam.writer.discard()
		collabSeamSetKeys(c, seamKeys)
		for _, key := range []string{resilience.GinKeyTerminalFinalized, service.ResponseCommittedKey, service.OpsStreamErrorKey, service.OpsStreamErrorsKey, service.OpsGatewayTerminalStatusKey} {
			c.Delete(key)
		}
		c.Request = parentRequest.WithContext(ctx)
	}
	l := service.CollabLadder{Body: parsed, First: rejections[0].AccountID, Evidence: evidence, Continue: func() bool { return !waiters() }}
	if budget := parent.Budget(); budget != nil {
		l.MaxAttempts = budget.MaxAttempts()
	}
	l.Select = func(ctx context.Context, preferred int64, excluded map[int64]bool) (int64, func()) {
		selection, err := h.gatewayService.SelectCollabAccount(ctx, key.GroupID, model, service.OpenAIEndpointCapabilityResponses, preferred, excluded)
		if err != nil || selection == nil || selection.Account == nil {
			return 0, nil
		}
		release := service.CombineSelectionRelease(selection, nil)
		selectionCtx := service.ContextWithSelectionProfitGate(ctx, selection)
		if !selection.Acquired {
			extra, acquired, err := h.concurrencyHelper.TryAcquireAccountSlot(selectionCtx, selection.Account.ID, selection.Account.Concurrency)
			if err != nil || !acquired {
				if release != nil {
					release()
				}
				return 0, nil
			}
			release = service.CombineSelectionRelease(selection, extra)
		}
		account, veto, _ := h.gatewayService.SelectionAdmissionForResult(selectionCtx, selection, false)
		if veto || account == nil {
			if release != nil {
				release()
			}
			return 0, nil
		}
		accounts[account.ID] = account
		return account.ID, release
	}
	l.Send = func(ctx context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		reset()
		observer := &service.CollabSendObserver{}
		attemptCtx := service.WithCollabSendObserver(service.WithCollabRejectedBodies(ctx, rejected), observer)
		c.Request = parentRequest.WithContext(attemptCtx)
		result, err := h.gatewayService.Forward(attemptCtx, c, accounts[id], body)
		if seam.writer.current() == collabSeamPassing {
			c.Request = parentRequest
			parent.NoteGatewaySend(id)
			resilience.SetRetryAdvice(parentRequest.Context(), resilience.RetryAdvice{})
			seam.settle(accounts[id], result, err)
			return resilience.CollabEvidence{}, true
		}
		observed := observer.Rejections()
		for _, rejection := range observed {
			rejected[service.CollabRejectedBody{AccountID: rejection.AccountID, Digest: rejection.BodyDigest}] = true
		}
		if len(observed) > 0 {
			latestEvidence = parsed.ResolveEvidence(observed[len(observed)-1].Evidence(parsed.Shape))
			return latestEvidence, false
		}
		var duplicate *service.CollabDuplicateSendError
		if errors.As(err, &duplicate) {
			return evidence, false
		}
		return resilience.CollabEvidence{}, false
	}
	if !l.Run(ctx) && latestEvidence.Grade == resilience.CollabEvidenceGradeVerified && ctx.Err() == nil && !waiters() && seam.localEnding != nil {
		reset()
		seam.localEnding(c, seam, saved, latestEvidence)
	}
}
