package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// A planned attempt deadline is distinct from client cancellation, including
// when net/http reports context.Canceled while closing a response body.
type geminiAttemptDeadlineError struct{}

func (*geminiAttemptDeadlineError) Error() string { return "Gemini attempt deadline exceeded" }
func (*geminiAttemptDeadlineError) Unwrap() error { return context.DeadlineExceeded }

type geminiStreamStallError struct{}

func (*geminiStreamStallError) Error() string { return "Gemini stream stalled" }

type geminiAttempt struct {
	mu       sync.Mutex
	ctx      context.Context
	parent   context.Context
	cancel   context.CancelCauseFunc
	timer    *time.Timer
	timeout  time.Duration
	stall    time.Duration
	output   bool
	terminal bool
	phase    string
	protocol string
}

func geminiTextRequest(model, action string, body []byte) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || strings.Contains(model, "image") || action == "countTokens" ||
		!gjson.ValidBytes(body) || !GroupSystemPromptSupportsRequest(model, body) {
		return false
	}
	for _, path := range []string{"generationConfig.responseModalities", "generation_config.response_modalities", "request.generationConfig.responseModalities"} {
		for _, modality := range gjson.GetBytes(body, path).Array() {
			if !strings.EqualFold(modality.String(), "TEXT") {
				return false
			}
		}
	}
	return true
}

// One guard spans the header wait, all in-place retries and all six body
// collection paths. Only replay-safe nonstream bodies get an attempt cutoff.
func newGeminiAttempt(ctx context.Context, account *Account, model, action string, body []byte, stream bool, protocol string, started time.Time) *geminiAttempt {
	guarded, cancel := context.WithCancelCause(ctx)
	g := &geminiAttempt{ctx: guarded, parent: ctx, cancel: cancel, protocol: protocol}
	g.headerWait()
	if account == nil || account.Platform != PlatformGemini || !geminiTextRequest(model, action, body) {
		return g
	}
	if stream && resilience.Current().StallOn() {
		g.stall = time.Duration(resilience.Current().Stall.TextStallSeconds) * time.Second
	}
	if b := resilienceTextBudget(ctx); b != nil && (stream || (!requestReplayUnsafe(ctx) && requestBodyReplaySafe(body, true))) {
		last := resilienceLastCandidate(ctx, account.ID)
		if !last {
			tier := time.Duration(resilience.Current().Budget.GeminiFirstEventTimeoutSeconds) * time.Second
			g.timeout = b.AttemptTimeout(tier, last, started)
			if g.timeout > 0 {
				g.mu.Lock()
				g.resetTimer(g.timeout-time.Since(started), &geminiAttemptDeadlineError{})
				g.mu.Unlock()
			}
		}
	}
	return g
}

// Callers hold mu after construction. A stopped callback checks the current
// generation before canceling, so content racing a timer can safely disarm it.
func (g *geminiAttempt) resetTimer(d time.Duration, cause error) {
	previous := g.timer
	if previous != nil {
		previous.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(d, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.timer == timer {
			g.cancel(cause)
		}
	})
	g.timer = timer
}

func (g *geminiAttempt) headerWait() {
	g.mu.Lock()
	g.phase = resilience.StageHeaderWait
	g.mu.Unlock()
	resilience.StateFrom(g.parent).SetStage(resilience.StageHeaderWait)
}

func (g *geminiAttempt) wrap(resp *http.Response) {
	g.mu.Lock()
	g.phase = resilience.StagePreOutput
	g.mu.Unlock()
	resilience.StateFrom(g.parent).SetStage(resilience.StagePreOutput)
	body := &geminiAttemptBody{ReadCloser: resp.Body, attempt: g}
	body.stop = context.AfterFunc(g.ctx, body.closeBody)
	resp.Body = body
}

func (g *geminiAttempt) close() {
	g.mu.Lock()
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.mu.Unlock()
	g.cancel(context.Canceled)
}

type geminiAttemptBody struct {
	io.ReadCloser
	attempt  *geminiAttempt
	stop     func() bool
	once     sync.Once
	closeErr error
}

func (b *geminiAttemptBody) closeBody() { b.once.Do(func() { b.closeErr = b.ReadCloser.Close() }) }
func (b *geminiAttemptBody) Close() error {
	b.stop()
	b.closeBody()
	return b.closeErr
}
func (b *geminiAttemptBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if cause := context.Cause(b.attempt.ctx); cause != nil {
		return 0, cause
	}
	return n, err
}

func geminiAttemptOf(resp *http.Response) *geminiAttempt {
	if resp != nil {
		if body, ok := resp.Body.(*geminiAttemptBody); ok {
			return body.attempt
		}
	}
	return nil
}

// completeGeminiResponse disarms only after the complete nonstream body (JSON
// or OAuth SSE) has been collected, before any downstream response is written.
func completeGeminiResponse(resp *http.Response, err error) error {
	if g := geminiAttemptOf(resp); g != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		if cause := context.Cause(g.ctx); cause != nil && !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			return cause
		}
		if g.timer != nil {
			g.timer.Stop()
			g.timer = nil
		}
	}
	return err
}

// Pings, empty data and usage-only events do not release the deadline.
// Content (including function calls) starts the existing text stall window;
// a terminal disarms both timers.
func observeGeminiStream(resp *http.Response, payload []byte) error {
	g := geminiAttemptOf(resp)
	if g == nil {
		return nil
	}
	if inner, err := unwrapGeminiResponse(payload); err == nil {
		payload = inner
	}
	root := gjson.ParseBytes(payload)
	terminal := string(payload) == "[DONE]" || root.Get("error").Exists() || root.Get("promptFeedback.blockReason").String() != ""
	content := false
	for _, candidate := range root.Get("candidates").Array() {
		terminal = terminal || candidate.Get("finishReason").String() != ""
		for _, part := range candidate.Get("content.parts").Array() {
			content = content || part.Get("text").String() != "" || part.Get("functionCall").Exists() || part.Get("inlineData").Exists()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if cause := context.Cause(g.ctx); cause != nil {
		return cause
	}
	if terminal || content {
		g.startOutput(terminal)
	}
	return nil
}

// Callers hold mu. Committing a full prelude has the same replay boundary as
// content: stop the first-output deadline and watch the committed stream.
func (g *geminiAttempt) startOutput(terminal bool) {
	g.output = true
	g.terminal = g.terminal || terminal
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	if !g.terminal && g.stall > 0 {
		g.resetTimer(g.stall, &geminiStreamStallError{})
	}
}

func (g *geminiAttempt) outputStarted() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.output
}

func geminiStreamMayWrite(resp *http.Response) bool {
	g := geminiAttemptOf(resp)
	return g == nil || g.timeout <= 0 || g.outputStarted()
}

// Native passthrough stages every prelude byte, including metadata and SSE
// field lines. Only the forwarding goroutine owns this bounded buffer.
type geminiNativeStreamWriter struct {
	c       *gin.Context
	resp    *http.Response
	pending bytes.Buffer
}

func (w *geminiNativeStreamWriter) write(line string) error {
	if !geminiStreamMayWrite(w.resp) {
		if w.pending.Len()+len(line) <= ssePendingLimit {
			w.pending.WriteString(line)
			return nil
		}
		g := geminiAttemptOf(w.resp)
		g.mu.Lock()
		if cause := context.Cause(g.ctx); cause != nil {
			g.mu.Unlock()
			return cause
		}
		g.startOutput(false)
		g.mu.Unlock()
	}
	if err := w.flush(); err != nil {
		return err
	}
	if _, err := io.WriteString(w.c.Writer, line); err != nil {
		return err
	}
	w.c.Writer.Flush()
	return nil
}

func (w *geminiNativeStreamWriter) flush() error {
	if w.pending.Len() == 0 {
		return nil
	}
	if _, err := w.pending.WriteTo(w.c.Writer); err != nil {
		return err
	}
	w.c.Writer.Flush()
	return nil
}

// Native upstreams may ignore alt=sse and send JSON. Keep that response intact,
// collecting it under the same cutoff before the existing passthrough loop.
func geminiNativeStreamReader(c *gin.Context, resp *http.Response, cfg *config.Config) (io.Reader, error) {
	g := geminiAttemptOf(resp)
	if g == nil || g.timeout <= 0 || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return resp.Body, nil
	}
	body, err := ReadUpstreamResponseBody(resp.Body, cfg, c, openAITooLargeError)
	if err = completeGeminiResponse(resp, err); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.output, g.terminal = true, true
	g.mu.Unlock()
	return bytes.NewReader(body), nil
}

func (g *geminiAttempt) finish(c *gin.Context, rl *RateLimitService, account *Account, err error) error {
	if err == nil || g.parent.Err() != nil {
		return err
	}
	var deadline *geminiAttemptDeadlineError
	var stall *geminiStreamStallError
	switch cause := context.Cause(g.ctx); {
	case errors.As(cause, &deadline) && !g.outputStarted() && OpenAICompactKeepaliveAdjustedWrittenSize(c) <= 0:
		fe := (&firstEventGuard{timeout: g.timeout}).failoverError(c, account, g.phase)
		fe.cause = cause
		rl.ApplyResilienceOutcome(g.parent, account, resilience.Result{Category: resilience.CategoryFirstOutputTimeout}, "Gemini attempt deadline")
		return fe
	case errors.As(cause, &stall):
		noteResilienceStall(g.parent, rl, account)
		MarkResponseCommitted(c)
		body := []byte(`{"error":{"type":"stream_timeout","message":"Upstream stream stalled"}}`)
		if g.protocol == "gemini" {
			_, _ = c.Writer.Write(append(append([]byte("data: "), body...), []byte("\n\n")...))
		} else {
			_, _ = c.Writer.Write(preOutputTerminalFrame(g.parent, g.protocol, http.StatusGatewayTimeout, body))
		}
		c.Writer.Flush()
	}
	return err
}
