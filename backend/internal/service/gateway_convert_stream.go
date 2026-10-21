package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/util/responseheaders"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Resilience of the Gateway conversions of an Anthropic upstream (W1.1,
// W1.2): Responses and Chat Completions clients answered by an Anthropic
// account through ForwardAsResponses and ForwardAsChatCompletions.
//
// The conversions used to commit 200 and write response.created or the
// Chat role chunk as soon as message_start arrived, drop the upstream's
// error events and turn any end of the stream, message_stop or not, into a
// completed response. With stream.prelude_staging the opening is held until
// the first content or terminal event (resilience.ClassifyStreamEvent), so
// a retryable failure before it switches accounts; the first-event deadline
// of the attempt runs until then. With stream.strict_terminal a stream that
// breaks after output ends with the inbound protocol's terminal frame
// (response.failed, or data:{error} and [DONE]) and is never reported as a
// success. With both switches off the conversions keep the 0.4.4 behavior.
//
// A client that leaves before the first output cancels the attempt: no
// switch, no charge, no account failure. After output the upstream is read
// to its end for the usage.

const (
	gatewayConvertResponses = "responses"
	gatewayConvertChat      = "chat"
)

// ErrGatewayConvertClientGone ends an attempt whose client left before any
// output: nothing is switched or billed.
var ErrGatewayConvertClientGone = errors.New("stream usage incomplete: client disconnected before output")

// gatewayConvertGuarded reports whether the Gateway conversions use the
// staged opening or strict terminal handling.
func gatewayConvertGuarded() bool {
	cfg := resilience.Current()
	return cfg.PreludeStagingOn() || cfg.StrictTerminalOn()
}

// gatewayConvertHooks are the protocol conversion of one attempt.
type gatewayConvertHooks struct {
	// convert turns one Anthropic event into wire frames. hold is set while
	// the opening is staged; the driver keeps the frames until it ends.
	convert func(event *apicompat.AnthropicStreamEvent, payload string, hold bool) []string
	// finalize returns the frames that end a stream treated as complete.
	finalize func() []string
	// failure returns the inbound protocol's terminal frames; source is an
	// Anthropic error event body.
	failure func(source []byte) []string
}

type gatewayConvertStream struct {
	s           *GatewayService
	c           *gin.Context
	resp        *http.Response
	account     *Account
	protocol    string
	model       string
	requestID   string
	hooks       gatewayConvertHooks
	staging     bool
	strict      bool
	guarded     bool
	firstEvent  *firstEventGuard
	guardFired  bool
	guardDone   bool
	stopCancel  func() bool
	bodyClosed  atomic.Bool
	preludeLog  *resilience.RequestState
	upstreamHdr bool
	held        []string
	released    bool
	sawStop     bool
	sawFailure  bool
	lineSeen    bool
	awaitData   bool
	eventName   string
	written     bool
	gone        bool
	lastWrite   time.Time
}

// gatewayConvertOutcome is the end of an attempt; noResult drops the usage.
type gatewayConvertOutcome struct {
	err      error
	noResult bool
}

func (s *GatewayService) newGatewayConvertStream(c *gin.Context, resp *http.Response, account *Account, protocol, model string, hooks gatewayConvertHooks) *gatewayConvertStream {
	cfg := resilience.Current()
	st := &gatewayConvertStream{
		s: s, c: c, resp: resp, account: account, protocol: protocol, model: model,
		requestID: resp.Header.Get("x-request-id"), hooks: hooks,
		staging: cfg.PreludeStagingOn(), strict: cfg.StrictTerminalOn(),
		stopCancel: func() bool { return false }, lastWrite: time.Now(),
	}
	st.guarded = st.staging || st.strict
	if st.guarded && resp.Request != nil {
		st.firstEvent = firstEventGuardFrom(resp.Request.Context())
	}
	return st
}

func (st *gatewayConvertStream) ctx() context.Context {
	if st.c != nil && st.c.Request != nil {
		return st.c.Request.Context()
	}
	return context.Background()
}

// run reads the attempt to its end.
func (st *gatewayConvertStream) run() gatewayConvertOutcome {
	if !st.guarded {
		st.headers(true)
	} else {
		resilience.StateFrom(st.ctx()).SetStage(resilience.StagePreOutput)
	}
	st.preludeLog = beginOpenAIPreludeTrace(st.ctx(), st.staging)
	if st.staging && st.c != nil && st.c.Request != nil {
		// Before the first output a departed client ends the attempt; the
		// upstream context is detached, so close the body ourselves. The flag
		// is set first: a cancel racing the first write runs this although
		// output started, and the read error that follows is that cancel.
		st.stopCancel = context.AfterFunc(st.ctx(), func() {
			st.bodyClosed.Store(true)
			_ = st.resp.Body.Close()
		})
		defer func() {
			if st.stopCancel() && st.ctx().Err() != nil {
				_ = st.resp.Body.Close()
			}
		}()
	}

	maxLineSize := defaultMaxLineSize
	streamInterval, keepaliveInterval := time.Duration(0), time.Duration(0)
	if cfg := st.s.cfg; cfg != nil {
		if cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = cfg.Gateway.MaxLineSize
		}
		streamInterval = time.Duration(cfg.Gateway.StreamDataIntervalTimeout) * time.Second
		keepaliveInterval = time.Duration(cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	scanner := bufio.NewScanner(st.resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	type upstreamLine struct {
		line string
		err  error
	}
	lines := make(chan upstreamLine, 16)
	done := make(chan struct{})
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())
	go func() {
		defer close(lines)
		for scanner.Scan() {
			lastReadAt.Store(time.Now().UnixNano())
			select {
			case lines <- upstreamLine{line: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- upstreamLine{err: err}:
			case <-done:
			}
		}
	}()
	defer close(done)

	var stallCh, keepaliveCh <-chan time.Time
	if base := resilienceStallCheckBase(streamInterval); st.strict && base > 0 {
		ticker := time.NewTicker(resilienceStallCheckPeriod(base))
		defer ticker.Stop()
		stallCh = ticker.C
	}
	if st.staging && keepaliveInterval > 0 {
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		keepaliveCh = ticker.C
	}

	for {
		select {
		case ev, ok := <-lines:
			if !ok {
				return st.onEnd(nil)
			}
			if ev.err != nil {
				return st.onEnd(ev.err)
			}
			if out, stop := st.onLine(ev.line); stop {
				return out
			}
		case <-stallCh:
			threshold := resilienceStallThreshold(streamInterval, st.outputStarted())
			if threshold <= 0 || time.Since(time.Unix(0, lastReadAt.Load())) < threshold {
				continue
			}
			// Before output an armed first-event deadline owns the attempt.
			if resilienceFirstOutputGuardOwnsStall(st.firstEvent != nil && !st.guardDone, st.outputStarted()) {
				continue
			}
			return st.onStall(threshold)
		case <-keepaliveCh:
			if st.released || time.Since(st.lastWrite) < keepaliveInterval {
				continue
			}
			st.heartbeat(":\n\n")
		}
	}
}

// onLine keeps the conversions' framing: an event line, then its data line.
func (st *gatewayConvertStream) onLine(line string) (gatewayConvertOutcome, bool) {
	if !st.lineSeen {
		st.lineSeen = true
		if st.guarded && !st.staging && st.stopFirstEvent() {
			return st.deadline(), true
		}
	}
	if st.awaitData {
		st.awaitData = false
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok {
			return gatewayConvertOutcome{}, false
		}
		return st.onEvent(st.eventName, payload)
	}
	if name, ok := extractOpenAISSEEventLine(line); ok {
		st.eventName, st.awaitData = name, true
		return gatewayConvertOutcome{}, false
	}
	// Upstream liveness reaches the client at once while the opening is held
	// (review R2-P1③); the comment text itself stays private.
	if st.guarded && !st.released && strings.HasPrefix(line, ":") {
		st.heartbeat(":\n\n")
	}
	return gatewayConvertOutcome{}, false
}

func (st *gatewayConvertStream) onEvent(name, payload string) (gatewayConvertOutcome, bool) {
	var event apicompat.AnthropicStreamEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		logger.L().Warn("gateway convert stream: failed to parse event", zap.Error(err),
			zap.String("request_id", st.requestID), zap.String("protocol", st.protocol), zap.String("event_type", name))
		return gatewayConvertOutcome{}, false
	}
	if st.canceledBeforeOutput() {
		return gatewayConvertOutcome{err: st.ctx().Err(), noResult: true}, true
	}
	if event.Type == "ping" {
		if st.protocol == gatewayConvertChat {
			st.heartbeat(": ping\n\n")
		} else {
			st.hooks.convert(&event, payload, false)
			if st.guarded && !st.released {
				st.heartbeat(":\n\n")
			}
		}
		return gatewayConvertOutcome{}, !st.guarded && st.gone
	}
	class := resilience.ClassifyStreamEvent("messages", name, []byte(payload))
	hold := st.staging && !st.released && class.Kind == resilience.Prelude
	if event.Type == "message_start" && st.staging && !st.released {
		st.preludeLog.NotePreludeCreated(time.Now())
	}
	if st.guarded && !hold && !st.released {
		st.released = true
		// Stop the deadline on content even after a failed heartbeat, so it
		// cannot cut the drain of a departed client's usage (PM7).
		if st.staging && st.stopFirstEvent() {
			return st.deadline(), true
		}
		if st.staging {
			releaseOpenAIPrelude(st.preludeLog, event.Type)
		}
	}
	if st.guarded && class.Kind == resilience.TerminalFailure {
		return st.onErrorEvent(&event, payload, class.Retryable)
	}
	// A stop_reason already ends the message; some compatible upstreams
	// close the stream without the message_stop that follows it.
	if event.Type == "message_stop" || (event.Type == "message_delta" && event.Delta != nil && event.Delta.StopReason != "") {
		st.sawStop = true
	}
	frames := st.hooks.convert(&event, payload, hold)
	if hold {
		st.held = append(st.held, frames...)
		return gatewayConvertOutcome{}, false
	}
	st.release()
	st.write(frames)
	if len(frames) == 0 && st.protocol == gatewayConvertChat && !st.gone && st.c.Writer.Written() {
		// 0.4.4 flushed the Chat stream after every converted event.
		st.c.Writer.Flush()
	}
	return gatewayConvertOutcome{}, !st.guarded && st.gone
}

// onErrorEvent handles an Anthropic error event of a guarded stream.
func (st *gatewayConvertStream) onErrorEvent(event *apicompat.AnthropicStreamEvent, payload string, retryable bool) (gatewayConvertOutcome, bool) {
	body := []byte(payload)
	if st.canReplay() && retryable {
		status := st.recordStreamError(body, false)
		return gatewayConvertOutcome{err: &UpstreamFailoverError{StatusCode: status, ResponseBody: body}, noResult: true}, true
	}
	if !st.strict {
		// 0.4.4: the event is dropped and the stream goes on. Its end is
		// then no reason to replay (review DELTA-P2-1).
		st.sawFailure = true
		st.write(st.hooks.convert(event, payload, false))
		return gatewayConvertOutcome{}, false
	}
	outputStarted := st.outputStarted()
	st.recordStreamError(body, outputStarted)
	if outputStarted {
		st.s.resilienceAnthropicFailedAfterOutput(st.ctx(), st.c, st.account, st.resp, &sseStreamErrorEventError{RawData: payload})
	}
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	if st.gone {
		MarkResponseCommitted(st.c)
		return gatewayConvertOutcome{err: fmt.Errorf("stream usage incomplete after disconnect: upstream error: %s", message)}, true
	}
	st.terminal(body)
	return gatewayConvertOutcome{err: fmt.Errorf("upstream stream error: %s", message)}, true
}

// onEnd handles the end of the upstream: EOF (cause nil) or a read error.
func (st *gatewayConvertStream) onEnd(cause error) gatewayConvertOutcome {
	if st.sawStop || st.sawFailure || !st.guarded {
		if cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			logger.L().Warn("gateway convert stream: read error", zap.Error(cause),
				zap.String("request_id", st.requestID), zap.String("protocol", st.protocol))
		}
		return st.finish()
	}
	if cause != nil && st.firstEvent.Fired() && !st.released {
		return st.deadline()
	}
	if st.canceledBeforeOutput() {
		return gatewayConvertOutcome{err: st.ctx().Err(), noResult: true}
	}
	canceled := errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
	if st.canReplay() && !canceled {
		message := "Anthropic stream ended before content or terminal"
		if cause != nil {
			message = "Anthropic stream read failed before content"
		}
		return st.failover(http.StatusBadGateway, "upstream_disconnected", message)
	}
	if st.bodyClosed.Load() {
		// The client's cancel closed the body after output started
		// (INTEG-P2-2): a client disconnect with the usage so far.
		st.gone = true
		MarkResponseCommitted(st.c)
		return gatewayConvertOutcome{err: fmt.Errorf("stream usage incomplete: %w", st.ctx().Err())}
	}
	if !st.strict {
		return st.finish()
	}
	if st.gone || canceled {
		MarkResponseCommitted(st.c)
		if cause == nil {
			return gatewayConvertOutcome{err: errors.New("stream usage incomplete: missing terminal event")}
		}
		return gatewayConvertOutcome{err: fmt.Errorf("stream usage incomplete: %w", cause)}
	}
	resilienceStreamBrokenAfterOutput(st.ctx(), st.s.rateLimitService, st.account, st.resp, cause, st.outputStarted())
	if cause == nil {
		st.terminal(gatewayConvertErrorSource("api_error", "upstream_disconnected", "Upstream stream ended before completion"))
		return gatewayConvertOutcome{err: errors.New("stream usage incomplete: missing terminal event")}
	}
	logger.L().Warn("gateway convert stream: read error after output", zap.Error(cause),
		zap.String("request_id", st.requestID), zap.String("protocol", st.protocol))
	st.terminal(gatewayConvertErrorSource("api_error", "stream_read_error", "upstream stream disconnected: "+sanitizeStreamError(cause)))
	return gatewayConvertOutcome{err: fmt.Errorf("stream read error: %w", cause)}
}

func (st *gatewayConvertStream) onStall(threshold time.Duration) gatewayConvertOutcome {
	if st.gone {
		MarkResponseCommitted(st.c)
		return gatewayConvertOutcome{err: errors.New("stream usage incomplete after timeout")}
	}
	logger.L().Warn("gateway convert stream: data interval timeout", zap.String("request_id", st.requestID),
		zap.String("protocol", st.protocol), zap.String("model", st.model), zap.Duration("interval", threshold))
	if st.s.rateLimitService != nil && st.account != nil {
		st.s.rateLimitService.HandleStreamTimeout(st.ctx(), st.account, st.model)
	}
	noteResilienceStall(st.ctx(), st.s.rateLimitService, st.account)
	if st.canReplay() {
		return st.failover(http.StatusBadGateway, "stream_timeout", "Anthropic stream stalled before content")
	}
	st.terminal(gatewayConvertErrorSource("api_error", "stream_timeout", fmt.Sprintf("upstream stream idle for %s", threshold)))
	return gatewayConvertOutcome{err: errors.New("stream data interval timeout")}
}

// deadline ends an attempt whose first-event deadline passed before content.
func (st *gatewayConvertStream) deadline() gatewayConvertOutcome {
	if st.gone || st.ctx().Err() != nil {
		MarkResponseCommitted(st.c)
		return gatewayConvertOutcome{err: ErrGatewayConvertClientGone, noResult: true}
	}
	return gatewayConvertOutcome{err: st.firstEvent.failoverError(st.c, st.account, "first_event"), noResult: true}
}

// stopFirstEvent stops the deadline once and reports whether it had fired.
func (st *gatewayConvertStream) stopFirstEvent() bool {
	if st.guardDone || st.firstEvent == nil {
		st.guardDone = true
		return st.guardFired
	}
	st.guardDone = true
	st.guardFired = !st.firstEvent.timer.Stop() || st.firstEvent.Fired()
	return st.guardFired
}

func (st *gatewayConvertStream) failover(status int, code, message string) gatewayConvertOutcome {
	if st.account != nil {
		appendOpsUpstreamError(st.c, OpsUpstreamErrorEvent{
			ProxyID: opsUpstreamProxyID(st.account), ProxyName: opsUpstreamProxyName(st.account),
			Platform: st.account.Platform, AccountID: st.account.ID, AccountName: st.account.Name,
			UpstreamStatusCode: status, UpstreamRequestID: st.requestID, Kind: "failover", Message: message,
		})
	}
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": code, "message": message}})
	return gatewayConvertOutcome{err: &UpstreamFailoverError{StatusCode: status, ResponseBody: body, RetryableOnSameAccount: true}, noResult: true}
}

// recordStreamError applies the side effects of an upstream error event, as
// Forward does for the native stream, and returns its semantic status.
func (st *gatewayConvertStream) recordStreamError(body []byte, outputWritten bool) int {
	status, sideEffectHeader := resilienceStreamErrorStatus(gjson.GetBytes(body, "error.type").String(), outputWritten, st.resp.Header)
	if st.account == nil {
		return status
	}
	if sideEffectHeader != nil && st.s.rateLimitService != nil {
		synthetic := &http.Response{StatusCode: status, Header: sideEffectHeader, Body: io.NopCloser(strings.NewReader(string(body)))}
		st.s.handleFailoverSideEffects(st.ctx(), synthetic, st.account, st.model)
	}
	detail := ""
	if cfg := st.s.cfg; cfg != nil && cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(body), maxBytes)
	}
	appendOpsUpstreamError(st.c, OpsUpstreamErrorEvent{
		ProxyID: opsUpstreamProxyID(st.account), ProxyName: opsUpstreamProxyName(st.account),
		Platform: st.account.Platform, AccountID: st.account.ID, AccountName: st.account.Name,
		UpstreamStatusCode: status, UpstreamRequestID: st.requestID, Kind: "stream_error",
		Message: sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body))), Detail: detail,
	})
	return status
}

// finish ends a stream treated as complete: the held opening, then the
// protocol's closing frames.
func (st *gatewayConvertStream) finish() gatewayConvertOutcome {
	st.release()
	st.write(st.hooks.finalize())
	return gatewayConvertOutcome{}
}

// terminal writes the inbound protocol's failure frames after the held
// opening and keeps the handler from appending another terminal.
func (st *gatewayConvertStream) terminal(source []byte) {
	st.release()
	st.write(st.hooks.failure(source))
	errType := strings.TrimSpace(gjson.GetBytes(source, "error.type").String())
	MarkOpsStreamError(st.c, errType, strings.TrimSpace(gjson.GetBytes(source, "error.message").String()), http.StatusBadGateway)
	MarkResponseCommitted(st.c)
}

func (st *gatewayConvertStream) release() {
	if len(st.held) == 0 {
		return
	}
	held := st.held
	st.held = nil
	st.write(held)
}

func (st *gatewayConvertStream) canceledBeforeOutput() bool {
	return st.staging && !st.outputStarted() && st.ctx().Err() != nil
}

// canReplay reports whether the attempt may still switch accounts: nothing
// but heartbeats reached a client that is still there.
func (st *gatewayConvertStream) canReplay() bool {
	return st.guarded && !st.gone && st.ctx().Err() == nil && !st.outputStarted()
}

func (st *gatewayConvertStream) outputStarted() bool {
	return openAIStreamClientOutputStarted(st.c, st.written)
}

// headers commits the SSE headers. The upstream's own headers belong to the
// attempt and are only added with its first semantic output.
func (st *gatewayConvertStream) headers(semantic bool) {
	w := st.c.Writer
	if semantic && !st.upstreamHdr && !w.Written() {
		st.upstreamHdr = true
		if st.s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(w.Header(), st.resp.Header, st.s.responseHeaderFilter)
		}
	}
	if w.Written() {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func (st *gatewayConvertStream) write(frames []string) {
	if st.gone || len(frames) == 0 {
		return
	}
	st.headers(true)
	for _, frame := range frames {
		n, err := st.c.Writer.Write([]byte(frame))
		if n > 0 && !st.written {
			st.written = true
			// After the first output a departed client no longer stops the
			// upstream: its usage is read to the end (review P0).
			st.stopCancel()
		}
		if err == nil && n != len(frame) {
			err = io.ErrShortWrite
		}
		if err != nil {
			st.gone = true
			logger.L().Info("gateway convert stream: client disconnected, continuing to drain upstream for billing",
				zap.String("request_id", st.requestID), zap.String("protocol", st.protocol))
			return
		}
	}
	st.c.Writer.Flush()
	st.lastWrite = time.Now()
}

// heartbeat writes a comment; it commits 200 but is no output.
func (st *gatewayConvertStream) heartbeat(frame string) {
	if st.gone {
		return
	}
	st.headers(false)
	n, err := st.c.Writer.Write([]byte(frame))
	if st.guarded {
		recordOpenAIStreamKeepaliveBytes(st.c, n)
	}
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		st.gone = true
		logger.L().Info("gateway convert stream: client disconnected during heartbeat",
			zap.String("request_id", st.requestID), zap.String("protocol", st.protocol))
		return
	}
	st.c.Writer.Flush()
	st.lastWrite = time.Now()
}

// gatewayConvertErrorSource is an Anthropic error event body for a failure
// the gateway detected itself.
func gatewayConvertErrorSource(errType, code, message string) []byte {
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": errType, "code": code, "message": message}})
	return body
}

// gatewayResponsesFailedFrames ends a converted Responses stream with
// response.failed. A stream that sent nothing opens with response.created
// first: strict SDKs reject any other first event.
func gatewayResponsesFailedFrames(ctx context.Context, state *apicompat.AnthropicEventToResponsesState, model string, source []byte) []string {
	var frames []string
	if !state.CreatedSent {
		state.CreatedSent = true
		if state.ResponseID == "" {
			state.ResponseID = "resp_" + randomKeepaliveHex(12)
		}
		frames = append(frames, string(ResponsesCreatedFrame(state.ResponseID, model, state.Created)))
		state.SequenceNumber = 1
	}
	frame := buildOpenAIResponseFailedSSE(ctx, state.ResponseID, model, source, "Upstream response failed")
	payload := strings.TrimSuffix(strings.TrimPrefix(frame, "event: response.failed\ndata: "), "\n\n")
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err == nil {
		event["sequence_number"] = state.SequenceNumber
		if response, ok := event["response"].(map[string]any); ok {
			response["created_at"] = state.Created
		}
		if encoded, err := json.Marshal(event); err == nil {
			frame = "event: response.failed\ndata: " + string(encoded) + "\n\n"
		}
	}
	state.SequenceNumber++
	state.CompletedSent = true
	return append(frames, frame)
}

// gatewayChatFailureFrames ends a converted Chat stream with an error chunk
// and [DONE].
func gatewayChatFailureFrames(source []byte) []string {
	errType := strings.TrimSpace(gjson.GetBytes(source, "error.type").String())
	if errType == "" {
		errType = "api_error"
	}
	message := strings.TrimSpace(gjson.GetBytes(source, "error.message").String())
	if message == "" {
		message = "Upstream response failed"
	}
	errObj := map[string]any{"type": errType, "message": message}
	if code := strings.TrimSpace(gjson.GetBytes(source, "error.code").String()); code != "" {
		errObj["code"] = code
	}
	payload, _ := json.Marshal(map[string]any{"error": errObj})
	return []string{"data: " + string(payload) + "\n\n", "data: [DONE]\n\n"}
}

// armGatewayConvertFirstEvent derives the attempt context of a guarded
// streaming conversion with the Anthropic first-event deadline; the guard
// is nil (inert) otherwise. The caller releases it when the attempt ends.
func (s *GatewayService) armGatewayConvertFirstEvent(ctx, upstreamCtx context.Context, account *Account, clientStream bool) (context.Context, *firstEventGuard) {
	if !clientStream || account == nil || !gatewayConvertGuarded() {
		return upstreamCtx, nil
	}
	return newFirstEventGuard(upstreamCtx, resilienceFirstEventTimeout(ctx, account.ID, account.Platform))
}
