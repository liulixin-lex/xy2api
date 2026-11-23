package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
	"math"
)

func (h *OpenAIGatewayHandler) wireCollabLocalEnding(seam *collabSeam) {
	if seam == nil {
		return
	}
	seam.terminalRejected = h.gatewayService.RecordOpenAICollabTerminalRejected
	seam.localEnding = func(c *gin.Context, seam *collabSeam, saved *collabWithheld, evidence resilience.CollabEvidence) {
		// A preemption has not exhausted the existing repair path yet.
		if saved == nil || seam.losslessDuration > 0 || seam.writer.current() == collabSeamPassing || !seam.localFinal && !service.LocalEndingBeforeDisposal(c.Request.Context()) {
			return
		}
		seam.quiesce(c)
		prelude := seam.writer.deliveredPrelude()
		if prelude.NextSequence < 0 || prelude.NextSequence > math.MaxInt64-8 || prelude.Created && !gjson.GetBytes(seam.body, "stream").Bool() {
			return
		}
		plan := h.gatewayService.ReserveOpenAICollabLocalEnding(c, evidence, saved.retryable)
		if plan == nil || c.Request.Context().Err() != nil {
			return
		}
		// All upstream readers have returned. Finish any remaining attempt
		// facts before local semantic bytes can become a latency observation.
		service.SealOpenAICollabAttemptForLocal(c.Request.Context())
		service.BeginOpenAICollabLocalResponse(c.Request.Context())
		response := plan.Response
		if prelude.Created {
			response = service.CollabLocalEndingResponseState{CreatedDelivered: true, ResponseID: prelude.ID, CreatedAt: prelude.CreatedAt, NextSequence: prelude.NextSequence}
		}
		for _, header := range []string{"Retry-After", "X-Should-Retry", "Content-Length", "X-Codex-Turn-State", service.EdgeUpstreamStatusHeader} {
			c.Writer.Header().Del(header)
		}
		complete := false
		defer func() {
			// A partially handed-off notice must never be followed by a retry
			// error, even when failure preceded its final frame.
			service.ClaimProtocolTerminal(c)
			plan.Finish(c.Request.Context(), complete)
		}()
		seam.writer.beginLocalEnding()
		complete, _ = service.WriteCollabLocalEnding(c.Request.Context(), c.Writer,
			response,
			gjson.GetBytes(seam.body, "model").String(), plan.Notice, gjson.GetBytes(seam.body, "stream").Bool(),
			func() bool { return service.ClaimProtocolTerminal(c) })
		complete = complete && service.OpenAICollabLocalWriteError(c.Request.Context()) == nil
	}
}

// Reservation has already succeeded. Local lifecycle frames are handed off
// individually, using their own identity and sequence without upstream replay.
func (w *collabSeamWriter) beginLocalEnding() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.held, w.commitHeader, w.continuation = nil, nil, nil
	w.forget()
	w.quiet = false
	w.takeover.Store(false)
	w.set(collabSeamPassing)
}

func (w *collabSeamWriter) FlushError() error {
	w.Flush()
	if observed, ok := w.ResponseWriter.(interface{ CollabDeliveryError() error }); ok {
		return observed.CollabDeliveryError()
	}
	return nil
}
