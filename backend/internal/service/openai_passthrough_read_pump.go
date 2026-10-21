package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Read pump of the OpenAI Responses passthrough (W1.2). With
// stream.strict_terminal the passthrough applies the first-output deadline
// and the stall check of the native path; both need the upstream read in
// its own goroutine, as Forward does.

// passthroughLineStep is what the read loop does after one upstream line.
type passthroughLineStep uint8

const (
	passthroughLineContinue passthroughLineStep = iota
	passthroughLineBreak
	passthroughLineReturn
)

// openAIPassthroughPump connects the read pump to the stream handler.
type openAIPassthroughPump struct {
	processLine func(line string) passthroughLineStep
	finish      func(scanErr error) (*openaiStreamingResultPassthrough, error)
	returned    func() (*openaiStreamingResultPassthrough, error)
	contentSeen func() bool
	// onDeadline ends the attempt when no content came before the deadline.
	onDeadline func() (*openaiStreamingResultPassthrough, error)
	// onStall checks the idle time; done reports that the attempt ended.
	onStall     func(lastRead time.Time, deadlineArmed bool) (result *openaiStreamingResultPassthrough, err error, done bool)
	onKeepalive func()

	firstOutputDeadline time.Time
	stallCheckBase      time.Duration
	keepaliveInterval   time.Duration
}

// firstOutputDeadline returns when the first output is due, or zero.
func firstOutputDeadline(start time.Time, timeout time.Duration) time.Time {
	if timeout <= 0 {
		return time.Time{}
	}
	return start.Add(timeout)
}

// runOpenAIPassthroughReadPump reads the upstream in a goroutine that owns
// scanBuf. The body is closed on return, which releases a pump still
// blocked in a read.
func runOpenAIPassthroughReadPump(resp *http.Response, scanner *openAISSEJSONDocumentScanner, scanBuf *sseScannerBuf64K, pump openAIPassthroughPump) (*openaiStreamingResultPassthrough, error) {
	defer func() { _ = resp.Body.Close() }()
	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())
	go func() {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for scanner.Scan() {
			lastReadAt.Store(time.Now().UnixNano())
			select {
			case events <- scanEvent{line: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- scanEvent{err: err}:
			case <-done:
			}
		}
	}()
	defer close(done)

	var deadlineCh <-chan time.Time
	if !pump.firstOutputDeadline.IsZero() {
		remaining := time.Until(pump.firstOutputDeadline)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		deadlineCh = timer.C
	}
	var stallCh, keepaliveCh <-chan time.Time
	if pump.stallCheckBase > 0 {
		ticker := time.NewTicker(resilienceStallCheckPeriod(pump.stallCheckBase))
		defer ticker.Stop()
		stallCh = ticker.C
	}
	if pump.keepaliveInterval > 0 {
		ticker := time.NewTicker(pump.keepaliveInterval)
		defer ticker.Stop()
		keepaliveCh = ticker.C
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return pump.finish(nil)
			}
			if ev.err != nil {
				return pump.finish(ev.err)
			}
			switch pump.processLine(ev.line) {
			case passthroughLineReturn:
				return pump.returned()
			case passthroughLineBreak:
				return pump.finish(nil)
			}
			if deadlineCh != nil && pump.contentSeen() {
				deadlineCh = nil
			}
		case <-deadlineCh:
			deadlineCh = nil
			if pump.contentSeen() {
				continue
			}
			return pump.onDeadline()
		case <-stallCh:
			if result, err, ended := pump.onStall(time.Unix(0, lastReadAt.Load()), deadlineCh != nil); ended {
				return result, err
			}
		case <-keepaliveCh:
			pump.onKeepalive()
		}
	}
}

// writeOpenAIPassthroughRuleTerminal ends a passthrough stream that only
// carried heartbeats with response.created and response.failed (one id,
// sequence 0 and 1) holding the error passthrough rule's type and message.
// The rule's status decides a retry hint (RetryAdviceEligible).
func writeOpenAIPassthroughRuleTerminal(ctx context.Context, c *gin.Context, status int, errType, message string) {
	MarkOpsStreamError(c, errType, message, status)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"type": errType, "code": errType, "message": message}})
	if _, err := c.Writer.Write(preOutputTerminalFrame(ctx, PreOutputProtocolResponses, status, body)); err == nil {
		c.Writer.Flush()
	}
}
