package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"go.uber.org/zap"
)

const ginKeyOpenAIQueueReselected = "xy_openai_queue_reselected"

type openAIQueueRejection struct {
	status                 int
	errType, code, message string
}

type openAIQueueRejectionKey struct{}

type openAIQueueReselectionContext struct {
	rejection *openAIQueueRejection
}

type openAIQueueSelector func(context.Context, map[int64]struct{}) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error)

func canTryOpenAIQueueReselection(c *gin.Context, hardOwner bool) bool {
	if hardOwner || c.Request.Context().Err() != nil || c.GetBool(ginKeyOpenAIQueueReselected) ||
		!service.OpenAIQueueReselectionAllowed(c.Request.Context()) || service.ProtocolTerminalClaimed(c) {
		return false
	}
	if deadline, ok := c.Request.Context().Deadline(); ok && !time.Now().Before(deadline) {
		return false
	}
	budget := requestBudget(c.Request.Context())
	return budget == nil || budget.AdmissionRemaining(time.Now(), false) > 0
}

func deferOpenAIQueueRejection(c *gin.Context, status int, errType, code, message string) bool {
	state, _ := c.Request.Context().Value(openAIQueueRejectionKey{}).(*openAIQueueReselectionContext)
	if state == nil || !canTryOpenAIQueueReselection(c, false) {
		return false
	}
	state.rejection = &openAIQueueRejection{status: status, errType: errType, code: code, message: message}
	return true
}

// tryOneQueueReselection owns only local admission. It does not consume/reset an
// upstream attempt budget or mark either account unhealthy.
func (h *OpenAIGatewayHandler) tryOneQueueReselection(
	c *gin.Context, groupID *int64, sessionHash string,
	selection **service.AccountSelectionResult, decision *service.OpenAIAccountScheduleDecision,
	excluded map[int64]struct{}, hardOwner bool, selectNext openAIQueueSelector,
	stream bool, streamStarted *bool, log *zap.Logger,
) (func(), openAISlotAcquireResult) {
	if !canTryOpenAIQueueReselection(c, hardOwner) {
		return h.acquireResponsesAccountSlot(c, groupID, sessionHash, *selection, stream, streamStarted, log)
	}
	originalCtx := c.Request.Context()
	state := &openAIQueueReselectionContext{}
	c.Request = c.Request.WithContext(context.WithValue(originalCtx, openAIQueueRejectionKey{}, state))
	release, result := h.acquireResponsesAccountSlot(c, groupID, sessionHash, *selection, stream, streamStarted, log)
	c.Request = c.Request.WithContext(originalCtx)
	if result != openAISlotAcquireQueueRejected || state.rejection == nil {
		return release, result
	}
	writeOriginal := func() (func(), openAISlotAcquireResult) {
		rejection := state.rejection
		applyConcurrencyRetryHint(c, rejection.status, rejection.code)
		h.handleStreamingAwareErrorWithCode(c, rejection.status, rejection.errType, rejection.code, rejection.message, *streamStarted, false)
		return nil, openAISlotAcquireFailed
	}
	if !canTryOpenAIQueueReselection(c, hardOwner) {
		return writeOriginal()
	}
	c.Set(ginKeyOpenAIQueueReselected, true)
	localExcluded := make(map[int64]struct{}, len(excluded)+1)
	for id := range excluded {
		localExcluded[id] = struct{}{}
	}
	localExcluded[(*selection).Account.ID] = struct{}{}
	next, nextDecision, err := selectNext(service.WithOpenAISelectionNoWait(originalCtx), localExcluded)
	if err != nil || next == nil || next.Account == nil || !next.Acquired || next.WaitPlan != nil {
		if next != nil && next.ReleaseFunc != nil {
			next.ReleaseFunc()
		}
		return writeOriginal()
	}
	// The NoWait selector already acquired an immediate slot; run the same
	// existing admission recheck, with any veto preserving the original 429.
	release, result = h.acquireOpenAIAccountSlot(c, groupID, sessionHash, next, stream, streamStarted, log, func(int, string, string, string) {})
	if result != openAISlotAcquireOK {
		return writeOriginal()
	}
	*selection, *decision = next, nextDecision
	log.Debug("openai.queue_reselected", zap.Int64("account_id", next.Account.ID), zap.Int("original_status", http.StatusTooManyRequests))
	return release, result
}
