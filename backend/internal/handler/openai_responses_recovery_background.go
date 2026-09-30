package handler

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

func nativeResponseTerminalState(status string) responseturn.State {
	switch status {
	case "completed":
		return responseturn.StateCompleted
	case "failed":
		return responseturn.StateFailed
	case "incomplete":
		return responseturn.StatePartial
	case "cancelled":
		return responseturn.StateCancelled
	default:
		return ""
	}
}

// Retrieve, duplicate create and cancel share the same view of local lifecycle
// state. A saved queued document must never resurrect a cancelled execution.
func writeNativeResponseSnapshot(c *gin.Context, snapshot responseturn.Snapshot) {
	status := snapshot.HTTPStatus
	if status < 200 || status > 599 {
		status = http.StatusOK
	}
	active := snapshot.State == responseturn.StateRunning || snapshot.State == responseturn.StateDetached
	if len(snapshot.Result) > 0 && (active || status >= 400 || nativeResponseTerminalState(gjson.GetBytes(snapshot.Result, "status").String()) == snapshot.State) {
		c.Data(status, "application/json", snapshot.Result)
		return
	}
	state := string(snapshot.State)
	if snapshot.State == responseturn.StateDetached {
		state = "in_progress"
	}
	out := gin.H{"id": snapshot.UpstreamResponseID, "object": "response", "status": state, "model": snapshot.Model, "output": []any{}}
	if snapshot.State == responseturn.StateCancelled && snapshot.BackgroundAccepted && !snapshot.CancelConfirmed {
		out["error"] = gin.H{"code": "upstream_cancel_unconfirmed", "message": "Gateway execution has stopped; upstream cancellation is not confirmed"}
	}
	c.JSON(status, out)
}

func settleNativeResponseUsage(c *gin.Context, submit func()) {
	if raw, ok := c.Get(nativeResponseExecutionKey); ok {
		if execution, ok := raw.(*nativeResponseExecution); ok {
			execution.turn.SettleOnce(submit)
			return
		}
	}
	submit()
}

// Native background cancellation has a bounded upstream cancel/accounting
// phase. Its worker retains capacity through that phase, even after ctx ends.
func wrapNativeResponseRelease(c *gin.Context, ctx context.Context, release func()) func() {
	if raw, ok := c.Get(nativeResponseExecutionKey); ok {
		if execution, ok := raw.(*nativeResponseExecution); ok && execution.backgroundRequested && release != nil {
			var once sync.Once
			return func() { once.Do(release) }
		}
	}
	return wrapReleaseOnDone(ctx, release)
}

func (h *OpenAIGatewayHandler) validateNativeResponseAccess(ctx context.Context, scope responseturn.Scope, model string) error {
	if h.apiKeyService == nil {
		return &responseturn.Error{Code: "authorization_unavailable", Status: http.StatusServiceUnavailable, Message: "Current response access could not be verified"}
	}
	key, err := h.apiKeyService.RevalidateNativeResponseAccess(ctx, scope.APIKeyID)
	if errors.Is(err, service.ErrAPIKeyNotFound) {
		return responseturn.ErrNotFound
	}
	if err != nil {
		return &responseturn.Error{Code: "authorization_unavailable", Status: http.StatusServiceUnavailable, Message: "Current response access could not be verified"}
	}
	if key == nil || key.ID != scope.APIKeyID || key.UserID != scope.UserID || key.User == nil || key.User.ID != scope.UserID || !key.User.IsActive() {
		return responseturn.ErrNotFound
	}
	// Quota exhaustion/expiry prevents new generation, not access to an already
	// accepted execution. Explicit disable remains a permission revocation.
	if !key.IsActive() && key.Status != service.StatusAPIKeyExpired && key.Status != service.StatusAPIKeyQuotaExhausted {
		return responseturn.ErrNotFound
	}
	groupID := int64(0)
	if key.GroupID != nil {
		groupID = *key.GroupID
	}
	if groupID != scope.GroupID {
		return responseturn.ErrNotFound
	}
	if groupID != 0 {
		if key.Group == nil || key.Group.ID != groupID || !key.Group.IsActive() {
			return responseturn.ErrNotFound
		}
		if !key.Group.IsSubscriptionType() && !key.User.CanBindGroup(groupID, key.Group.IsExclusive) {
			return responseturn.ErrNotFound
		}
		if key.Group.ModelAllowlistEnabled() && !key.Group.ModelAllowlist.Allows(model) {
			return responseturn.ErrNotFound
		}
	}
	return nil
}

// Only immutable scope/model and execution context are retained. No original
// Gin context, HTTP writer or request body is referenced by the permission loop.
func (h *OpenAIGatewayHandler) watchNativeResponseAccess(turn *responseturn.Turn, done <-chan struct{}) context.CancelFunc {
	ctx, cancel := context.WithCancel(turn.Context())
	snapshot := turn.Snapshot()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				err := h.validateNativeResponseAccess(checkCtx, snapshot.Scope, snapshot.Model)
				stop()
				if err == nil {
					failures = 0
					continue
				}
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, responseturn.ErrNotFound) {
					turn.Cancel(responseturn.Reason("permission_revoked"))
					return
				}
				failures++
				if failures >= 3 {
					turn.Cancel(responseturn.Reason("authorization_unavailable"))
					return
				}
			}
		}
	}()
	return cancel
}

// This hook remains inside the original account-slot defer. A queued JSON
// response is delivered once, while only original-owner GET operations continue
// until a verified terminal document. Existing usage recording runs afterwards.
func (h *OpenAIGatewayHandler) completeNativeBackgroundResponse(c *gin.Context, account *service.Account, result *service.OpenAIForwardResult) (finalResult *service.OpenAIForwardResult, finalErr error) {
	raw, ok := c.Get(nativeResponseExecutionKey)
	if !ok {
		return result, nil
	}
	execution, ok := raw.(*nativeResponseExecution)
	if !ok {
		return result, nil
	}
	writer, ok := c.Writer.(*nativeJournalWriter)
	if !ok {
		return result, nil
	}
	writer.writeMu.Lock()
	body := append([]byte(nil), writer.jsonBody...)
	writer.writeMu.Unlock()
	if len(body) == 0 {
		return result, nil
	}
	status := gjson.GetBytes(body, "status").String()
	if terminal := nativeResponseTerminalState(status); terminal != "" {
		if result != nil {
			result.UpstreamTerminalEvent = "response." + status
		}
		return result, nil
	}
	if status != "queued" && status != "in_progress" {
		return nil, errors.New("native response has no verified protocol terminal")
	}
	var terminalBody []byte
	defer func() {
		if err := service.FinishNativeBackground(c.Request.Context(), terminalBody, finalErr); finalErr == nil {
			finalErr = err
		}
	}()
	supported, _ := account.Extra["openai_responses_supported"].(bool)
	responseID := gjson.GetBytes(body, "id").String()
	if !account.IsOpenAIApiKey() || !supported {
		return nil, errors.New("native background retrieval is unsupported by the selected account")
	}
	if err := execution.turn.AcceptBackground(account.ID, responseID, body, responseturn.Capabilities{Protocol: "responses_http", Verified: true, Retrieve: true}); err != nil {
		if execution.turn.Snapshot().State == responseturn.StateCancelled {
			finalResult, terminalBody = h.finishCancelledNativeBackground(execution.turn, account, result)
			return finalResult, err
		}
		return nil, err
	}
	writer.writeMu.Lock()
	writer.jsonHandled = true
	writer.writeMu.Unlock()
	if _, err := execution.turn.Publish(c.Request.Context(), responseturn.Event{Data: body, HTTPStatus: http.StatusOK, ResponseID: responseID}); err != nil {
		execution.turn.Cancel(responseturn.ReasonUpstreamFailure)
		finalResult, terminalBody = h.finishCancelledNativeBackground(execution.turn, account, result)
		return finalResult, err
	}
	finalResult, terminalBody, finalErr = h.pollNativeBackgroundResponse(c.Request.Context(), execution.turn, account, result)
	return finalResult, finalErr
}

func (h *OpenAIGatewayHandler) pollNativeBackgroundResponse(ctx context.Context, turn *responseturn.Turn, account *service.Account, initial *service.OpenAIForwardResult) (*service.OpenAIForwardResult, []byte, error) {
	ctx = service.NativeBackgroundExecutionContext(ctx)
	responseID := turn.Snapshot().UpstreamResponseID
	delay := 500 * time.Millisecond
	errorsInRow := 0
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancelNativeBackgroundContext(ctx, turn)
			result, body := h.finishCancelledNativeBackground(turn, account, initial)
			return result, body, context.Cause(ctx)
		case <-timer.C:
		}
		pollCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		doc, err := h.gatewayService.RetrieveNativeResponse(pollCtx, account, responseID)
		stop()
		if err != nil {
			if ctx.Err() != nil {
				cancelNativeBackgroundContext(ctx, turn)
				result, body := h.finishCancelledNativeBackground(turn, account, initial)
				return result, body, context.Cause(ctx)
			}
			errorsInRow++
			if errorsInRow >= 3 {
				turn.Cancel(responseturn.ReasonUpstreamFailure)
				result, body := h.finishCancelledNativeBackground(turn, account, initial)
				return result, body, err
			}
			delay = time.Duration(errorsInRow) * 500 * time.Millisecond
			var requestErr *service.NativeResponseReadError
			if errors.As(err, &requestErr) && requestErr.RetryAfter > delay {
				delay = requestErr.RetryAfter
			}
			continue
		}
		errorsInRow = 0
		delay = 500 * time.Millisecond
		if terminal := nativeResponseTerminalState(doc.Status); terminal != "" {
			if err := turn.Finish(terminal, "", doc.Body); err != nil {
				if errors.Is(err, responseturn.ErrTerminal) && turn.Snapshot().State == responseturn.StateCancelled && doc.Status == "cancelled" {
					_ = turn.RecordCancellationResult(doc.Body)
				} else if !errors.Is(err, responseturn.ErrTerminal) {
					return nil, nil, err
				}
			}
			return service.ApplyNativeResponseUsage(initial, doc), doc.Body, context.Cause(ctx)
		}
		if err := turn.UpdateBackgroundSnapshot(doc.Body); err != nil {
			turn.Cancel(responseturn.ReasonUpstreamFailure)
			result, body := h.finishCancelledNativeBackground(turn, account, initial)
			return result, body, err
		}
	}
}

func cancelNativeBackgroundContext(ctx context.Context, turn *responseturn.Turn) {
	reason := responseturn.Reason(service.ControlledStreamSnapshot(ctx).CancelReason)
	if reason == "" {
		reason = responseturn.ReasonAdminCancel
		if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			reason = responseturn.ReasonDeadline
		}
	}
	turn.Cancel(reason)
}

func (h *OpenAIGatewayHandler) finishCancelledNativeBackground(turn *responseturn.Turn, account *service.Account, initial *service.OpenAIForwardResult) (*service.OpenAIForwardResult, []byte) {
	h.cancelAcceptedNativeResponse(turn)
	// Bounded accounting cleanup may retrieve final usage, but it never resumes
	// generation or restores a cancelled local execution.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		doc, err := h.gatewayService.RetrieveNativeResponse(ctx, account, turn.Snapshot().UpstreamResponseID)
		if err == nil && nativeResponseTerminalState(doc.Status) != "" {
			if doc.Status == "cancelled" {
				_ = turn.RecordCancellationResult(doc.Body)
			}
			return service.ApplyNativeResponseUsage(initial, doc), doc.Body
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}
