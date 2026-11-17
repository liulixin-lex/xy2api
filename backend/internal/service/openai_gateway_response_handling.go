package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/pkg/xai"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/liulixin-lex/xy2api/internal/util/responseheaders"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// openaiStreamingResult streaming response result
type openaiStreamingResult struct {
	ResponseFactsKnown bool
	UsagePresent       bool
	UsageSource        string
	TerminalSeen       bool
	TerminalValid      bool
	ResponseBodyKind   string

	usage            *OpenAIUsage
	firstTokenMs     *int
	responseID       string
	imageCount       int
	imageOutputSizes []string
	searchCount      int
	clientSawOutput  bool
	upstreamAccepted bool
}

// openAISemanticWriteTracker observes actual downstream writes, including a
// partial write followed by an error. The first semantic data line owns a
// range in the queued stream; opening and terminal frames never own it.
type openAISemanticWriteTracker struct {
	io.Writer
	written       int64
	semanticStart int64
	semanticEnd   int64
	sawOutput     bool
}

func (w *openAISemanticWriteTracker) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 && w.semanticEnd > w.semanticStart && w.written < w.semanticEnd && w.written+int64(n) > w.semanticStart {
		w.sawOutput = true
	}
	w.written += int64(n)
	return n, err
}

type openaiNonStreamingResult struct {
	ResponseFactsKnown bool
	UsagePresent       bool
	UsageSource        string
	TerminalSeen       bool
	TerminalValid      bool
	ResponseBodyKind   string

	*OpenAIUsage
	upstreamAccepted bool
	usage            *OpenAIUsage
	responseID       string
	imageCount       int
	imageOutputSizes []string
	searchCount      int
}

func (s *OpenAIGatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel string) (*openaiStreamingResult, error) {
	return s.handleStreamingResponseWithReasoning(ctx, resp, c, account, startTime, originalModel, mappedModel, "")
}

func (s *OpenAIGatewayService) handleStreamingResponseWithReasoning(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel, reasoningEffort string) (schedResult *openaiStreamingResult, schedErr error) {
	usageSource := "missing"
	responsesObserved := false
	var currentTerminal, observedTerminal OpenAIResponsesTerminal
	typedBareErrorRecoverable := false
	defer func() {
		if !responsesObserved {
			return
		}
		if schedResult != nil {
			schedResult.ResponseFactsKnown, schedResult.UsagePresent, schedResult.UsageSource = true, usageSource != "missing", usageSource
			schedResult.TerminalSeen, schedResult.TerminalValid, schedResult.ResponseBodyKind = observedTerminal.Seen, observedTerminal.Valid, "sse"
		}
		noteOpenAIResponsesFacts(c, account, usageSource, observedTerminal, "sse")
	}()

	schedCapture := currentSchedAttempt(ctx)
	defer func() {
		if schedResult != nil {
			schedCapture.noteUsage(schedResult.usage)
		}
		schedCapture.noteError(schedErr)
	}()
	preludeTrace := beginOpenAIPreludeTrace(ctx, resilience.Current().NativePreludeWhitelistOn())
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	// xy2api resilience: upstream headers arrived (design §8.2 stages).
	resilience.StateFrom(ctx).SetStage(resilience.StagePreOutput)
	firstOutputTimeout := time.Duration(0)
	var firstOutputPlan resilience.TimeoutPlan
	if account != nil && account.Platform == PlatformOpenAI {
		firstOutputTimeout, firstOutputPlan = s.openAIFirstOutputPlanFor(ctx, account.ID, originalModel, reasoningEffort, startTime)
	}
	guardFirstOutput := firstOutputTimeout > 0
	stageFirstOutput := account != nil && account.Platform == PlatformOpenAI
	var attemptResponseHeaders http.Header
	if stageFirstOutput {
		if s.responseHeaderFilter != nil {
			attemptResponseHeaders = responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter)
		} else if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
			attemptResponseHeaders = http.Header{"X-Request-Id": []string{requestID}}
		}
	} else if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// x-codex-turn-state 不在通用响应头白名单内，按 Codex 协议显式回传：
	// 客户端会在同回合的后续请求中回带（openai_codex_turn_state.go）。
	// OpenAI 首个语义输出前暂存；首次心跳或语义输出提交 turn-state 时同步记录溯源。
	if stageFirstOutput {
		stageOpenAICodexTurnState(&attemptResponseHeaders, resp.Header)
	} else {
		s.relayOpenAICodexTurnState(c, account, resp.Header)
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if !stageFirstOutput && resp.Header.Get("x-request-id") != "" {
		v := resp.Header.Get("x-request-id")
		c.Header("x-request-id", v)
	}
	applyAttemptResponseHeaders := func() {
		if !stageFirstOutput || len(attemptResponseHeaders) == 0 || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		// 暂存头此刻才真正写给客户端：turn-state 溯源在这里记录（见
		// noteStagedOpenAICodexTurnStateCommitted 的 failover 说明）。
		s.noteStagedOpenAICodexTurnStateCommitted(c, account, attemptResponseHeaders)
		// These headers describe this gateway's SSE stream and are stable across
		// account attempts. Keep them authoritative over upstream values.
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	var firstTokenMs *int
	ttftMode := s.openAITTFTMode(ctx)
	firstOutputProgressObserved := false
	delivery := &openAISemanticWriteTracker{Writer: w}
	bufferedWriter := bufio.NewWriterSize(delivery, 4*1024)
	var queuedBytes int64
	var firstOutputStage *openAIFirstOutputStage
	if stageFirstOutput {
		firstOutputStage = newDefaultOpenAIFirstOutputStage()
		defer func() {
			if err := firstOutputStage.Close(); err != nil {
				logger.LegacyPrintf("service.openai_gateway", "OpenAI first-output staging cleanup failed: account=%d model=%s error=%v", account.ID, originalModel, err)
			}
		}()
	}
	writePendingString := func(value string) (n int, err error) {
		defer func() { queuedBytes += int64(n) }()
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.WriteString(value)
		}
		return bufferedWriter.WriteString(value)
	}
	pendingBytes := func() int64 {
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.Buffered()
		}
		return int64(bufferedWriter.Buffered())
	}
	flushBuffered := func() error {
		if firstOutputStage != nil && !firstOutputStage.closed {
			if err := firstOutputStage.CommitTo(delivery); err != nil {
				return err
			}
		} else {
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		flusher.Flush()
		return nil
	}
	discardPendingWrites := func() error {
		if firstOutputStage != nil && !firstOutputStage.closed {
			if err := firstOutputStage.Reset(); err != nil {
				return err
			}
		}
		bufferedWriter.Reset(delivery)
		queuedBytes = delivery.written
		return nil
	}

	usage := &OpenAIUsage{}
	tailMetered := false
	var finishResponsesFrame func()
	var releaseFrameScan func()
	responsesEOF := false
	responsesEOFUnterminated := false
	responsesFrameBytes := 0
	responseCreatedAt := int64(0)
	imageCounter := newOpenAIImageOutputCounter()
	// xy2api resilience: image generation keeps the image stall threshold.
	imageGenerationSeen := false
	responseID := ""
	var firstOutputScanGuard atomic.Bool
	firstOutputScanGuard.Store(stageFirstOutput)
	relayLive := newRelayLiveness(ctx, account)
	scanner := bufio.NewScanner(newOpenAIRelayKeepaliveFilter(resp.Body, relayLive))
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	if stageFirstOutput {
		scanner.Split(openAIFirstOutputDynamicScanLines(&firstOutputScanGuard))
	}
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	// Grok: always enforce an upstream-read idle so hung SSE bodies fail over
	// instead of holding the OAuth slot until the client cancels. Prefer the
	// global gateway setting when set; otherwise apply a Grok-only default.
	if account != nil && account.Platform == PlatformGrok {
		cfgSec := 0
		if s.cfg != nil {
			cfgSec = s.cfg.Gateway.StreamDataIntervalTimeout
		}
		streamInterval = resolveGrokStreamIdleTimeout(cfgSec)
	}
	// 仅监控上游数据间隔超时，不被下游写入阻塞影响
	// xy2api resilience: check often enough to notice a stall within 5 s,
	// and apply the text stall threshold after output (design §7.5).
	stallCheckBase := resilienceStallCheckBase(streamInterval)
	var intervalTicker *time.Ticker
	if stallCheckBase > 0 {
		intervalTicker = time.NewTicker(resilienceStallCheckPeriod(stallCheckBase))
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// Staged upstream events do not count as downstream writes; start from the
	// request heartbeat when it has already committed.
	lastDownstreamWriteAt := openAIKeepaliveLastWrite(c)
	var keepaliveTimer *time.Timer
	var keepaliveCh <-chan time.Time
	keepalivePending := false
	if keepaliveInterval > 0 {
		keepaliveTimer = time.NewTimer(time.Until(lastDownstreamWriteAt.Add(keepaliveInterval)))
		defer keepaliveTimer.Stop()
		keepaliveCh = keepaliveTimer.C
	}

	var firstOutputTimer *time.Timer
	var firstOutputCh <-chan time.Time
	if firstOutputTimeout > 0 {
		remaining := time.Until(startTime.Add(firstOutputTimeout))
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		firstOutputTimer = time.NewTimer(remaining)
		firstOutputCh = firstOutputTimer.C
		defer firstOutputTimer.Stop()
	}
	stopFirstOutputTimer := func() {
		if firstOutputTimer == nil {
			return
		}
		if !firstOutputTimer.Stop() {
			select {
			case <-firstOutputTimer.C:
			default:
			}
		}
		firstOutputTimer = nil
		firstOutputCh = nil
	}
	// 仅发送一次错误事件，避免多次写入导致协议混乱。
	// 注意：OpenAI `/v1/responses` streaming 事件必须符合 OpenAI Responses schema；
	// 否则下游 SDK（例如 OpenCode）会因为类型校验失败而报错。
	errorEventSent := false
	nextFailureSequence := int64(0)
	clientDisconnected := false // 客户端断开后继续 drain 上游以收集 usage
	sawTerminalEvent := false
	sawFailedEvent := false
	sawBareError := false
	sawResponseFailed := false
	terminalEventType := ""
	capacityFailoverSuppressedLogged := false
	failedMessage := ""
	responsesSemanticOutputSeen := false
	clientOutputStarted := false
	codexFailureTerminal := account != nil && account.IsOpenAIOAuthLike()
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	var streamEarlyErr error
	attemptStagedBlankPlaceholder := false
	eventHasBlankPlaceholder := false
	terminalFailurePending := false
	failureDelivered := false
	suppressCurrentEvent := false
	var bareErrorPayload []byte
	bareErrorWrappedStatus := 0
	bareErrorAccountSideEffectsPending := false
	pendingSSEEventType := ""
	pendingSSEEventLine := ""
	eventInProgress := false
	eventStartsClientOutput := false
	eventStartsTTFTOutput := false
	eventShouldFlush := false
	handlePendingWriteError := func(err error) {
		if firstOutputStage != nil && !firstOutputStage.closed {
			message := "OpenAI first-output staging failed"
			if errors.Is(err, errOpenAIFirstOutputStageLimit) {
				message = "OpenAI first-output staging limit exceeded"
			}
			logger.LegacyPrintf("service.openai_gateway", "%s: account=%d model=%s error=%v", message, account.ID, originalModel, err)
			failoverErr := s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, message)
			failoverErr.SafeToFailoverAfterWrite = true
			streamEarlyErr = failoverErr
			_ = resp.Body.Close()
			return
		}
		clientDisconnected = true
		cancelSchedClient(c)
		logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
	}
	completeGuardedEvent := func(queueDrained bool) {
		completedProgressEvent := eventStartsClientOutput
		completedTTFTEvent := eventStartsTTFTOutput
		shouldFlush := eventShouldFlush || (queueDrained && clientOutputStarted)
		eventInProgress = false
		if !clientDisconnected {
			if completedProgressEvent {
				applyAttemptResponseHeaders()
			}
			if shouldFlush {
				if err := flushBuffered(); err != nil {
					clientDisconnected = true
					cancelSchedClient(c)
					logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
				} else {
					clientOutputStarted = true
					lastDownstreamWriteAt = time.Now()
				}
			}
		}
		if completedProgressEvent && !firstOutputProgressObserved {
			firstOutputScanGuard.Store(false)
			firstOutputProgressObserved = true
			stopFirstOutputTimer()
		}
		if completedTTFTEvent && firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		eventStartsClientOutput = false
		eventStartsTTFTOutput = false
		eventShouldFlush = false
	}
	sendErrorEvent := func(message string) {
		if errorEventSent || clientDisconnected || failureDelivered || sawTerminalEvent {
			return
		}
		// A visible event may still await its closing blank line. Preserve that
		// pending output and let the failure frame close the original boundary.
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			if err := discardPendingWrites(); err != nil {
				handlePendingWriteError(err)
				return
			}
			eventInProgress = false
		}
		if !claimOpenAIResponsesTerminal(c) {
			return
		}
		errorEventSent = true
		applyAttemptResponseHeaders()
		source, _ := json.Marshal(gin.H{
			"sequence_number": nextFailureSequence,
			"error":           gin.H{"type": "server_error", "code": "server_error", "status_code": http.StatusInternalServerError},
			"response":        openAIResponsesFailureMetadata(usage, responseCreatedAt),
		})
		frame := buildOpenAIResponseFailedSSEForClient(ctx, c, responseID, originalModel, source, message)
		if eventInProgress || responsesEOFUnterminated {
			frame = "\n" + frame
		}
		if _, err := writePendingString(frame); err != nil {
			clientDisconnected = true
			cancelSchedClient(c)
		} else if err := flushBuffered(); err != nil {
			clientDisconnected = true
			cancelSchedClient(c)
		} else {
			failureDelivered = true
			clientOutputStarted = true
			lastDownstreamWriteAt = time.Now()
			MarkResponseCommitted(c)
		}
		_ = resp.Body.Close()
	}

	needModelReplace := originalModel != mappedModel
	streamOutputAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamDoneItems := newResponsesStreamOutputItems()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	searchCounter := 0
	// Dedup search tool calls across SSE events (item.done + response.completed
	// both list the same call_id — counting both would ~2× the surcharge).
	streamSearchSeen := make(map[string]struct{})
	cleanEOF := false
	resultWithUsage := func() *openaiStreamingResult {
		return &openaiStreamingResult{
			TerminalSeen:     observedTerminal.Seen,
			TerminalValid:    observedTerminal.Valid,
			usage:            usage,
			firstTokenMs:     relayLive.firstTokenMs(firstTokenMs, startTime, ttftMode),
			responseID:       responseID,
			imageCount:       imageCounter.Count(),
			imageOutputSizes: imageCounter.Sizes(),
			searchCount:      searchCounter,
			clientSawOutput:  delivery.sawOutput,
			upstreamAccepted: !sawFailedEvent && (openAIM3TerminalAccepted(terminalEventType, responsesSemanticOutputSeen) || cleanEOF && responsesSemanticOutputSeen),
		}
	}
	flushPending := func(disconnectMessage string) {
		if clientDisconnected || pendingBytes() == 0 {
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			cancelSchedClient(c)
			logger.LegacyPrintf("service.openai_gateway", "%s", disconnectMessage)
			return
		}
		clientOutputStarted = true
		lastDownstreamWriteAt = time.Now()
	}
	finalizeStream := func() (*openaiStreamingResult, error) {
		responsesObserved = responsesObserved || cleanEOF
		if finishResponsesFrame != nil {
			finishResponsesFrame()
		}
		if streamEarlyErr != nil {
			return resultWithUsage(), streamEarlyErr
		}
		if cleanEOF && tailMetered && !sawFailedEvent {
			sawTerminalEvent = true
			terminalEventType = "usage_eof"
		}
		if stageFirstOutput && eventInProgress {
			// EOF dispatches the final SSE event even without a trailing blank line.
			completeGuardedEvent(true)
		}
		if codexFailureTerminal && sawBareError && !sawResponseFailed && bareErrorAccountSideEffectsPending {
			s.handleOpenAIStreamFailureAfterOutput(ctx, c, account, resp, bareErrorPayload, failedMessage, mappedModel, bareErrorWrappedStatus)
			bareErrorAccountSideEffectsPending = false
		}
		if codexFailureTerminal && sawBareError && !sawResponseFailed && !clientDisconnected && claimOpenAIResponsesTerminal(c) {
			applyAttemptResponseHeaders()
			clientErrorPayload := openAIResponsesFailureSource(bareErrorPayload, usage, responseCreatedAt, nextFailureSequence)
			if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(clientErrorPayload); changed {
				clientErrorPayload = rewritten
			}
			if _, err := writePendingString(buildOpenAIResponseFailedSSEForClient(ctx, c, responseID, originalModel, clientErrorPayload, failedMessage)); err != nil {
				handlePendingWriteError(err)
			} else {
				failureDelivered = true
			}
		}
		if sawTerminalEvent && !sawFailedEvent {
			s.clearOpenAIProxyStreamDisconnect(account)
		}
		if !sawTerminalEvent && !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				false,
				upstreamRequestID,
				nil,
				"OpenAI stream ended before a terminal event",
			)
		}
		flushPending("Client disconnected during final flush, returning collected usage")
		if !sawTerminalEvent {
			if openAIStreamClientOutputStarted(c, clientOutputStarted) && !clientDisconnected {
				s.recordOpenAIProxyStreamDisconnect(account, errors.New("stream ended before terminal event"), upstreamRequestID)
				resilienceStreamBrokenAfterOutput(ctx, s.rateLimitService, account, resp, nil, true)
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		if sawFailedEvent {
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
		}
		logOpenAISuccessMissingUsage(ctx, c, account, resp, usage, terminalEventType, clientDisconnected, usageSource != "missing")
		return resultWithUsage(), nil
	}
	handleScanErr := func(scanErr error) (*openaiStreamingResult, error, bool) {
		if scanErr == nil {
			return nil, nil, false
		}
		if finishResponsesFrame != nil {
			finishResponsesFrame()
		}
		if errors.Is(scanErr, errOpenAIFirstOutputScannerLimit) && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE token exceeded guarded first-output limit: account=%d limit=%d error=%v", account.ID, openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance, scanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) && stageFirstOutput && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long before first output: account=%d max_size=%d error=%v", account.ID, maxLineSize, scanErr)
			failoverErr := s.newOpenAIStreamFailoverError(
				c, account, false, upstreamRequestID, nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			)
			failoverErr.SafeToFailoverAfterWrite = true
			return resultWithUsage(), failoverErr, true
		}
		if sawTerminalEvent {
			if !sawFailedEvent {
				s.clearOpenAIProxyStreamDisconnect(account)
				logger.LegacyPrintf("service.openai_gateway", "Upstream scan ended after terminal event: %v", scanErr)
			}
			result, err := finalizeStream()
			return result, err, true
		}
		schedCapture.noteNetworkError(scanErr)
		// 客户端断开/取消请求时，上游读取往往会返回 context canceled。
		// /v1/responses 的 SSE 事件必须符合 OpenAI 协议；这里不注入自定义 error event，避免下游 SDK 解析失败。
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			if clientDisconnected {
				s.clearOpenAIProxyStreamDisconnect(account)
			}
			if eventShouldFlush {
				flushPending("Client disconnected during canceled stream flush, returning collected usage")
			}
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", scanErr), true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, scanErr)
			sendErrorEvent("Upstream response exceeded the size limit")
			return resultWithUsage(), scanErr, true
		}
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(scanErr.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(), s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, msg), true
		}
		// 客户端已断开时，上游出错仅影响体验，不影响计费；返回已收集 usage
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", scanErr), true
		}
		s.recordOpenAIProxyStreamDisconnect(account, scanErr, upstreamRequestID)
		resilienceStreamBrokenAfterOutput(ctx, s.rateLimitService, account, resp, scanErr, true)
		_, message := classifyOpenAIUpstreamStreamReadError(scanErr)
		sendErrorEvent(message)
		return resultWithUsage(), fmt.Errorf("stream read error: %w", scanErr), true
	}
	processResponseLine := func(line string, queueDrained bool) {
		if streamEarlyErr != nil {
			return
		}
		if eventType, ok := extractOpenAISSEEventLine(line); ok {
			pendingSSEEventType = eventType
			pendingSSEEventLine = line
			eventType = strings.TrimSpace(eventType)
			suppressCurrentEvent = codexFailureTerminal && (eventType == "error" || (sawBareError && !sawResponseFailed && eventType != "response.failed"))
		}
		// Extract data from SSE line (supports both "data: " and "data:" formats)
		if data, ok := extractOpenAISSEDataLine(line); ok {
			restoreFailureEventLine := false
			dataBytes := []byte(data)
			if seq := gjson.GetBytes(dataBytes, "sequence_number"); seq.Exists() && seq.Int() >= nextFailureSequence {
				nextFailureSequence = seq.Int() + 1
			}
			eventType := effectiveOpenAISSEEventType(dataBytes, pendingSSEEventType)
			if currentTerminal.Valid {
				eventType = currentTerminal.EventType
			}
			if sawFailedEvent && !currentTerminal.Failure {
				s.parseSSEUsageBytesWithType(dataBytes, eventType, usage)
				suppressCurrentEvent = true
				return
			}
			if created := gjson.GetBytes(dataBytes, "response.created_at"); created.Exists() {
				responseCreatedAt = created.Int()
			}
			if currentTerminal.Failure && !currentTerminal.Valid {
				s.parseSSEUsageBytesWithType(dataBytes, eventType, usage)
				sawFailedEvent = true
				failedMessage = "Upstream returned an invalid Responses terminal event"
				if currentTerminal.Reason == "malformed_wrapped_error" {
					logOpenAIWrappedErrorMalformed(ctx, account, "native_sse")
					failedMessage = openAIMalformedWrappedErrorMessage
				}
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
					streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, nil, failedMessage, mappedModel, resp.Header)
				} else {
					sendErrorEvent(failedMessage)
					streamEarlyErr = errors.New(failedMessage)
				}
				return
			}

			wrappedStatus, wrappedFrame, malformedWrapped := openAIWrappedUpstreamErrorStatus(pendingSSEEventType, dataBytes)
			if malformedWrapped {
				logOpenAIWrappedErrorMalformed(ctx, account, "native_sse")
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
					// Failed validation is a control-flow boundary: none of the
					// rejected frame's status, code or message is classification input.
					sawFailedEvent = true
					streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, nil, openAIMalformedWrappedErrorMessage, mappedModel, resp.Header)
					return
				}
			}
			if wrappedFrame && eventType == "proxy.upstream_error" && !openAIStreamClientOutputStarted(c, clientOutputStarted) {
				// Only pre-output E1 dispatch uses the raw error event. E2 keeps
				// its authoritative response.failed payload type on both sides.
				eventType = "error"
			}
			if disposition := resilience.ResponsesEventOutputDisposition(eventType, dataBytes); disposition.BlankPlaceholder {
				eventHasBlankPlaceholder = true
			}
			observeOpenAIPreludeCreated(preludeTrace, eventType)

			if codexFailureTerminal && sawBareError && !sawResponseFailed && eventType != "response.failed" {
				suppressCurrentEvent = true
			}
			observeSchedPayload(c, dataBytes, eventType)
			observer.ObserveOpenAI(dataBytes, eventType)
			// 初始上游 data 的 type 只解析一次：原始值保持终止事件的精确匹配，规范化值供后续分支复用。
			if currentTerminal.Seen && currentTerminal.Valid || strings.TrimSpace(data) == "[DONE]" {
				sawTerminalEvent = true
				terminalEventType = eventType
				if strings.TrimSpace(data) == "[DONE]" {
					terminalEventType = "[DONE]"
				}
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			forceFlushFailedEvent := false
			if !capacityFailoverSuppressedLogged && account != nil && account.Platform == PlatformOpenAI &&
				(eventType == "error" || eventType == "response.failed") &&
				openAIStreamClientOutputStarted(c, clientOutputStarted) &&
				isOpenAIUpstreamCapacityShedEvent(dataBytes) {
				logOpenAICapacityFailoverSuppressed(ctx, account, "native_sse", upstreamRequestID, eventType)
				capacityFailoverSuppressedLogged = true
			}
			if resilience.ClassifyStreamEvent("responses", eventType, dataBytes).Kind == resilience.TerminalFailure &&
				!openAIStreamClientOutputStarted(c, clientOutputStarted) {
				if err := discardPendingWrites(); err != nil {
					handlePendingWriteError(err)
					return
				}
				if writer := deliveryWriterFrom(c); writer != nil {
					// A bare error is still a Responses terminal after its prelude is discarded.
					writer.stream.responses.Store(true)
				}
				restoreFailureEventLine = pendingSSEEventLine != ""
				eventInProgress = false
				eventHasBlankPlaceholder = false
			}
			cyberHit := false
			if currentTerminal.Failure {
				if codexFailureTerminal && eventType == "error" {
					sawBareError = true
					bareErrorPayload = append(bareErrorPayload[:0], dataBytes...)
					bareErrorWrappedStatus = wrappedStatus
					suppressCurrentEvent = true
				} else if codexFailureTerminal && eventType == "response.failed" {
					sawResponseFailed = true
					restoreFailureEventLine = restoreFailureEventLine || (suppressCurrentEvent && pendingSSEEventLine != "")
					suppressCurrentEvent = false
				}
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				if failedMessage == "" {
					failedMessage = "Upstream response failed"
				}
				// response.failed 自带上游已消耗的 usage（input token 通常已扣）；必须先解析
				// 再打 cyber 标记，否则 mark 记到的是解析前的 0，导致流式 cyber 按 0 token 计费
				// 而漏记真实用量。对齐 WS V2 / Chat 流式路径（均先解析 usage 再 Mark）。
				s.parseSSEUsageBytesWithType(dataBytes, eventType, usage)
				if hit, code, msg := detectOpenAICyberPolicy(dataBytes); hit {
					cyberHit = true
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				outputStarted := openAIStreamClientOutputStarted(c, clientOutputStarted)
				if !outputStarted && !cyberHit {
					if compactErr := newOpenAICompactFallbackSignal(c, dataBytes, failedMessage, wrappedStatus); compactErr != nil {
						sawFailedEvent = true
						streamEarlyErr = compactErr
						return
					}
					if replayErr := newOpenAIWebSearchReplaySignal(c, account, dataBytes, failedMessage, wrappedStatus); replayErr != nil {
						sawFailedEvent = true
						streamEarlyErr = replayErr
						return
					}
					if repairErr := newOpenAIRequestRepairSignal(ctx, c, account, dataBytes, failedMessage, wrappedStatus); repairErr != nil {
						sawFailedEvent = true
						streamEarlyErr = repairErr
						return
					}
				}
				captureOpenAIStateRejection(c, account, dataBytes, failedMessage, "native_sse", outputStarted, nil, wrappedStatus)
				if a := openAICollaborationAttempt(c); a != nil && a.applied && !outputStarted && resp.StatusCode == http.StatusOK {
					status := openAIStreamFailedEventSemanticStatus(dataBytes, failedMessage, wrappedStatus)
					a.clientRejected = a.clientRejected || (status >= 400 && status < 500 && status != 429 && classifyUpstreamHTTP(status, nil, dataBytes, failedMessage).Category == resilience.CategoryClientError)
				}
				if outputStarted && !cyberHit {
					if codexFailureTerminal && eventType == "error" {
						// OpenAI commonly follows a bare error with response.failed.
						// Defer account health updates so the pair is applied once.
						bareErrorAccountSideEffectsPending = true
					} else {
						s.handleOpenAIStreamFailureAfterOutput(ctx, c, account, resp, dataBytes, failedMessage, mappedModel, wrappedStatus)
						bareErrorAccountSideEffectsPending = false
					}
					if eventType == "response.failed" {
						// Once semantic output is committed, failover replay is unsafe. Keep
						// the terminal event on the existing stream, but retain the upstream
						// request ID and payload for operations diagnostics.
						s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "stream_failed", dataBytes, failedMessage)
					}
				}
				if !outputStarted {
					if failoverErr := s.failoverOpenAIStreamModelAvailability(ctx, c, account, dataBytes, failedMessage, resp.Header, mappedModel, false, wrappedStatus); failoverErr != nil {
						sawFailedEvent = true
						streamEarlyErr = failoverErr
						return
					}
					if wrappedFrame {
						if failoverErr := s.newOpenAIWrappedStreamFailoverError(ctx, c, account, false, upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header, wrappedStatus); failoverErr != nil {
							sawFailedEvent = true
							streamEarlyErr = failoverErr
							return
						}
					}
					shouldFailover := false
					if !cyberHit && !wrappedFrame {
						if eventType == "error" {
							shouldFailover = openAIStreamErrorEventShouldFailover(dataBytes, failedMessage)
						} else {
							shouldFailover = openAIStreamFailedEventShouldFailover(dataBytes, failedMessage)
						}
					}
					if shouldFailover {
						sawFailedEvent = true
						streamEarlyErr = s.newOpenAIStreamFailoverErrorWithModel(c, account, false, upstreamRequestID, dataBytes, failedMessage, mappedModel, resp.Header)
						return
					}
					if !cyberHit && !sawBareError {
						if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, account.Platform, dataBytes, failedMessage); matched {
							sawFailedEvent = true
							// 命中透传规则也要记录 ops 上游错误事件（对齐 CC/Messages 与
							// antigravity 先例），否则透传命中的 failed 在监控中不可见。
							s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "http_error", dataBytes, failedMessage)
							MarkResponseCommitted(c)
							// xy2api resilience (PM instruction 5, as DELTA-P1-2): after
							// heartbeats committed 200 the rule's error ends the stream
							// as the Responses terminal event instead of a bare JSON body.
							writeOpenAIStreamRuleError(ctx, c, status, errType, errMsg)
							streamEarlyErr = fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
							return
						}
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
				terminalFailurePending = !codexFailureTerminal || eventType == "response.failed"
			}
			if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
			}
			imageCounter.AddSSEData(dataBytes)
			if !imageGenerationSeen && bytes.Contains(dataBytes, []byte("image_generation_call")) {
				imageGenerationSeen = true
			}
			searchCounter += countGrokNativeSearchCallsInSSEDataDedup(dataBytes, streamSearchSeen)

			// Correct Codex tool calls if needed (apply_patch -> edit, etc.)
			if correctedData, corrected := s.toolCorrector.CorrectToolCallsInSSEBytes(dataBytes); corrected {
				dataBytes = correctedData
				data = string(correctedData)
				line = "data: " + data
				eventType = effectiveOpenAISSEEventType(dataBytes, eventType)
			}
			if imageOutput, ok := extractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			streamDoneItems.Observe(dataBytes)
			if responsesStreamEventMayContributeToOutput(eventType) {
				var streamEvent apicompat.ResponsesStreamEvent
				if err := json.Unmarshal(dataBytes, &streamEvent); err == nil {
					streamOutputAccumulator.ProcessEvent(&streamEvent)
				}
			}
			if normalizedData, normalized := normalizeResponsesStreamingTerminalOutput(dataBytes, streamOutputAccumulator, streamDoneItems, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = effectiveOpenAISSEEventType(dataBytes, eventType)
			}
			restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
				return
			}
			restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
			if restoreErr != nil {
				streamEarlyErr = fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
				return
			}
			restoredData = restoreCodexToolNamesFromSSEContext(c, restoredData, eventType)
			restoredData, restoreErr = restoreOpenAICollaboration(c, eventType, restoredData, true)
			if restoreErr != nil {
				writeOpenAIStreamRuleError(ctx, c, http.StatusBadGateway, "upstream_error", openAICollabRestoreMessage)
				streamEarlyErr = restoreErr
				return
			}
			if !bytes.Equal(restoredData, dataBytes) {
				dataBytes = restoredData
				data = string(restoredData)
				line = "data: " + data
				eventType = effectiveOpenAISSEEventType(dataBytes, eventType)
			}
			if sanitizedData, sanitized := sanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				openAIStreamClientOutputStarted(c, clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				data = string(sanitizedData)
				line = "data: " + data
			}
			if currentTerminal.Failure && currentTerminal.EventType == "error" && !codexFailureTerminal && strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String()) == "" {
				frame := buildOpenAIResponseFailedSSEForClient(ctx, c, responseID, originalModel, openAIResponsesFailureSource(dataBytes, usage, responseCreatedAt, nextFailureSequence), failedMessage)
				line = strings.TrimSuffix(frame, "\n\n")
			}
			// Replace model in response if needed.
			if needModelReplace {
				line = s.replaceModelInSSELine(line, mappedModel, originalModel)
			}
			if strings.TrimSpace(data) != "[DONE]" && !openAIStreamEventTypeIsTerminal(eventType) {
				responsesSemanticOutputSeen = responsesSemanticOutputSeen || resilience.ResponsesEventOutputDisposition(eventType, dataBytes).SemanticOutput
			}
			// Preserve the silent-refusal guard (#5009/#7774); whitespace does not
			// turn an otherwise empty, usage-free completed event into success.
			if account != nil && (account.Platform == PlatformOpenAI || account.Platform == PlatformGrok) &&
				(eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !responsesSemanticOutputSeen && !clientOutputStarted &&
				openAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				sawTerminalEvent = true
				streamEarlyErr = newOpenAIResponsesEmptyCompletedFailoverError(c, account, upstreamRequestID)
				return
			}
			startsClientOutput := forceFlushFailedEvent || openAIStreamDataStartsClientOutput(data, eventType)
			if startsClientOutput && !clientOutputStarted {
				releaseOpenAIPrelude(preludeTrace, eventType, dataBytes)
			}
			startsVisibleOutput := openAIStreamDataStartsVisibleOutput(data, eventType)
			startsTTFTOutput := openAIStreamDataStartsTTFT(data, eventType, forceFlushFailedEvent, ttftMode)
			if stageFirstOutput {
				eventStartsClientOutput = eventStartsClientOutput || startsClientOutput
				eventStartsTTFTOutput = eventStartsTTFTOutput || startsTTFTOutput
				if startsClientOutput {
					firstOutputScanGuard.Store(false)
				}
			}
			// 写入客户端（客户端断开后继续 drain 上游）
			if !clientDisconnected && !failureDelivered && !suppressCurrentEvent {
				if currentTerminal.Seen && currentTerminal.Valid && !claimOpenAIResponsesTerminal(c) {
					suppressCurrentEvent = true
					return
				}
				// The pre-output reset also removed this failure's event line.
				// Restore only the current failure header, never the old prelude.
				if restoreFailureEventLine {
					if _, err := writePendingString(pendingSSEEventLine + "\n"); err != nil {
						handlePendingWriteError(err)
						return
					}
				}
				if delivery.semanticEnd == 0 && startsClientOutput && !openAIStreamEventTypeIsTerminal(eventType) {
					delivery.semanticStart = queuedBytes
					delivery.semanticEnd = queuedBytes + int64(len(line))
				}
				shouldFlush := queueDrained && (clientOutputStarted || startsClientOutput)
				if firstTokenMs == nil && startsVisibleOutput {
					// 保证首个 token 事件尽快出站，避免影响 TTFT。
					shouldFlush = true
				}
				eventShouldFlush = eventShouldFlush || shouldFlush
				if _, err := writePendingString(line); err != nil {
					handlePendingWriteError(err)
				} else if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				} else {
					if firstOutputStage != nil && !firstOutputStage.closed && resilience.ResponsesEventOutputDisposition(eventType, dataBytes).Staged {
						resilience.StateFrom(ctx).NotePreludeStaged(startTime)
					}
					if eventHasBlankPlaceholder && !attemptStagedBlankPlaceholder && firstOutputStage != nil && !firstOutputStage.closed {
						markOpenAIBlankPlaceholderStaged(c, account.ID)
						attemptStagedBlankPlaceholder = true
					}
					eventInProgress = true
				}
			}

			// Record first token time
			if !guardFirstOutput && firstTokenMs == nil && startsTTFTOutput {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
				stopFirstOutputTimer()
			}
			s.parseSSEUsageBytesWithType(dataBytes, eventType, usage)
			return
		}

		// A blank line dispatches a guarded event from the attempt-local stage.
		if stageFirstOutput && line == "" {
			pendingSSEEventType = ""
			pendingSSEEventLine = ""
			eventHasBlankPlaceholder = false
			if suppressCurrentEvent {
				suppressCurrentEvent = false
				terminalFailurePending = false
				eventInProgress = false
				eventStartsClientOutput = false
				eventStartsTTFTOutput = false
				eventShouldFlush = false
				return
			}
			if failureDelivered {
				terminalFailurePending = false
				eventInProgress = false
				eventStartsClientOutput = false
				eventStartsTTFTOutput = false
				eventShouldFlush = false
				return
			}
			if !clientDisconnected && !responsesEOF {
				if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				}
			}
			if streamEarlyErr == nil {
				completeGuardedEvent(queueDrained)
			}
			if terminalFailurePending && streamEarlyErr == nil {
				terminalFailurePending = false
				failureDelivered = true
			}
			return
		}
		// Non-guarded streams retain upstream's event-boundary flushing: a keepalive
		// or queue-drain flush must never split an open SSE event.
		shouldFlush := false
		if line == "" {
			pendingSSEEventType = ""
			pendingSSEEventLine = ""
			if suppressCurrentEvent {
				suppressCurrentEvent = false
				terminalFailurePending = false
				eventInProgress = false
				eventShouldFlush = false
				return
			}
			shouldFlush = eventShouldFlush || (queueDrained && clientOutputStarted)
			eventShouldFlush = false
			if failureDelivered {
				terminalFailurePending = false
			}
		}
		if !clientDisconnected && !failureDelivered && !suppressCurrentEvent {
			suffix := "\n"
			if responsesEOF && line == "" {
				suffix = ""
			}
			if _, err := writePendingString(line); err != nil {
				handlePendingWriteError(err)
			} else if _, err := writePendingString(suffix); err != nil {
				handlePendingWriteError(err)
			} else {
				eventInProgress = line != ""
				if shouldFlush {
					if err := flushBuffered(); err != nil {
						clientDisconnected = true
						cancelSchedClient(c)
						logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
					} else {
						clientOutputStarted = true
						lastDownstreamWriteAt = time.Now()
					}
				}
			}
			if line == "" && terminalFailurePending && streamEarlyErr == nil {
				terminalFailurePending = false
				failureDelivered = true
			}
		}
	}

	var responsesParser openAICompatSSEFrameParser
	var frameExtras []string
	var frameDataLine, frameEventLine string
	frameDataCount := 0
	processSingleFrame := func(frame openAICompatSSEFrame, queueDrained bool) {
		defer func() {
			frameDataLine, frameEventLine, frameDataCount = "", "", 0
			frameExtras = nil
			responsesFrameBytes = 0
		}()
		currentTerminal = ClassifyOpenAIResponsesTerminal(frame.EventType, []byte(frame.Data))
		if currentTerminal.Failure {
			if !observedTerminal.Failure {
				typedBareErrorRecoverable = currentTerminal.Valid && openAIResponsesTypedBareError([]byte(frame.Data))
			} else if !currentTerminal.Valid || !openAIResponsesTypedBareError([]byte(frame.Data)) {
				typedBareErrorRecoverable = false
			}
		}
		if codexFailureTerminal && typedBareErrorRecoverable && sawBareError && !sawResponseFailed && currentTerminal.Success {
			// Codex may report a provisional typed error before recovering.
			sawBareError, sawFailedEvent, terminalFailurePending, suppressCurrentEvent = false, false, false, false
			bareErrorPayload, bareErrorWrappedStatus, bareErrorAccountSideEffectsPending = nil, 0, false
			failedMessage = ""
			observedTerminal = currentTerminal
			typedBareErrorRecoverable = false
			resilience.StateFrom(ctx).SetUpstreamRecoveryHint(nil)
		}
		if !currentTerminal.Failure && openAIStreamDataStartsClientOutput(frame.Data, effectiveOpenAISSEEventType([]byte(frame.Data), frame.EventType)) {
			firstOutputScanGuard.Store(false)
			if releaseFrameScan != nil {
				releaseFrameScan()
			}
		}
		if currentTerminal.Failure && currentTerminal.Valid && !observedTerminal.Failure {
			noteOpenAIResponsesRecoveryHint(c, account, resp.Header, []byte(frame.Data), time.Now())
		}
		if source := s.parseSSEUsageBytesWithType([]byte(frame.Data), currentTerminal.EventType, usage); source != "" {
			usageSource = source
		}
		tailMetered = openAIResponsesTailUsageCandidate(tailMetered, frame.EventType, []byte(frame.Data), currentTerminal)
		if !observedTerminal.Failure && (currentTerminal.Seen || currentTerminal.Failure) {
			observedTerminal = currentTerminal
		}
		if currentTerminal.Valid && currentTerminal.Failure {
			if rejection := s.handleHTTPEncryptedContentFrame(ctx, c, account, []byte(frame.Data), currentTerminal.WrappedStatus, openAIStreamClientOutputStarted(c, clientOutputStarted), flushBuffered, upstreamRequestID); rejection != nil {
				sawFailedEvent, streamEarlyErr = true, rejection
				return
			}
		}
		if currentTerminal.Failure && !currentTerminal.Valid {
			pendingSSEEventType, pendingSSEEventLine = frame.EventType, ""
			processResponseLine("data: "+frame.Data, queueDrained)
			return
		}
		if frame.EventType != "" {
			processResponseLine(frameEventLine, queueDrained)
		}
		for _, extra := range frameExtras {
			processResponseLine(extra, queueDrained)
		}
		wireData := frameDataLine
		if frameDataCount != 1 {
			wireData = "data: " + compactOpenAIResponsesFrameData(frame.Data)
		}
		processResponseLine(wireData, queueDrained)
		processResponseLine("", queueDrained)
	}
	processFrame := func(frame openAICompatSSEFrame, queueDrained bool) {
		frames := splitOpenAIResponsesFrame(frame)
		if len(frames) == 1 {
			processSingleFrame(frame, queueDrained)
			return
		}
		eventLine, eof := frameEventLine, responsesEOF
		for i, event := range frames {
			frameEventLine, frameDataLine, frameDataCount = eventLine, "data: "+event.Data, 1
			responsesEOF = eof && i == len(frames)-1
			processSingleFrame(event, queueDrained && i == len(frames)-1)
			if streamEarlyErr != nil {
				break
			}
		}
		responsesEOF = eof
	}
	processSSELine := func(line string, queueDrained bool) {
		responsesObserved = true
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "data:") {
			frameDataCount++
			frameDataLine = line
		}
		if strings.HasPrefix(line, "event:") {
			frameEventLine = line
		}
		responsesFrameBytes += len(line) + 1
		if stageFirstOutput && !clientOutputStarted && responsesFrameBytes > openAIFirstOutputStageMaxBytes {
			failure := s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, "OpenAI first-output staging limit exceeded")
			failure.SafeToFailoverAfterWrite = true
			streamEarlyErr = failure
			return
		}
		if strings.HasPrefix(line, ":") && len(responsesParser.dataLines) == 0 && responsesParser.eventType == "" {
			processResponseLine(line, queueDrained)
			return
		}
		if frame, ok := responsesParser.AddLine(line); ok {
			processFrame(frame, queueDrained)
			return
		}
		if line == "" {
			for _, extra := range frameExtras {
				processResponseLine(extra, queueDrained)
			}
			frameExtras = nil
			responsesFrameBytes = 0
			processResponseLine("", queueDrained)
		} else if !strings.HasPrefix(line, "data:") && !strings.HasPrefix(line, "event:") {
			frameExtras = append(frameExtras, line)
		}
	}
	finishResponsesFrame = func() {
		if frame, ok := responsesParser.Finish(); ok {
			responsesEOF = true
			processFrame(frame, true)
			responsesEOFUnterminated = clientOutputStarted && !failureDelivered && !sawTerminalEvent
			responsesEOF = false
		}
	}

	// 无超时/无 keepalive 的常见路径走同步扫描，减少 goroutine 与 channel 开销。
	if streamInterval <= 0 && keepaliveInterval <= 0 && firstOutputTimeout <= 0 {
		defer putSSEScannerBuf64K(scanBuf)
		for documentScanner.Scan() {
			processSSELine(documentScanner.Text(), true)
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}
			// Terminal 事件完整写出后直接结束，不等上游 EOF（见下方异步循环同款说明）。
			// Codex bare error 仅在尚未收全权威 failed 或 completed 时继续等待。
			if sawTerminalEvent && !eventInProgress && (!codexFailureTerminal || !sawBareError || sawResponseFailed) {
				return finalizeStream()
			}
		}
		if result, err, done := handleScanErr(documentScanner.Err()); done {
			return result, err
		}
		cleanEOF = true
		return finalizeStream()
	}

	type scanEvent struct {
		line      string
		err       error
		processed chan struct{}
	}
	// 独立 goroutine 读取上游，避免读取阻塞影响 keepalive/超时处理
	// Guard mode permits one queued token plus the token being processed. With
	// the guarded scanner cap this bounds scanner/channel retention near 16 MiB;
	// the timeout-disabled path preserves the legacy depth of 16.
	events := make(chan scanEvent, openAIFirstOutputEventQueueSize(guardFirstOutput))
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		if firstOutputScanGuard.Load() {
			ev.processed = make(chan struct{})
		}
		select {
		case events <- ev:
		case <-done:
			return false
		}
		if ev.processed == nil {
			return true
		}
		select {
		case <-ev.processed:
			return true
		case <-done:
			return false
		}
	}
	markEventProcessed := func(ev scanEvent) {
		if ev.processed != nil {
			close(ev.processed)
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for documentScanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: documentScanner.Text()}) {
				return
			}
		}
		if err := documentScanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				cleanEOF = true
				if stageFirstOutput && eventInProgress {
					// EOF dispatches the final SSE event even without a trailing blank
					// line. Do not synthesize extra bytes on the downstream wire.
					completeGuardedEvent(true)
				}
				return finalizeStream()
			}
			if result, err, done := handleScanErr(ev.err); done {
				markEventProcessed(ev)
				return result, err
			}
			releaseFrameScan = func() { markEventProcessed(ev); ev.processed = nil }
			processSSELine(ev.line, len(events) == 0)
			releaseFrameScan = nil
			markEventProcessed(ev)
			if keepalivePending && !eventInProgress {
				keepalivePending = false
				remaining := time.Until(lastDownstreamWriteAt.Add(keepaliveInterval))
				if remaining < 0 {
					remaining = 0
				}
				keepaliveTimer.Reset(remaining)
			}
			if streamEarlyErr != nil {
				return resultWithUsage(), streamEarlyErr
			}
			// Terminal 事件（response.completed 等）已完整写出后不再等上游 EOF：
			// 上游在 keep-alive/HTTP2 复用连接上可能拖延关闭连接，空等期间只能
			// 靠 keepalive 维持，白白拉长尾延迟。usage 已在 terminal 事件中解析。
			// Codex bare error 仅在尚未收全权威 failed 或 completed 时继续等待。
			if sawTerminalEvent && !eventInProgress && (!codexFailureTerminal || !sawBareError || sawResponseFailed) {
				_ = resp.Body.Close()
				return finalizeStream()
			}

		case <-intervalCh:
			if failureDelivered {
				return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
			}
			lastRead := relayLive.lastActivity(time.Unix(0, atomic.LoadInt64(&lastReadAt)))
			stallThreshold := resilienceStallThreshold(streamInterval, openAIStreamClientOutputStarted(c, clientOutputStarted) && !imageGenerationSeen)
			if stallThreshold <= 0 || time.Since(lastRead) < stallThreshold {
				continue
			}
			// xy2api resilience (review P1-C): before output an armed
			// first-output guard owns the attempt, so a silent upstream is
			// switched at its tier instead of ending the request here.
			if resilienceFirstOutputGuardOwnsStall(firstOutputTimer != nil, openAIStreamClientOutputStarted(c, clientOutputStarted)) {
				continue
			}
			if codexFailureTerminal && sawBareError && !sawResponseFailed {
				_ = resp.Body.Close()
				return finalizeStream()
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.LegacyPrintf("service.openai_gateway", "Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, stallThreshold)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			noteSchedStall(c)
			noteResilienceStall(ctx, s.rateLimitService, account)
			// Grok: short cool + account failover when no client-visible bytes
			// were committed yet (pre-commit). After output started we keep the
			// legacy stream_timeout path so partial SSE is not dual-written.
			if account != nil && account.Platform == PlatformGrok {
				s.tempUnscheduleGrok(ctx, account, grokStreamIdleCooldown, "grok stream idle timeout")
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
					_ = resp.Body.Close()
					return resultWithUsage(), grokStreamIdleFailoverError(account, streamInterval)
				}
			}
			sendErrorEvent("Upstream response stream timed out")
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-firstOutputCh:
			if firstOutputProgressObserved {
				stopFirstOutputTimer()
				continue
			}
			if codexFailureTerminal && sawBareError && !sawResponseFailed && len(events) == 0 {
				_ = resp.Body.Close()
				return finalizeStream()
			}
			if resilience.BudgetTraceEnabledForTest {
				firstOutputPlan.ObserveFiredForTest("openai_semantic_output")
			}
			_ = resp.Body.Close()
			for ev := range events {
				markEventProcessed(ev)
			}
			markOpenAIBlankPlaceholderTimeout(c, attemptStagedBlankPlaceholder)
			return resultWithUsage(), s.newOpenAIFirstOutputTimeoutError(
				ctx, c, account, opsUpstreamProxyID(account), opsUpstreamProxyName(account),
				startTime, originalModel, reasoningEffort,
				firstOutputTimeout, "semantic_output", resp.Header, firstOutputPlan,
			)

		case <-keepaliveCh:
			if clientDisconnected || failureDelivered {
				keepaliveCh = nil
				continue
			}
			if eventInProgress || responsesFrameBytes > 0 {
				keepalivePending = true
				continue
			}
			if remaining := time.Until(lastDownstreamWriteAt.Add(keepaliveInterval)); remaining > 0 {
				keepaliveTimer.Reset(remaining)
				continue
			}
			frame := preOutputCommentFrame
			if !clientOutputStarted {
				frame = responsesPreOutputHeartbeatFrame(c)
			}
			if stageFirstOutput {
				// Bypass attempt-local buffered frames. Only turn-state may accompany
				// the stable SSE headers; record its origin before the heartbeat commits.
				// Later attempts cannot replace either the committed header or its origin.
				if !c.Writer.Written() {
					if turnState := extractOpenAICodexTurnState(attemptResponseHeaders); turnState != "" {
						c.Writer.Header().Set(openAICodexTurnStateHeader, turnState)
						s.noteOpenAICodexTurnStateProvenance(c, account)
					}
				}
				_, err := writeOpenAIStreamKeepalive(c, w, frame, true)
				if err != nil {
					clientDisconnected = true
					cancelSchedClient(c)
					logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
					continue
				}
				flusher.Flush()
				lastDownstreamWriteAt = time.Now()
				keepaliveTimer.Reset(keepaliveInterval)
				continue
			}
			if _, err := writePendingString(string(frame)); err != nil {
				clientDisconnected = true
				cancelSchedClient(c)
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
				continue
			}
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				cancelSchedClient(c)
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during keepalive flush, continuing to drain upstream for billing")
			} else {
				lastDownstreamWriteAt = time.Now()
				keepaliveTimer.Reset(keepaliveInterval)
			}
		}
	}

}

// extractOpenAISSEDataLine 低开销提取 SSE `data:` 行内容。
// 兼容 `data: xxx` 与 `data:xxx` 两种格式。
func extractOpenAISSEDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	start := len("data:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return line[start:], true
}

func extractOpenAISSEEventLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "event:") {
		return "", false
	}
	start := len("event:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return strings.TrimSpace(line[start:]), true
}

type openAICompatSSEFrame struct {
	EventType string
	Data      string
}

type openAICompatSSEFrameParser struct {
	eventType string
	dataLines []string
}

func (p *openAICompatSSEFrameParser) AddLine(line string) (openAICompatSSEFrame, bool) {
	if line == "" {
		return p.dispatch()
	}
	if strings.HasPrefix(line, ":") {
		return openAICompatSSEFrame{}, false
	}
	if eventType, ok := extractOpenAISSEEventLine(line); ok {
		p.eventType = eventType
		return openAICompatSSEFrame{}, false
	}
	if data, ok := extractOpenAISSEDataLine(line); ok {
		p.dataLines = append(p.dataLines, data)
	}
	return openAICompatSSEFrame{}, false
}

func (p *openAICompatSSEFrameParser) Finish() (openAICompatSSEFrame, bool) {
	return p.dispatch()
}

func (p *openAICompatSSEFrameParser) dispatch() (openAICompatSSEFrame, bool) {
	frame := openAICompatSSEFrame{
		EventType: p.eventType,
		Data:      strings.Join(p.dataLines, "\n"),
	}
	p.eventType = ""
	p.dataLines = nil
	return frame, frame.Data != ""
}

func openAICompatPayloadWithEventType(payload, eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || strings.TrimSpace(payload) == "" || strings.TrimSpace(payload) == "[DONE]" {
		return payload
	}
	if strings.TrimSpace(gjson.Get(payload, "type").String()) != "" {
		return payload
	}
	patched, err := sjson.Set(payload, "type", eventType)
	if err != nil {
		return payload
	}
	return patched
}

func effectiveOpenAISSEEventType(payload []byte, eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if payloadType := strings.TrimSpace(gjson.GetBytes(payload, "type").String()); payloadType != "" {
		return payloadType
	}
	return eventType
}

func (s *OpenAIGatewayService) replaceModelInSSELine(line, fromModel, toModel string) string {
	if fromModel == "" || toModel == "" || fromModel == toModel {
		return line
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok || !gjson.Valid(data) {
		return line
	}
	updated := data
	// Only protocol model fields are rewritten; text and tool payloads are untouched.
	for _, path := range []string{"model", "response.model"} {
		if m := gjson.Get(updated, path); m.Type == gjson.String {
			var err error
			updated, err = sjson.Set(updated, path, toModel)
			if err != nil {
				return line
			}
		}
	}
	if updated == data {
		return line
	}
	return "data: " + updated
}

// correctToolCallsInResponseBody 修正响应体中的工具调用
func (s *OpenAIGatewayService) correctToolCallsInResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	updated := body
	if s != nil && s.toolCorrector != nil {
		if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(updated); changed {
			updated = corrected
		}
	}
	if normalized, changed := normalizeOpenAIResponsesFunctionCallArguments(updated); changed {
		updated = normalized
	}
	return updated
}

func normalizeOpenAIResponsesFunctionCallArguments(data []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(data)) == 0 || !bytes.Contains(data, []byte(`"arguments"`)) {
		return data, false
	}
	if !gjson.ValidBytes(data) {
		return data, false
	}

	updated := data
	changed := false
	setDedupedArgument := func(path string) {
		arg := gjson.GetBytes(updated, path)
		if !arg.Exists() || arg.Type != gjson.String {
			return
		}
		deduped, ok := dedupeRepeatedJSONArgumentString(arg.Str)
		if !ok {
			return
		}
		next, err := sjson.SetBytes(updated, path, deduped)
		if err != nil {
			return
		}
		updated = next
		changed = true
	}

	eventType := strings.TrimSpace(gjson.GetBytes(updated, "type").String())
	if eventType == "response.function_call_arguments.done" {
		setDedupedArgument("arguments")
	}
	if itemType := strings.TrimSpace(gjson.GetBytes(updated, "item.type").String()); isResponsesFunctionCallItemType(itemType) {
		setDedupedArgument("item.arguments")
	}
	dedupeResponsesFunctionCallOutputArguments(updated, "response.output", setDedupedArgument)
	dedupeResponsesFunctionCallOutputArguments(updated, "output", setDedupedArgument)

	return updated, changed
}

func dedupeResponsesFunctionCallOutputArguments(data []byte, outputPath string, setDedupedArgument func(string)) {
	output := gjson.GetBytes(data, outputPath)
	if !output.Exists() || !output.IsArray() {
		return
	}
	for i, item := range output.Array() {
		if !isResponsesFunctionCallItemType(strings.TrimSpace(item.Get("type").String())) {
			continue
		}
		setDedupedArgument(outputPath + "." + strconv.Itoa(i) + ".arguments")
	}
}

func isResponsesFunctionCallItemType(itemType string) bool {
	return itemType == "function_call" || itemType == "custom_tool_call"
}

func dedupeRepeatedJSONArgumentString(arguments string) (string, bool) {
	if len(arguments) == 0 || len(arguments)%2 != 0 {
		return "", false
	}
	halfLen := len(arguments) / 2
	first := arguments[:halfLen]
	if first != arguments[halfLen:] {
		return "", false
	}
	trimmed := strings.TrimSpace(first)
	if trimmed == "" || (!strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[")) {
		return "", false
	}
	if !json.Valid([]byte(first)) {
		return "", false
	}
	return first, true
}

func (s *OpenAIGatewayService) parseSSEUsage(data string, usage *OpenAIUsage) {
	s.parseSSEUsageBytes([]byte(data), usage)
}

func (s *OpenAIGatewayService) parseSSEUsageBytes(data []byte, usage *OpenAIUsage) {
	if usage == nil {
		return
	}
	// Legacy consumers do not authenticate count presence through this adapter.
	input, output, read, write := usage.InputTokensPresent, usage.OutputTokensPresent, usage.CacheReadInputTokensPresent, usage.CacheCreationInputTokensPresent
	s.parseSSEUsageBytesWithType(data, "", usage)
	usage.InputTokensPresent, usage.OutputTokensPresent, usage.CacheReadInputTokensPresent, usage.CacheCreationInputTokensPresent = input, output, read, write
}

func (s *OpenAIGatewayService) parseSSEUsageBytesWithType(data []byte, eventType string, usage *OpenAIUsage) string {
	if usage == nil || len(data) == 0 || !bytes.Contains(data, []byte(`"usage"`)) {
		return ""
	}
	source := "upstream_sse_progressive"
	if openAIStreamEventTypeIsTerminal(effectiveOpenAISSEEventType(data, eventType)) {
		source = "upstream_sse_terminal"
	}
	parsedUsage, present := openAIResponsesUsageEvidence(data, source)
	if !present {
		return ""
	}
	if source == "upstream_sse_terminal" {
		if !openAIUsageHasTokens(&parsedUsage) && openAIUsageHasTokens(usage) {
			return ""
		}
		*usage = parsedUsage
		return source
	}
	retained := openAIUsageHasTokens(&parsedUsage) || !openAIUsageHasTokens(usage)
	mergeOpenAIUsageNonZero(usage, parsedUsage)
	usage.InputTokensPresent = usage.InputTokensPresent || parsedUsage.InputTokensPresent
	usage.OutputTokensPresent = usage.OutputTokensPresent || parsedUsage.OutputTokensPresent
	usage.CacheReadInputTokensPresent = usage.CacheReadInputTokensPresent || parsedUsage.CacheReadInputTokensPresent
	usage.CacheCreationInputTokensPresent = usage.CacheCreationInputTokensPresent || parsedUsage.CacheCreationInputTokensPresent
	if retained {
		return source
	}
	return ""
}

// Parse only the first authenticated failure, before any client/ops rewriting.
func noteOpenAIResponsesRecoveryHint(c *gin.Context, account *Account, headers http.Header, payload []byte, observedAt time.Time) {
	if c == nil || c.Request == nil {
		return
	}
	state := resilience.StateFrom(c.Request.Context())
	if hint, ok := resilience.ParseUpstreamRecoveryHint(headers, payload, observedAt); ok {
		state.SetUpstreamRecoveryHint(&hint)
		if account != nil {
			state.NoteAccountRetryNotBefore(account.ID, hint)
		}
	}
}

func noteOpenAIResponsesFacts(c *gin.Context, account *Account, source string, terminal OpenAIResponsesTerminal, kind string) {
	if c == nil || c.Request == nil || account == nil {
		return
	}
	resilience.StateFrom(c.Request.Context()).SetGatewayResponseFacts(account.ID, true, source != "missing", source, terminal.Seen, terminal.Valid, kind)
}

func (s *OpenAIGatewayService) openAIResponsesUsageFromBody(body string) (*OpenAIUsage, string) {
	usage, source := &OpenAIUsage{}, "missing"
	forEachOpenAIResponsesFrame(body, func(event string, data []byte) {
		terminal := ClassifyOpenAIResponsesTerminal(event, data)
		if observed := s.parseSSEUsageBytesWithType(data, terminal.EventType, usage); observed != "" {
			source = observed
		}
	})
	return usage, source
}

// Compatible Responses upstreams may report usage before the terminal event.
// Retain those non-zero fields as a fallback. A terminal usage with any billed
// tokens is authoritative as a whole; an all-zero terminal does not erase a
// non-zero progressive observation from the same turn.
func mergeOpenAIUsageNonZero(dst *OpenAIUsage, src OpenAIUsage) {
	if dst == nil {
		return
	}
	if src.InputTokens > 0 {
		dst.InputTokens = src.InputTokens
	}
	if src.ImageInputTokens > 0 {
		dst.ImageInputTokens = src.ImageInputTokens
	}
	if src.ImageCacheReadTokens > 0 {
		dst.ImageCacheReadTokens = src.ImageCacheReadTokens
	}
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
	if src.CacheCreationInputTokens > 0 || (dst.CacheCreationInputTokens == 0 && src.CacheWriteSource != "") {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		dst.CacheWriteSource = src.CacheWriteSource
	}
	if src.CacheReadInputTokens > 0 || (dst.CacheReadInputTokens == 0 && src.CacheReadSource != "") {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
		dst.CacheReadSource = src.CacheReadSource
	}
	if src.ImageOutputTokens > 0 {
		dst.ImageOutputTokens = src.ImageOutputTokens
	}
}

func openAIUsageHasTokens(usage *OpenAIUsage) bool {
	return usage != nil && (usage.InputTokens > 0 || usage.ImageInputTokens > 0 ||
		usage.OutputTokens > 0 || usage.CacheCreationInputTokens > 0 ||
		usage.CacheReadInputTokens > 0 || usage.ImageOutputTokens > 0)
}

const openAIMissingUsageLogInterval = time.Minute

type openAIMissingUsageLogSampler struct {
	total      atomic.Uint64
	suppressed atomic.Uint64
	lastLog    atomic.Int64
}

var openAIMissingUsageSampler openAIMissingUsageLogSampler

func (s *openAIMissingUsageLogSampler) sample(now time.Time) (logNow bool, total uint64, suppressed uint64) {
	total = s.total.Add(1)
	nowNanos := now.UnixNano()
	for {
		last := s.lastLog.Load()
		if last != 0 && nowNanos-last < int64(openAIMissingUsageLogInterval) {
			s.suppressed.Add(1)
			return false, total, 0
		}
		if s.lastLog.CompareAndSwap(last, nowNanos) {
			return true, total, s.suppressed.Swap(0)
		}
	}
}

func logOpenAISuccessMissingUsage(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, usage *OpenAIUsage, terminalEvent string, clientDisconnected bool, presence ...bool) {
	usagePresent := openAIUsageHasTokens(usage)
	if len(presence) > 0 {
		usagePresent = presence[0]
	}
	if resp == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || usagePresent {
		return
	}
	terminalEvent = strings.TrimSpace(terminalEvent)
	if terminalEvent != "response.completed" && terminalEvent != "response.incomplete" && terminalEvent != "response.done" && terminalEvent != "[DONE]" && terminalEvent != "json" {
		return
	}
	logNow, total, suppressed := openAIMissingUsageSampler.sample(time.Now())
	if !logNow {
		return
	}
	accountID := int64(0)
	accountType := ""
	if account != nil {
		accountID = account.ID
		accountType = string(account.Type)
	}
	inboundEndpoint := ""
	if c != nil && c.Request != nil && c.Request.URL != nil {
		inboundEndpoint = c.Request.URL.Path
	}
	logger.FromContext(ctx).With(
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.String("account_type", accountType),
		zap.String("inbound_endpoint", inboundEndpoint),
		zap.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
		zap.Int("upstream_status_code", resp.StatusCode),
		zap.String("terminal_event", terminalEvent),
		zap.Bool("client_disconnected", clientDisconnected),
		zap.Uint64("missing_usage_total", total),
		zap.Uint64("suppressed_since_last", suppressed),
	).Warn("openai_usage.success_missing_usage")
}

func extractOpenAIUsageFromJSONBytes(body []byte) (OpenAIUsage, bool) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return OpenAIUsage{}, false
	}
	// 部分 OpenAI 兼容上游（例如 Cline API）会将标准响应包在 data 字段中：
	// {"data":{"choices": [...], "usage": {...}}, "success":true}。
	// 按优先级先保留原有路径，再尝试兼容层 data 包装，
	// 避免同步请求能正常返回但用量被静默记录为 0。
	candidates := []struct {
		usagePath      string
		imageUsagePath string
	}{
		{usagePath: "usage", imageUsagePath: "tool_usage.image_gen"},
		{usagePath: "response.usage", imageUsagePath: "response.tool_usage.image_gen"},
		{usagePath: "data.usage", imageUsagePath: "data.tool_usage.image_gen"},
		{usagePath: "data.response.usage", imageUsagePath: "data.response.tool_usage.image_gen"},
	}
	for _, candidate := range candidates {
		if usage, ok := openAIUsageFromGJSON(gjson.GetBytes(body, candidate.usagePath)); ok {
			mergeHostedImageGenToolUsage(gjson.GetBytes(body, candidate.imageUsagePath), &usage)
			return usage, true
		}
	}
	return OpenAIUsage{}, false
}

// openAIResponsesCompletedEventIsEmpty reports whether a response.completed /
// response.done SSE payload carries no usage, no error and no output items.
// The accumulated usage is consulted too, because OpenAI may deliver usage on
// an earlier event. An empty terminal event after a stream with no semantic
// output is treated as a silent upstream refusal (issue #5009).
func openAIResponsesCompletedEventIsEmpty(data []byte, usage *OpenAIUsage) bool {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return false
	}
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.ImageInputTokens > 0 || usage.ImageOutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0) {
		return false
	}
	usageValue := gjson.GetBytes(data, "usage")
	responseUsage := gjson.GetBytes(data, "response.usage")
	if (usageValue.Exists() && usageValue.Type != gjson.Null) || (responseUsage.Exists() && responseUsage.Type != gjson.Null) {
		return false
	}
	errorValue := gjson.GetBytes(data, "error")
	responseError := gjson.GetBytes(data, "response.error")
	if (errorValue.Exists() && errorValue.Type != gjson.Null) || (responseError.Exists() && responseError.Type != gjson.Null) {
		return false
	}
	if output := gjson.GetBytes(data, "response.output"); output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	return true
}

func mergeHostedImageGenToolUsage(imageGen gjson.Result, usage *OpenAIUsage) {
	if !imageGen.Exists() || !imageGen.IsObject() {
		return
	}
	if usage.ImageOutputTokens == 0 {
		if v := imageGen.Get("output_tokens_details.image_tokens").Int(); v > 0 {
			usage.ImageOutputTokens = int(v)
		}
	}
	if usage.ImageInputTokens == 0 {
		if v := imageGen.Get("input_tokens_details.image_tokens").Int(); v > 0 {
			usage.ImageInputTokens = int(v)
		}
	}
}

func extractOpenAIResponseIDFromJSONBytes(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "id").String()); id != "" {
		return id
	}
	return strings.TrimSpace(gjson.GetBytes(body, "response.id").String())
}

const openAIHTTPResponseOwnerContextKey = "openai_http_response_owner"

type openAIHTTPResponseOwner struct {
	userID   int64
	apiKeyID int64
}

// SetOpenAIHTTPResponseOwner marks the authenticated downstream owner whose
// successful Responses IDs may be used for later HTTP continuations.
func SetOpenAIHTTPResponseOwner(c *gin.Context, userID, apiKeyID int64) {
	if c == nil || userID <= 0 || apiKeyID <= 0 {
		return
	}
	c.Set(openAIHTTPResponseOwnerContextKey, openAIHTTPResponseOwner{userID: userID, apiKeyID: apiKeyID})
}

// ValidateOpenAIHTTPResponseOwner authorizes a continuation by downstream
// tenant. API key identity is retained in the binding, while keys owned by the
// same user remain interoperable.
func (s *OpenAIGatewayService) ValidateOpenAIHTTPResponseOwner(
	ctx context.Context,
	groupID int64,
	responseID string,
	userID, apiKeyID int64,
) (bool, error) {
	if s == nil || strings.TrimSpace(responseID) == "" || userID <= 0 || apiKeyID <= 0 {
		return false, nil
	}
	ownerUserID, ownerAPIKeyID, found, err := s.getOpenAIWSStateStore().GetHTTPResponseOwner(ctx, groupID, responseID)
	if err != nil || !found {
		return false, err
	}
	return ownerUserID == userID || (ownerUserID <= 0 && ownerAPIKeyID == apiKeyID), nil
}

// BindOpenAIHTTPResponseOwner records an HTTP continuation owner independently
// from the upstream account selected for that response.
func (s *OpenAIGatewayService) BindOpenAIHTTPResponseOwner(
	ctx context.Context,
	groupID int64,
	responseID string,
	userID, apiKeyID int64,
) error {
	if s == nil {
		return nil
	}
	return s.getOpenAIWSStateStore().BindHTTPResponseOwner(
		ctx, groupID, responseID, userID, apiKeyID, s.openAIWSResponseStickyTTL(),
	)
}

func (s *OpenAIGatewayService) bindHTTPResponseAccount(ctx context.Context, c *gin.Context, account *Account, responseID string) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return
	}
	// The client may close the stream immediately after receiving its terminal
	// event, canceling the request context before these durable affinity writes
	// run. Preserve request values, but give all Redis writes one bounded budget
	// independent of the downstream connection lifecycle.
	bindBaseCtx := context.Background()
	if ctx != nil {
		bindBaseCtx = context.WithoutCancel(ctx)
	}
	bindCtx, cancel := context.WithTimeout(bindBaseCtx, openAIWSStateStoreRedisTimeout)
	defer cancel()

	groupID := getOpenAIGroupIDFromContext(c)
	ttl := s.openAIWSResponseStickyTTL()
	logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, store.BindResponseAccount(bindCtx, groupID, responseID, account.ID, ttl))
	if rawOwner, ok := c.Get(openAIHTTPResponseOwnerContextKey); ok {
		if owner, ok := rawOwner.(openAIHTTPResponseOwner); ok && owner.userID > 0 && owner.apiKeyID > 0 {
			if err := s.BindOpenAIHTTPResponseOwner(bindCtx, groupID, responseID, owner.userID, owner.apiKeyID); err != nil {
				logger.L().Warn(
					"openai.http_bind_response_owner_failed",
					zap.Int64("group_id", groupID),
					zap.Int64("account_id", account.ID),
					zap.Int64("user_id", owner.userID),
					zap.Int64("api_key_id", owner.apiKeyID),
					zap.String("response_id", truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen)),
					zap.Error(err),
				)
			}
		}
	}
}

func openAIUsageFromGJSON(value gjson.Result) (OpenAIUsage, bool) {
	if !value.Exists() || !value.IsObject() {
		return OpenAIUsage{}, false
	}
	counts := apicompat.ParseOpenAIUsageCounts(value)
	inputTokens, outputTokens := int64(counts.InputTokens), int64(counts.OutputTokens)
	// xAI reports visible output separately from reasoning_tokens; OpenAI
	// folds reasoning into completion/output. Use total_tokens to tell them apart.
	reasoningTokens := max(int(firstPositiveGJSONInt(
		value.Get("completion_tokens_details.reasoning_tokens"),
		value.Get("output_tokens_details.reasoning_tokens"),
	)), 0)
	if reasoningTokens > 0 {
		outputTokens = xai.IncludeIndependentReasoningTokens(
			inputTokens, outputTokens, value.Get("total_tokens").Int(), int64(reasoningTokens),
		)
	}
	cacheReadTokens := counts.CacheReadTokens
	cacheCreationTokens := counts.CacheWriteTokens
	imageOutputTokens := value.Get("output_tokens_details.image_tokens").Int()
	if imageOutputTokens == 0 {
		imageOutputTokens = value.Get("completion_tokens_details.image_tokens").Int()
	}
	// 图片输入 token（如 gpt-image-2 的 /v1/images/edits 带图请求），
	// 上游在 input_tokens_details.image_tokens 单独回传，用于图/文输入分价计费。
	// 普通文本请求该字段为 0，走原路径行为不变。
	imageInputTokens := firstPositiveGJSONInt(
		value.Get("input_tokens_details.image_tokens"),
		value.Get("prompt_tokens_details.image_tokens"),
	)
	return OpenAIUsage{
		InputTokensPresent:              counts.InputTokensPresent,
		OutputTokensPresent:             counts.OutputTokensPresent,
		CacheReadInputTokensPresent:     counts.CacheReadSource != "" && value.Get(counts.CacheReadSource).Num >= 0,
		CacheCreationInputTokensPresent: counts.CacheWriteSource != "" && value.Get(counts.CacheWriteSource).Num >= 0,
		CacheReadSource:                 counts.CacheReadSource,
		CacheWriteSource:                counts.CacheWriteSource,
		InputTokens:                     int(inputTokens),
		ImageInputTokens:                imageInputTokens,
		OutputTokens:                    int(outputTokens),
		CacheCreationInputTokens:        cacheCreationTokens,
		CacheReadInputTokens:            cacheReadTokens,
		ImageOutputTokens:               int(imageOutputTokens),
	}, true
}

func openAICacheReadTokensFromUsage(value gjson.Result) int {
	return apicompat.ParseOpenAIUsageCounts(value).CacheReadTokens
}

func openAICacheCreationTokensFromUsage(value gjson.Result) int {
	return apicompat.ParseOpenAIUsageCounts(value).CacheWriteTokens
}

func (s *OpenAIGatewayService) handleNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, mappedModel string) (schedResult *openaiNonStreamingResult, schedErr error) {
	bodyKind, usageSource := "", "missing"
	var observedUsage *OpenAIUsage
	observedBody := false
	defer func() {
		if !observedBody {
			return
		}
		if schedResult == nil && observedUsage != nil {
			schedResult = &openaiNonStreamingResult{OpenAIUsage: observedUsage, usage: observedUsage}
		}
		if schedResult != nil {
			schedResult.ResponseFactsKnown, schedResult.UsagePresent, schedResult.UsageSource = true, usageSource != "missing", usageSource
			schedResult.ResponseBodyKind = bodyKind
		}
		noteOpenAIResponsesFacts(c, account, usageSource, OpenAIResponsesTerminal{}, bodyKind)
	}()

	schedCapture := currentSchedAttempt(ctx)
	defer func() {
		if schedResult != nil {
			schedCapture.noteUsage(schedResult.usage)
		}
		schedCapture.noteError(schedErr)
	}()
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, nonstreamReadError(ctx, resp, err)
	}

	observedBody = true

	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	if bodyHasSSEFraming(body) {
		observeOpenAISSEBody(observer, string(body))
	} else {
		observeSchedPayload(c, body)
		observer.ObserveOpenAI(body, strings.TrimSpace(gjson.GetBytes(body, "type").String()))
	}

	// A complete JSON object is authoritative even when the upstream labels it SSE.
	bodyLooksLikeSSE := bodyHasSSEFraming(body)
	if !isOpenAIResponseJSONObject(body) {
		if bodyLooksLikeSSE || isEventStreamResponse(resp.Header) {
			observedBody = false
			return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
		}
		return nil, s.writeOpenAINonStreamingProtocolError(resp, c, "Upstream returned an invalid non-streaming response")
	}
	if account != nil && account.IsGrok() && isOpenAIResponsesCompactPath(c) {
		body, err = convertGrokResponseToOpenAICompact(body)
		if err != nil {
			return nil, fmt.Errorf("convert Grok compact response: %w", err)
		}
	}

	bodyKind = "json"
	usageValue, present := openAIResponsesUsageEvidence(body, "upstream_json")
	usage := &usageValue
	observedUsage = usage
	if present {
		usageSource = "upstream_json"
	}
	failedResponse := openAIResponsesJSONFailed(body)
	if failedResponse {
		schedCapture.noteUsage(usage)
		observedUsage = nil
		noteOpenAIResponsesRecoveryHint(c, account, resp.Header, body, time.Now())
	} else {
		logOpenAISuccessMissingUsage(ctx, c, account, resp, usage, "json", false, usageSource != "missing")
	}

	// Replace model in response if needed
	if originalModel != mappedModel {
		body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
	}
	body, err = restoreGrokResponsesClientToolPayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore Grok Responses client tool response: %w", err)
	}
	body, err = restoreOpenAIResponsesClientToolPayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI Responses client tool response: %w", err)
	}
	body, err = restoreOpenAIResponsesNamespacePayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI namespace response: %w", err)
	}
	body = restoreCodexToolNamesFromContext(c, body)
	body, err = restoreOpenAICollaboration(c, "", body, false)
	if err != nil {
		return nil, s.writeOpenAINonStreamingProtocolError(resp, c, openAICollabRestoreMessage)
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	// Codex 协议要求 /responses/compact JSON 响应携带 x-codex-turn-state
	// （codex-api/src/endpoint/compact.rs 从响应头捕获），显式回传。
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json; charset=utf-8"
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Del("Content-Length")

	if failedResponse {
		if !writeOpenAICompactSSEBridge(c, http.StatusBadGateway, body) {
			c.Data(resp.StatusCode, contentType, body)
		}
		// Preserve the upstream envelope and normal client-name restoration,
		// while keeping failed JSON out of successful billing ownership.
		message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
		if message == "" {
			message = "Upstream response failed"
		}
		setOpsUpstreamError(c, http.StatusBadGateway, message, "")
		return nil, fmt.Errorf("upstream response failed: %s", message)
	}
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		upstreamAccepted: openAIM3JSONAccepted(body),
		imageCount:       countOpenAIResponseImageOutputsFromJSONBytes(body),
		imageOutputSizes: collectOpenAIResponseImageOutputSizesFromJSONBytes(body),
		searchCount:      countGrokNativeSearchCallsFromJSONBytes(body),
	}, nil
}

// A complete JSON document can still describe a failed response. It must not
// enter the successful no-usage path or acquire billing ownership.
func openAIResponsesJSONFailed(body []byte) bool {
	switch gjson.GetBytes(body, "status").String() {
	case "failed", "cancelled", "canceled":
		return true
	}
	terminal := ClassifyOpenAIResponsesTerminal("", body)
	return terminal.Valid && terminal.Failure
}

func isOpenAIResponseJSONObject(body []byte) bool {
	return utf8.Valid(body) && json.Valid(body) && gjson.ParseBytes(body).IsObject()
}

func isEventStreamResponse(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

// bodyHasSSEFraming reports whether body contains genuine SSE framing by
// scanning for physical lines that begin with the "data:" or "event:"
// field names, per the SSE spec. Unlike a raw substring scan, this does not
// match when those strings only appear embedded inside JSON string values
// (e.g. "data: foo" quoted as part of an assistant text field), since such
// occurrences never start a physical line in a valid JSON encoding.
func bodyHasSSEFraming(body []byte) bool {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) handleSSEToJSON(resp *http.Response, c *gin.Context, account *Account, body []byte, originalModel, mappedModel string) (result *openaiNonStreamingResult, resultErr error) {
	bodyText := string(body)
	usage, usageSource := s.openAIResponsesUsageFromBody(bodyText)
	terminal, terminalSSEEvent, terminalPayload := extractOpenAISSETerminalEvent(bodyText)
	defer func() {
		if result == nil {
			result = &openaiNonStreamingResult{OpenAIUsage: usage, usage: usage}
		}
		result.ResponseFactsKnown, result.UsagePresent, result.UsageSource = true, usageSource != "missing", usageSource
		result.TerminalSeen, result.TerminalValid, result.ResponseBodyKind = terminal.Seen, terminal.Valid, "sse"
		noteOpenAIResponsesFacts(c, account, usageSource, terminal, "sse")
	}()

	// Observe earlier markers before reconstructing the terminal response.
	// Keep the raw frames for aggregation; only the final JSON is delivered.
	var collabErr error
	if openAICollaborationAttempt(c) != nil {
		forEachOpenAISSEFrame(bodyText, func(event string, data []byte) {
			if collabErr == nil {
				_, collabErr = restoreOpenAICollaboration(c, event, data, true)
			}
		})
		if collabErr != nil {
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, openAICollabRestoreMessage)
		}
	}
	terminalType := terminal.EventType
	observeSchedPayload(c, terminalPayload, terminalType)
	if terminal.Failure && terminal.Valid {
		noteOpenAIResponsesRecoveryHint(c, account, resp.Header, terminalPayload, time.Now())
	}
	if terminal.Failure {
		if !terminal.Valid {
			if terminal.Reason == "malformed_wrapped_error" {
				logOpenAIWrappedErrorMalformed(c.Request.Context(), account, "native_sse_aggregate")
			}
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, "Upstream returned an invalid Responses terminal event", OpenAIUpstreamProtocolErrorCode)
		}
		msg := extractOpenAISSEErrorMessage(terminalPayload)
		if msg == "" {
			msg = "Upstream compact response failed"
		}
		wrappedStatus, _, malformed := openAIWrappedUpstreamErrorStatus(terminalSSEEvent, terminalPayload)
		if malformed {
			msg = openAIMalformedWrappedErrorMessage
		} else if repairErr := newHTTPEncryptedContentRepairSignal(httpEncryptedRepairContext(c), c, account, openAIHTTPRepairBody(c), terminalPayload, msg, wrappedStatus, true); repairErr != nil {
			return nil, repairErr
		} else if compactErr := newOpenAICompactFallbackSignal(c, terminalPayload, msg, wrappedStatus); compactErr != nil {
			return nil, compactErr
		}
		if failoverErr := s.nonStreamingTerminalFailureFailover(c, resp, account, false, terminalType, terminalSSEEvent, terminalPayload, msg, mappedModel); failoverErr != nil {
			return nil, failoverErr
		}
		return nil, s.writeOpenAINonStreamingProtocolError(resp, c, msg, openAIStreamErrorField(terminalPayload, "code").String())
	}
	finalResponse, ok := openAIResponsesTerminalResponse(terminal, terminalPayload)
	if !ok {
		return nil, s.writeOpenAINonStreamingProtocolError(resp, c, "Upstream response stream ended before a valid terminal event", OpenAIUpstreamStreamTruncatedCode)
	}

	if ok {
		if outputJSON, reconstructed := mergeResponsesOutputFromSSE(finalResponse, bodyText); reconstructed {
			if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
				finalResponse = patched
			}
		}
		finalResponse = supplementCompactionItemFromSSE(c, finalResponse, bodyText)
		body = finalResponse
		if originalModel != mappedModel {
			body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
		}
		// Correct tool calls in final response
		body = s.correctToolCallsInResponseBody(body)
		restoredBody, restoreErr := restoreGrokResponsesClientToolPayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
		}
		restoredBody, restoreErr = restoreOpenAIResponsesClientToolPayload(c, restoredBody)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI Responses client tool response: %w", restoreErr)
		}
		restoredBody, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredBody)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
		}
		restoredBody = restoreCodexToolNamesFromContext(c, restoredBody)
		restoredBody, restoreErr = restoreOpenAICollaboration(c, "", restoredBody, false)
		if restoreErr != nil {
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, openAICollabRestoreMessage)
		}
		body = restoredBody
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	logOpenAISuccessMissingUsage(c.Request.Context(), c, account, resp, usage, terminalType, false, usageSource != "missing")
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json; charset=utf-8"
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Del("Content-Length")
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		upstreamAccepted: openAIM3AggregateAccepted(bodyText, terminalType),
		imageCount:       countOpenAIImageOutputsFromSSEBody(bodyText),
		imageOutputSizes: collectOpenAIImageOutputSizesFromSSEBody(bodyText),
		searchCount:      countGrokNativeSearchCallsFromSSEBody(bodyText),
	}, nil
}

// forEachOpenAIResponsesFrame dispatches the complete SSE event, including EOF.
// Complete adjacent JSON records retain the legacy missing-separator handling.
// Legacy relays may omit the blank separator between complete JSON objects.
// A valid multiline JSON document remains one event; any partial line fails.
func splitOpenAIResponsesFrame(frame openAICompatSSEFrame) []openAICompatSSEFrame {
	if !strings.Contains(frame.Data, "\n") || json.Valid([]byte(frame.Data)) {
		return []openAICompatSSEFrame{frame}
	}
	lines := strings.Split(frame.Data, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "[DONE]" && !isOpenAIResponseJSONObject([]byte(line)) {
			return []openAICompatSSEFrame{frame}
		}
	}
	frames := make([]openAICompatSSEFrame, 0, len(lines))
	for _, line := range lines {
		frames = append(frames, openAICompatSSEFrame{EventType: frame.EventType, Data: line})
	}
	return frames
}

func forEachOpenAIResponsesFrame(body string, visit func(string, []byte)) {
	var parser openAICompatSSEFrameParser
	emit := func(frame openAICompatSSEFrame, ok bool) {
		if ok {
			for _, event := range splitOpenAIResponsesFrame(frame) {
				visit(event.EventType, []byte(event.Data))
			}
		}
	}
	for _, line := range strings.Split(body, "\n") {
		emit(parser.AddLine(strings.TrimSuffix(line, "\r")))
	}
	emit(parser.Finish())
}

// A3's untyped error never takes the Codex provisional-error recovery path.
func openAIResponsesTypedBareError(payload []byte) bool {
	kind := gjson.GetBytes(payload, "type")
	return isOpenAIResponseJSONObject(payload) && kind.Type == gjson.String && kind.String() == "error"
}

func extractOpenAISSETerminalEvent(body string) (OpenAIResponsesTerminal, string, []byte) {
	var terminal OpenAIResponsesTerminal
	var rawEvent string
	var payload []byte
	forEachOpenAIResponsesFrame(body, func(event string, data []byte) {
		observed := ClassifyOpenAIResponsesTerminal(event, data)
		if (!terminal.Failure || terminal.Valid && openAIResponsesTypedBareError(payload) && (observed.Success || observed.Failure)) && (observed.Seen || observed.Failure) {
			terminal, rawEvent, payload = observed, event, append([]byte(nil), data...)
		}
	})
	return terminal, rawEvent, payload
}

func openAIResponsesTerminalResponse(terminal OpenAIResponsesTerminal, payload []byte) ([]byte, bool) {
	if !terminal.Valid || terminal.Failure || (terminal.Status != "completed" && terminal.Status != "incomplete") {
		return nil, false
	}
	if normalized, changed := normalizeCompletedImageGenerationStatus(payload); changed {
		payload = normalized
	}
	response := gjson.GetBytes(payload, "response")
	return []byte(response.Raw), response.IsObject()
}

func extractOpenAISSEErrorMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	if top := gjson.GetBytes(payload, "type"); top.String() == "error" {
		if message := strings.TrimSpace(openAIStreamErrorField(payload, "message").String()); message != "" {
			return sanitizeUpstreamErrorMessage(message)
		}
		if openAIStreamErrorObject(payload).IsObject() {
			return ""
		}
		return sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if msg := strings.TrimSpace(gjson.GetBytes(payload, path).String()); msg != "" {
			return sanitizeUpstreamErrorMessage(msg)
		}
	}
	return sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
}

func buildOpenAIResponseFailedSSE(ctx context.Context, responseID, model string, source []byte, fallbackMessage string) string {
	return buildOpenAIResponseFailedSSEForClient(ctx, nil, responseID, model, source, fallbackMessage)
}

func buildOpenAIResponseFailedSSEForClient(ctx context.Context, c *gin.Context, responseID, model string, source []byte, fallbackMessage string) string {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	errorType := strings.TrimSpace(openAIStreamErrorField(source, "type").String())
	code := strings.TrimSpace(openAIStreamErrorField(source, "code").String())
	if code == "" {
		code = "upstream_error"
	}
	message := extractOpenAISSEErrorMessage(source)
	if message == "" {
		message = strings.TrimSpace(fallbackMessage)
	}
	message = SanitizeUpstreamErrorMessage(message)
	if message == "" {
		message = "Upstream response failed"
	}
	// This is a semantic error inside an HTTP 200 stream. Use an explicit
	// upstream error status when present; zero leaves eligibility to type/code.
	status := 0
	for _, path := range openAIStreamErrorStatusPaths {
		if candidate := int(gjson.GetBytes(source, path).Int()); candidate >= 400 && candidate < 600 {
			status = candidate
			break
		}
	}
	errorBody := openAIResponseFailureError(ctx, status, errorType, code, message)
	if param := openAIStreamErrorField(source, "param"); param.Exists() && param.Type != gjson.Null {
		errorBody["param"] = param.Value()
	}
	if errorType != "" {
		errorBody["type"] = errorType
	}
	response := gin.H{
		"id":     responseID,
		"object": "response",
		"status": "failed",
		"output": []any{},
		"error":  errorBody,
	}
	if model = strings.TrimSpace(model); model != "" {
		response["model"] = model
	}
	createdAt := time.Now().Unix()
	if at := gjson.GetBytes(source, "response.created_at"); at.Exists() {
		createdAt = at.Int()
	}
	response["created_at"] = createdAt
	for _, path := range []string{"response.usage", "usage"} {
		if value := gjson.GetBytes(source, path); value.IsObject() {
			if normalized := targetProtocolFailureUsage(json.RawMessage(value.Raw)); len(normalized) > 0 {
				response["usage"] = normalized
			}
			break
		}
	}
	frame := gin.H{"type": "response.failed", "response": response}
	if sequence := gjson.GetBytes(source, "sequence_number"); sequence.Exists() {
		frame["sequence_number"] = sequence.Int()
	}
	var opening []byte
	if only, tracked := DeliveryOnlyHeartbeats(c); tracked && only {
		opening = ResponsesCreatedFrame(responseID, model, createdAt)
		if sequence := gjson.GetBytes(source, "sequence_number").Int(); sequence < 1 {
			frame["sequence_number"] = int64(1)
		}
	}
	payload, err := marshalOpenAIUpstreamJSON(frame)
	if err != nil {
		// All values above are JSON primitives, so this is only a defensive fallback.
		payload = []byte(`{"type":"response.failed","response":{"status":"failed","output":[],"error":{"code":"upstream_error","message":"Upstream response failed"}}}`)
	}
	return string(opening) + "event: response.failed\ndata: " + string(restoreOpenAICollaborationError(c, payload)) + "\n\n"
}

func sanitizeOpenAIResponseFailedEventForClient(payload []byte, eventType string, clientOutputStarted bool) ([]byte, bool) {
	eventType = strings.TrimSpace(eventType)
	isFailedEvent := eventType == "response.failed"
	if (!isFailedEvent && eventType != "error") || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
	// 容量降载码对 Codex CLI 是致命错误；事件既然要写给客户端（failover 已不可用），
	// 就改写为客户端可重试的错误码。error 帧与 response.failed 都要改：上游降载
	// 总是先推 error 帧再收 failed，两帧携带同一个错误。
	if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(updated); changed {
		updated = rewritten
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if message := gjson.GetBytes(updated, path); message.Type == gjson.String {
			if sanitized := SanitizeUpstreamErrorMessage(message.String()); sanitized != message.String() {
				if next, err := sjson.SetBytes(updated, path, sanitized); err == nil {
					updated = next
				}
			}
		}
	}
	if !isFailedEvent {
		return updated, !bytes.Equal(updated, payload)
	}
	if clientOutputStarted && isOpenAIContextWindowError(extractOpenAISSEErrorMessage(payload), payload) {
		errorPath := ""
		switch {
		case gjson.GetBytes(updated, "response.error").Exists():
			errorPath = "response.error"
		case gjson.GetBytes(updated, "error").Exists():
			errorPath = "error"
		}
		if errorPath != "" {
			next, err := sjson.SetBytes(updated, errorPath+".type", "invalid_request_error")
			if err != nil {
				return payload, false
			}
			updated = next
			next, err = sjson.SetBytes(updated, errorPath+".code", "context_length_exceeded")
			if err != nil {
				return payload, false
			}
			updated = next
		}
	}
	if !gjson.GetBytes(updated, "response").Exists() {
		return updated, !bytes.Equal(updated, payload)
	}
	for _, path := range []string{
		"response.instructions",
		"response.output",
		"response.usage",
		"response.metadata",
		"response.reasoning",
		"response.tools",
		"response.tool_choice",
		"response.parallel_tool_calls",
		"response.text",
		"response.truncation",
		"response.max_output_tokens",
		"response.incomplete_details",
	} {
		next, err := sjson.DeleteBytes(updated, path)
		if err != nil {
			return payload, false
		}
		updated = next
	}
	return updated, !bytes.Equal(updated, payload)
}

func (s *OpenAIGatewayService) writeOpenAINonStreamingProtocolError(resp *http.Response, c *gin.Context, message string, code ...string) error {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "Upstream returned an invalid non-streaming response"
	}
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	// body-signal compact 心跳可能已把响应头提交为 200，此时只能以
	// response.failed 终止事件回传错误，不能再写 JSON+状态码。
	if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		writeOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", message)
		return fmt.Errorf("non-streaming openai protocol error: %s", message)
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.Header().Del("Content-Length")
	errType, errorCode := "upstream_error", ""
	if len(code) > 0 && code[0] != "" {
		errType, errorCode = "server_error", code[0]
	}
	WriteTargetProtocolError(c, PreOutputProtocolResponses, http.StatusBadGateway, errType, errorCode, "", message)
	return fmt.Errorf("non-streaming openai protocol error: %s", message)
}

func extractCodexFinalResponse(body string) ([]byte, bool) {
	terminal, _, payload := extractOpenAISSETerminalEvent(body)
	return openAIResponsesTerminalResponse(terminal, payload)
}

func normalizeCompletedImageGenerationStatus(data []byte) ([]byte, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data, false
	}

	shouldNormalize := func(item gjson.Result) bool {
		if !item.Exists() || !item.IsObject() ||
			strings.TrimSpace(item.Get("type").String()) != "image_generation_call" {
			return false
		}
		switch strings.TrimSpace(item.Get("status").String()) {
		case "generating", "in_progress":
			return strings.TrimSpace(item.Get("result").String()) != ""
		default:
			return false
		}
	}

	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.output_item.done":
		if !shouldNormalize(gjson.GetBytes(data, "item")) {
			return data, false
		}
		updated, err := sjson.SetBytes(data, "item.status", "completed")
		if err != nil {
			return data, false
		}
		return updated, true
	case "response.completed", "response.done":
		output := gjson.GetBytes(data, "response.output")
		if !output.Exists() || !output.IsArray() {
			return data, false
		}
		updated := data
		changed := false
		for i, item := range output.Array() {
			if !shouldNormalize(item) {
				continue
			}
			next, err := sjson.SetBytes(updated, "response.output."+strconv.Itoa(i)+".status", "completed")
			if err != nil {
				return data, false
			}
			updated = next
			changed = true
		}
		return updated, changed
	default:
		return data, false
	}
}

// responsesStreamOutputItems remembers the raw item carried by each
// response.output_item.done event, keyed by output_index.
//
// reconstructResponseOutputFromSSE already prefers the raw done items over
// delta accumulation when it rebuilds a buffered response, because the
// accumulator models only "one reasoning, one message, N function calls" and
// therefore cannot preserve item identity, per-item status/phase, ordering, or
// item types it does not know about. The streaming path had no equivalent
// because it never sees the whole body at once; this collector gives it one.
type responsesStreamOutputItems struct {
	items map[int]json.RawMessage
}

func newResponsesStreamOutputItems() *responsesStreamOutputItems {
	return &responsesStreamOutputItems{items: make(map[int]json.RawMessage)}
}

// Observe records the item of a response.output_item.done event verbatim. The
// raw JSON is kept byte for byte so vendor extensions and future fields survive
// the rebuild.
func (r *responsesStreamOutputItems) Observe(data []byte) {
	if r == nil || len(data) == 0 || !gjson.ValidBytes(data) {
		return
	}
	if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
		return
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() {
		return
	}
	index := int(gjson.GetBytes(data, "output_index").Int())
	r.items[index] = json.RawMessage(append([]byte(nil), item.Raw...))
}

func (r *responsesStreamOutputItems) HasItems() bool {
	return r != nil && len(r.items) > 0
}

// Count reports how many distinct output items the stream reported as done.
func (r *responsesStreamOutputItems) Count() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}

// BuildOutput returns the remembered items ordered by output_index.
func (r *responsesStreamOutputItems) BuildOutput() ([]byte, bool) {
	if !r.HasItems() {
		return nil, false
	}
	indexes := make([]int, 0, len(r.items))
	for index := range r.items {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]json.RawMessage, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, r.items[index])
	}
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func normalizeResponsesStreamingTerminalOutput(data []byte, acc *apicompat.BufferedResponseAccumulator, doneItems *responsesStreamOutputItems, imageOutputs []json.RawMessage) ([]byte, bool) {
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return data, false
	}

	output := gjson.GetBytes(data, "response.output")
	hasAccumulatedOutput := (acc != nil && acc.HasContent()) || len(imageOutputs) > 0 || doneItems.HasItems()
	if output.Exists() && output.IsArray() {
		terminalCount := len(output.Array())
		// A terminal output carrying at least as many items as the stream
		// reported is left untouched. Carrying fewer means the terminal
		// dropped items the stream already reported as done, and those
		// reported items are the authoritative record of the turn.
		if terminalCount > 0 && terminalCount >= doneItems.Count() {
			return data, false
		}
		if terminalCount == 0 && !hasAccumulatedOutput {
			return data, false
		}
	}

	outputJSON := []byte("[]")
	// Same precedence as reconstructResponseOutputFromSSE: the items the stream
	// actually reported win over anything rebuilt from deltas. Image generation
	// items arrive as done events too, so imageOutputs would duplicate them here.
	if reconstructed, ok := doneItems.BuildOutput(); ok {
		outputJSON = reconstructed
	} else if reconstructed, ok := buildResponsesOutputJSON(acc, imageOutputs); ok {
		outputJSON = reconstructed
	}
	updated, err := sjson.SetRawBytes(data, "response.output", outputJSON)
	if err != nil {
		return data, false
	}
	return updated, true
}

func responsesStreamEventMayContributeToOutput(eventType string) bool {
	switch eventType {
	case "response.output_text.delta",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta":
		return true
	default:
		return false
	}
}

// collectRawResponsesOutputItemsFromSSE keeps raw done items and added-only
// compaction items in the same output-index order as nonstream reconstruction.
func collectRawResponsesOutputItemsFromSSE(bodyText string) ([]byte, bool) {
	return mergeResponsesOutputItems(nil, bodyText, true)
}

// isResponsesCompactionItemType reports whether the item type is the Codex
// remote-compact result item ("compaction", upstream alias "compaction_summary").
func isResponsesCompactionItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// supplementCompactionItemFromSSE 保证 compact 请求的终态 output 携带
// compaction item：终态 output 非空但缺失 compaction、而原始事件流的
// output_item.done（或 added）中存在时（上游不一致形态），以 raw JSON 补入。
// Codex remote compact v2 只从 output_item.done 收集 item 且要求恰好一个
// compaction item——纯流式透传（v0.1.146）下客户端直接读事件流天然拿得到，
// SSE→JSON 提取链路必须给出等价结果。非 compact 请求原样返回。
func supplementCompactionItemFromSSE(c *gin.Context, finalResponse []byte, bodyText string) []byte {
	if !isOpenAIResponsesCompactPath(c) {
		return finalResponse
	}
	if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
		// 空 output 由 reconstructResponseOutputFromSSE 整体修补，不在此处理。
		return finalResponse
	}
	if responsesOutputHasCompactionItem(finalResponse) {
		return finalResponse
	}
	item, found := findRawCompactionItemFromSSE(bodyText)
	if !found {
		return finalResponse
	}
	patched, err := sjson.SetRawBytes(finalResponse, "output.-1", item)
	if err != nil {
		return finalResponse
	}
	return patched
}

// responsesOutputHasCompactionItem reports whether the response JSON already
// carries a compaction item in its output array.
func responsesOutputHasCompactionItem(response []byte) bool {
	for _, item := range gjson.GetBytes(response, "output").Array() {
		if isResponsesCompactionItemType(item.Get("type").String()) {
			return true
		}
	}
	return false
}

// findRawCompactionItemFromSSE 从原始 SSE 事件流中提取第一个 compaction 类
// item 的 raw JSON：output_item.done 优先，output_item.added 兜底。
func findRawCompactionItemFromSSE(bodyText string) (json.RawMessage, bool) {
	var found json.RawMessage
	pick := func(eventType string) {
		forEachOpenAISSEFrame(bodyText, func(effectiveType string, data []byte) {
			if found != nil {
				return
			}
			if effectiveType != eventType {
				return
			}
			item := gjson.GetBytes(data, "item")
			if !item.IsObject() || !isResponsesCompactionItemType(item.Get("type").String()) {
				return
			}
			found = json.RawMessage(item.Raw)
		})
	}
	pick("response.output_item.done")
	if found == nil {
		pick("response.output_item.added")
	}
	return found, found != nil
}

// reconstructResponseOutputFromSSE merges each item's done and delta evidence.
func reconstructResponseOutputFromSSE(bodyText string) ([]byte, bool) {
	return mergeResponsesOutputFromSSE(nil, bodyText)
}

func buildResponsesOutputJSON(acc *apicompat.BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	if (acc == nil || !acc.HasContent()) && len(imageOutputs) == 0 {
		return nil, false
	}
	var output []json.RawMessage
	if acc != nil && acc.HasContent() {
		outputJSON, err := json.Marshal(acc.BuildOutput())
		if err == nil {
			_ = json.Unmarshal(outputJSON, &output)
		}
	}
	output = append(output, imageOutputs...)
	if len(output) == 0 {
		return nil, false
	}

	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

func extractImageGenerationOutputFromSSEData(data []byte, seen map[string]struct{}) (json.RawMessage, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil, false
	}
	if gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return nil, false
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() || item.Get("type").String() != "image_generation_call" {
		return nil, false
	}
	if strings.TrimSpace(item.Get("result").String()) == "" {
		return nil, false
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("output_format").String()) + "|" + strings.TrimSpace(item.Get("result").String())
	}
	if key != "" && seen != nil {
		if _, exists := seen[key]; exists {
			return nil, false
		}
		seen[key] = struct{}{}
	}
	return json.RawMessage(item.Raw), true
}

func (s *OpenAIGatewayService) replaceModelInSSEBody(body, fromModel, toModel string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if _, ok := extractOpenAISSEDataLine(line); !ok {
			continue
		}
		lines[i] = s.replaceModelInSSELine(line, fromModel, toModel)
	}
	return strings.Join(lines, "\n")
}

func compactOpenAIResponsesFrameData(data string) string {
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(data)) == nil {
		return compact.String()
	}
	return data
}

func claimOpenAIResponsesTerminal(c *gin.Context) bool {
	if keepalive := keepaliveFrom(c); keepalive != nil {
		keepalive.Stop()
	}
	return ClaimProtocolTerminal(c)
}

func openAIResponsesFailureMetadata(usage *OpenAIUsage, createdAt int64) gin.H {
	metadata := gin.H{}
	if createdAt > 0 {
		metadata["created_at"] = createdAt
	}
	if usage != nil && (openAIUsageHasTokens(usage) || usage.InputTokensPresent || usage.OutputTokensPresent) {
		wireUsage := gin.H{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.InputTokens + usage.OutputTokens}
		inputDetails := gin.H{}
		if usage.CacheReadInputTokens > 0 || usage.CacheReadInputTokensPresent {
			inputDetails["cached_tokens"] = usage.CacheReadInputTokens
		}
		if usage.CacheCreationInputTokens > 0 || usage.CacheCreationInputTokensPresent {
			inputDetails["cache_write_tokens"] = usage.CacheCreationInputTokens
		}
		if usage.ImageInputTokens > 0 {
			inputDetails["image_tokens"] = usage.ImageInputTokens
		}
		if len(inputDetails) > 0 {
			wireUsage["input_tokens_details"] = inputDetails
		}
		if usage.ImageOutputTokens > 0 {
			wireUsage["output_tokens_details"] = gin.H{"image_tokens": usage.ImageOutputTokens}
		}
		metadata["usage"] = wireUsage
	}
	return metadata
}

func openAIResponsesFailureSource(payload []byte, usage *OpenAIUsage, createdAt, nextSequence int64) []byte {
	if !gjson.GetBytes(payload, "sequence_number").Exists() {
		if patched, err := sjson.SetBytes(payload, "sequence_number", nextSequence); err == nil {
			payload = patched
		}
	}
	for key, value := range openAIResponsesFailureMetadata(usage, createdAt) {
		if gjson.GetBytes(payload, "response."+key).Exists() || key == "usage" && gjson.GetBytes(payload, "usage").IsObject() {
			continue
		}
		if patched, err := sjson.SetBytes(payload, "response."+key, value); err == nil {
			payload = patched
		}
	}
	return payload
}

func openAIResponsesTailUsageCandidate(previous bool, rawEvent string, data []byte, terminal OpenAIResponsesTerminal) bool {
	if terminal.Failure {
		return false
	}
	event := effectiveOpenAISSEEventType(data, rawEvent)
	if resilience.ResponsesEventOutputDisposition(event, data).SemanticOutput {
		return false
	}
	usage, present := openAIResponsesUsageEvidence(data, "upstream_sse_progressive")
	if present && usage.InputTokensPresent && usage.OutputTokensPresent {
		return true
	}
	return previous
}
