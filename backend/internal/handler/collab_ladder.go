package handler

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

func collabCanRescue(seam *collabSeam) *atomic.Bool {
	if seam == nil || seam.observer == nil {
		return nil
	}
	return seam.observer.RescuableFlag()
}

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
	parsed := seam.observer.Body()
	if parsed.Shape.Ambiguous || parsed.Shape.ContinuationPointer {
		return
	}
	evidence := parsed.ResolveEvidence(last.Evidence(parsed.Shape))
	if evidence.Named != nil && evidence.Named.Ambiguous || evidence.Grade != resilience.CollabEvidenceGradeVerified && evidence.Grade != resilience.CollabEvidenceGradeClaimed {
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
	defer func() { c.Request = parentRequest }()
	parent := resilience.StateFrom(parentRequest.Context())
	started := seam.started
	if budget := parent.Budget(); budget != nil {
		started = budget.Started()
	}
	deadline, _ := parentRequest.Context().Deadline()
	if !seam.idleDeadline.IsZero() && (deadline.IsZero() || seam.idleDeadline.Before(deadline)) {
		deadline = seam.idleDeadline
	}
	retryable := collabSavedRetryable(saved)
	if len(saved.ops) == 0 {
		retryable = last.Status == 408 || last.Status == 409 || last.Status == 429 || last.Status >= 500 || last.Source == resilience.CollabEvidenceSourceStreamError || last.Source == resilience.CollabEvidenceSourceAggregatedStream
	}
	saved.retryable = retryable
	duration := service.CollabTakeoverDuration(time.Now(), started, retryable, saved.committed, deadline)
	if seam.losslessDuration > 0 {
		duration = min(duration, seam.losslessDuration)
	}
	duration = min(duration, time.Minute-seam.optionalSpent)
	if duration <= 0 {
		reason := service.CollabLadderDeliveryUnavailable
		for _, rejection := range rejections {
			if evidence.Named != nil && rejection.AccountID == last.AccountID && rejection.Handled[evidence.Named.Identity] && !parsed.ProtectedNamedItem(evidence) {
				reason = service.CollabLadderHandledAgain
			}
		}
		if seam.localEnding != nil && parsed.LadderPolicy(evidence, reason).LocalEnding && parentRequest.Context().Err() == nil {
			seam.localEnding(c, seam, saved, evidence)
		}
		return
	}
	entered := time.Now()
	defer func() { seam.optionalSpent += time.Since(entered) }()
	var activeObserver atomic.Pointer[service.CollabSendObserver]
	var retainedWaiters func(context.Context) bool
	if seam.losslessDuration > 0 {
		// Lossless preemption holds the account that produced the latest rejection,
		// which may differ from the first account after ordinary failover.
		retained := last.AccountID
		retainedWaiters = seam.watchRetainedWaiters(func(query context.Context) bool {
			waiting, err := h.concurrencyHelper.concurrencyService.GetAccountWaitingCount(query, retained)
			return err != nil || waiting > 0
		})
	}
	waiters := func(query context.Context) bool {
		loads, err := h.concurrencyHelper.concurrencyService.GetUsersLoadBatch(query, []service.UserWithConcurrency{{ID: subject.UserID, MaxConcurrency: subject.Concurrency}})
		return err != nil || loads[subject.UserID] != nil && loads[subject.UserID].WaitingCount > 0 || retainedWaiters != nil && retainedWaiters(query)
	}
	ctx, stopWatch := watchCollabTakeover(parentRequest.Context(), duration, func() bool { return activeObserver.Load().Accepted() }, waiters, retainedWaiters)
	defer stopWatch()
	if ctx.Err() != nil {
		return
	}
	if seam.writer.deliveredPrelude().Created && !h.gatewayService.CanContinueCollabResponse(ctx) {
		return
	}
	child := resilience.NewCollabChildState(parent)
	settings := parent.CollabSettingsSnapshot().Clone()
	settings.Budget.MaxAttempts = math.MaxInt
	optionalBudget := resilience.NewBudget(ctx, &settings, resilience.KindText, time.Now())
	child.SetBudget(optionalBudget)
	ctx = resilience.WithRequestState(ctx, child)
	ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true})
	ctx = service.WithCollabAttemptIsolation(ctx)
	model := gjson.GetBytes(seam.body, "model").String()
	accounts := map[int64]*service.Account{}
	if seam.rejected == nil {
		seam.rejected = seam.observer.RejectedBodies()
	}
	rejected := seam.rejected
	for _, rejection := range rejections {
		seam.progress.RememberRejection(rejection)
		rejected[service.CollabRejectedBody{AccountID: rejection.AccountID, Digest: rejection.BodyDigest}] = true
	}
	seamKeys := c.Copy().Keys
	latestEvidence := evidence
	reset := func() {
		seam.resetAttempt(c)
		collabSeamSetKeys(c, seamKeys)
		collabSeamClearTerminal(c)
		service.IsolateCollabAttempt(c)
		c.Request = parentRequest.Clone(ctx)
	}
	l := service.CollabLadder{Body: parsed, First: rejections[0].AccountID, Current: last.AccountID, Progress: &seam.progress, ReturnToLegacy: seam.losslessDuration > 0, Evidence: evidence, Continue: func() bool { return ctx.Err() == nil && time.Since(entered) < duration }}
	if budget := parent.Budget(); budget != nil {
		l.MaxAttempts = budget.MaxAttempts()
		l.NamedReasoningAllowed = func(account int64) bool {
			// A terminal takeover cannot renew a reasoning repair that the
			// existing account exhausted under its in-place retry admission.
			return account != last.AccountID || budget.Attempts()+1 < budget.MaxAttempts()
		}
	}
	l.BeforeDisposal = func(_ context.Context, current resilience.CollabEvidence) bool {
		if seam.localEnding == nil || !parsed.LadderPolicy(current, service.CollabLadderFinalDisposal).LocalEnding || parentRequest.Context().Err() != nil {
			return false
		}
		reset()
		c.Request = parentRequest.Clone(resilience.WithCollabAttempt(parentRequest.Context(), resilience.CollabAttempt{Optional: true}))
		seam.localEnding(c, seam, saved, current)
		return seam.writer.current() == collabSeamPassing
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
	l.Sent = func() bool { observer := activeObserver.Load(); return observer != nil && observer.SendCount() > 0 }
	l.Send = func(ctx context.Context, id int64, body []byte, action resilience.CollabAction) (resilience.CollabEvidence, bool) {
		child = resilience.NewCollabChildState(parent)
		child.SetBudget(optionalBudget)
		ctx = resilience.WithRequestState(ctx, child)
		reset()
		observer := service.NewCollabSendObserver(parsed)
		activeObserver.Store(observer)
		// Leave half the available preparation window for final disposal.
		if action != resilience.CollabActionReplaceAll {
			remaining := duration - time.Since(entered)
			var stop func()
			ctx, stop = watchCollabTakeover(ctx, remaining/2, observer.Accepted, waiters, retainedWaiters)
			defer stop()
		}
		ctx = resilience.WithCollabAttempt(ctx, resilience.CollabAttempt{Optional: true, Action: action})
		attemptCtx := service.WithCollabSendObserver(service.WithCollabRejectedBodies(ctx, rejected), observer)
		c.Request = parentRequest.Clone(attemptCtx)
		var result *service.OpenAIForwardResult
		var err error
		defer func() {
			recovered := recover()
			defer func() { c.Request = parentRequest }()
			if seam.writer.current() == collabSeamPassing {
				c.Request = parentRequest.WithContext(resilience.WithCollabAttempt(parentRequest.Context(), resilience.CollabAttempt{Optional: true, Delivered: true}))
				parent.NoteGatewaySend(id)
				parent.AdoptCollabDelivery(id, child)
				seam.deliveredAccount = id
				persist := h.gatewayService.CommitCollabResponseIdentity(c, accounts[id], result, seam.writer.deliveredPrelude().ID)
				if persist != nil {
					defer persist()
				}
				if recovered != nil {
					err = errors.New("optional delivery interrupted")
				}
				seam.settle(accounts[id], result, err)
			}
			if recovered != nil {
				panic(recovered)
			}
		}()
		result, err = h.gatewayService.Forward(attemptCtx, c, accounts[id], body)
		if seam.writer.current() == collabSeamPassing {
			return resilience.CollabEvidence{}, true
		}

		observed := observer.Rejections()
		for _, rejection := range observed {
			seam.progress.RememberRejection(rejection)
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
		latestEvidence = resilience.CollabEvidence{}
		return resilience.CollabEvidence{}, false
	}
	if !l.Run(ctx) && parsed.LadderPolicy(l.LastEvidence, l.StopReason).LocalEnding {
		l.BeforeDisposal(parentRequest.Context(), latestEvidence)
	}
}

func collabSavedRetryable(saved *collabWithheld) bool {
	if saved == nil {
		return false
	}
	if saved.header.Get("x-should-retry") == "false" {
		return false
	}
	if saved.header.Get("x-should-retry") == "true" {
		return true
	}
	if saved.status == 408 || saved.status == 409 || saved.status == 429 || saved.status >= 500 {
		return true
	}
	var body []byte
	for _, op := range saved.ops {
		body = append(body, op.data...)
	}
	return parseOpsErrorResponse(body).StreamFailure
}

// deliveredAccountID is available to the request owner's thread-binding adapter.
func (s *collabSeam) deliveredAccountID() int64 {
	if s == nil {
		return 0
	}
	return s.deliveredAccount
}
