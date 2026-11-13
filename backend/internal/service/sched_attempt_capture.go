package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// SetSchedAttemptRecorder is called once during assembly, before serving requests.
func (s *OpenAIGatewayService) SetSchedAttemptRecorder(r SchedAttemptRecorder) {
	s.schedAttemptRecorder = r
}

// Each send owns this handle. A parent's cleanup never finishes a recursive send.
type schedAttemptCapture struct {
	mu                                         sync.Mutex
	state                                      *resilience.RequestState
	ctx                                        context.Context
	accountID                                  int64
	attempt                                    SchedAttempt
	sendErr                                    error
	started                                    time.Time
	first, done, client                        bool
	expectTerminal, terminal, benignIncomplete bool
	failure                                    *resilience.Outcome
	usage                                      *SchedAttemptUsage
}

func (s *OpenAIGatewayService) beginSchedAttempt(ctx context.Context, account *Account, body []byte, bodyBytes int, stream bool) *schedAttemptCapture {
	if account == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	st := resilience.StateFrom(ctx)
	selection, ok := st.SchedSelection()
	record := s.schedAttemptRecorder != nil && ok && selection.AccountID == account.ID
	change := pendingOpenAIStickyChange(ctx)
	if !record && (change == nil || change.target != account.ID) {
		return nil
	}
	a := &schedAttemptCapture{state: st, ctx: ctx, accountID: account.ID, started: time.Now(), attempt: schedAttemptNoop{}}
	if change != nil && change.target == account.ID {
		change.mu.Lock()
		change.activeAttempt, change.attemptSucceeded, change.attemptCanceled = a, false, false
		change.mu.Unlock()
	}
	if !record {
		st.SetCurrentAttempt(a)
		return a
	}
	groupID, sessionKey := st.SessionIdentity()
	in := SchedAttemptStart{AccountID: account.ID, GroupID: groupID, Model: selection.Model,
		Stream: stream, SessionKey: sessionKey, RequestID: st.GatewayRequestID()}
	if test, ok := st.SchedTest().(*SchedTestAttempt); ok && test != nil {
		copy := *test
		in.Test = &copy
	}
	a.attempt = s.schedAttemptRecorder.Begin(in)
	st.SetCurrentAttempt(a)
	return a
}
func currentSchedAttempt(ctx context.Context) *schedAttemptCapture {
	a, _ := resilience.StateFrom(ctx).CurrentAttempt().(*schedAttemptCapture)
	return a
}
func (a *schedAttemptCapture) MarkFirstOutput(at time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done || a.first {
		return
	}
	a.first = true
	a.attempt.MarkFirstOutput(at)
}
func schedUsage(u *OpenAIUsage) *SchedAttemptUsage {
	if u == nil || u.InputTokens <= 0 {
		return nil
	}
	read, creation := max(u.CacheReadInputTokens, 0), max(u.CacheCreationInputTokens, 0)
	return &SchedAttemptUsage{
		PromptTokens:        u.InputTokens,
		InputTokens:         max(u.InputTokens-read-creation, 0),
		CacheReadTokens:     read,
		CacheCreationTokens: creation,
	}
}
func (a *schedAttemptCapture) noteUsage(u *OpenAIUsage) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if usage := schedUsage(u); !a.done && usage != nil {
		a.usage = usage
	}
}
func (a *schedAttemptCapture) observe(data []byte) {
	if a == nil {
		return
	}
	if u, ok := extractOpenAIUsageFromJSONBytes(data); ok {
		a.noteUsage(&u)
	}
	root := gjson.ParseBytes(data)
	typ := root.Get("type").String()
	if isOpenAIWSTerminalEvent(typ) || typ == "error" || typ == "message_stop" || strings.TrimSpace(string(data)) == "[DONE]" {
		a.markTerminal()
	}
	if typ == "" {
		var terminal openAIRawStreamTerminalState
		terminal.ObserveDataLine(strings.TrimSpace(string(data)))
		if terminal.Terminated() {
			a.markTerminal()
		}
	}
	status := root.Get("status").String()
	if typ == "response.incomplete" || status == "incomplete" {
		reason := root.Get("response.incomplete_details.reason").String()
		if reason == "" {
			reason = root.Get("incomplete_details.reason").String()
		}
		if reason == "max_output_tokens" || reason == "content_filter" {
			a.mu.Lock()
			a.benignIncomplete = true
			a.mu.Unlock()
		}
	}
	failed := typ == "error" || typ == "response.failed" || typ == "response.incomplete" || typ == "response.cancelled" || typ == "response.canceled" || status == "failed" || status == "incomplete" || root.Get("error").IsObject()
	if !failed {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	o := resilience.Outcome{Source: resilience.SourceStreamEvent, StatusCode: http.StatusOK, Body: append([]byte(nil), data...)}
	errRoot := root.Get("response.error")
	if !errRoot.IsObject() {
		errRoot = root.Get("error")
	}
	o.Code, o.Type, o.Message = errRoot.Get("code").String(), errRoot.Get("type").String(), errRoot.Get("message").String()
	a.failure = &o
}
func (a *schedAttemptCapture) finishHTTP(resp *http.Response, body []byte) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if !a.done {
		a.failure = &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: resp.StatusCode, Headers: resp.Header, Body: append([]byte(nil), body...)}
	}
	a.mu.Unlock()
	a.finish(nil, nil)
}
func (a *schedAttemptCapture) cancel() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.client = true
	a.mu.Unlock()
	a.finish(nil, nil)
}
func (a *schedAttemptCapture) timeout(plan resilience.TimeoutPlan) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	noteOpenAIStickyAttemptOutcome(a, false)
	if a.state.ClientCanceled() {
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: string(resilience.CategoryClientCancel), Usage: a.usage})
	} else if plan.Applied() || a.first {
		detail := string(classifyUpstreamOutcome(resilience.Outcome{Source: resilience.SourceNetwork, Err: context.DeadlineExceeded}).Category)
		if a.first {
			detail = string(resilience.CategoryStall)
		}
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: detail, Usage: a.usage})
	} else {
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Detail: string(resilience.CategoryFirstOutputTimeout), Waited: time.Since(a.started), Usage: a.usage})
	}
	a.state.ClearCurrentAttemptIf(a)
}
func (a *schedAttemptCapture) finish(result *OpenAIForwardResult, err error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	if err == nil {
		err = a.sendErr
	}
	if result != nil {
		if u := schedUsage(&result.Usage); u != nil {
			a.usage = u
		}
		a.client = a.client || result.ClientDisconnect
	}
	a.done = true
	defer a.state.ClearCurrentAttemptIf(a)
	canceled := a.client || a.state.ClientCanceled()
	success := !canceled && a.ctx.Err() == nil && err == nil && a.failure == nil &&
		(!a.expectTerminal || a.terminal) && !a.benignIncomplete &&
		(result == nil || !result.PartialFailure && (result.UpstreamTerminalEvent == "" || result.UpstreamTerminalEvent == "response.completed" || result.UpstreamTerminalEvent == "response.done"))

	noteOpenAIStickyAttemptOutcome(a, success)
	// Client cancellation is an explicit delivery fact, never a generic deadline.
	if canceled {
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: string(resilience.CategoryClientCancel), Usage: a.usage})
		return
	}
	failure := a.failure
	if failure == nil && err != nil {
		var httpErr *UpstreamFailoverError
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 {
			failure = &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: httpErr.StatusCode, Body: httpErr.ResponseBody, Headers: httpErr.ResponseHeaders}
		} else {
			failure = &resilience.Outcome{Source: resilience.SourceNetwork, Err: err}
		}
	}
	if !success {
		detail := string(classifyUpstreamOutcome(resilience.Outcome{Source: resilience.SourceStreamEvent}).Category)
		if failure != nil {
			detail = string(classifyUpstreamOutcome(*failure).Category)
		}
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: detail, Usage: a.usage})
		return
	}
	a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: a.usage})
}

// FinishSchedAttemptAtHandlerExit only repairs outcomes invisible to the service.
func FinishSchedAttemptAtHandlerExit(ctx context.Context) {
	a := currentSchedAttempt(ctx)
	if a == nil {
		return
	}
	if resilience.StateFrom(ctx).ClientCanceled() {
		a.cancel()
		CompleteOpenAIStickyChange(ctx, a.accountID, false, true)
		return
	}
	a.finishUnfinished()
	CompleteOpenAIStickyChange(ctx, a.accountID, false, false)
}
func (a *schedAttemptCapture) finishUnfinished() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done {
		a.done = true
		noteOpenAIStickyAttemptOutcome(a, false)
		a.attempt.Finish(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: string(resilience.CategoryTransient), Usage: a.usage})
		a.state.ClearCurrentAttemptIf(a)
	}
}
func schedTestRequest(ctx context.Context) bool {
	test, ok := resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt)
	return ok && test != nil
}
func observeSchedPayload(c *gin.Context, data []byte, eventType ...string) {
	if c == nil || c.Request == nil {
		return
	}
	a := currentSchedAttempt(c.Request.Context())
	if a == nil {
		return
	}
	if len(eventType) > 0 {
		data = []byte(openAICompatPayloadWithEventType(string(data), eventType[0]))
	}
	a.observe(data)
}
func markSchedWSOutput(a *schedAttemptCapture, payload []byte) {
	if a == nil {
		return
	}
	root := gjson.ParseBytes(payload)
	typ := strings.TrimSpace(root.Get("type").String())
	disposition := resilience.ResponsesEventOutputDisposition(typ, payload)
	if disposition.Visible || semanticJSONOutput(root) {
		a.MarkFirstOutput(time.Now())
	}
}

// sendSchedHTTP is used only by the enumerated text forwarding paths.
func (s *OpenAIGatewayService) sendSchedHTTP(ctx context.Context, account *Account, req *http.Request, proxyURL string, body []byte, stream bool, owner **schedAttemptCapture) (*http.Response, error) {
	// Builders may add group prompts or normalize cache fields after the caller's
	// payload is prepared; ContentLength is the final wire length without a reread.
	*owner = s.beginSchedAttempt(ctx, account, body, int(req.ContentLength), stream)
	resp, err := s.doOpenAIUpstream(req, proxyURL, account)
	if err != nil {
		(*owner).noteError(err)
	} else if resp != nil && *owner != nil {
		(*owner).expectTerminal = isEventStreamResponse(resp.Header)
	}
	return resp, err
}
func (a *schedAttemptCapture) noteError(err error) {
	if a == nil || err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done {
		a.sendErr = err
	}
}
func noteSchedError(c *gin.Context, err error) {
	if c != nil && c.Request != nil {
		currentSchedAttempt(c.Request.Context()).noteError(err)
	}
}
func noteSchedStall(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	a := currentSchedAttempt(c.Request.Context())
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done && a.failure == nil {
		a.failure = &resilience.Outcome{Source: resilience.SourceStall}
	}
}
func finishSchedRead(c *gin.Context, err error) {
	if c == nil || c.Request == nil {
		return
	}
	// EOF may precede buffered delivery or a protocol conversion's final
	// output. The forwarding function owns successful completion.
	if !errors.Is(err, io.EOF) {
		currentSchedAttempt(c.Request.Context()).noteError(err)
	}
}

func cancelSchedClient(c *gin.Context) {
	if c != nil && c.Request != nil {
		currentSchedAttempt(c.Request.Context()).cancel()
	}
}

// One holder per passthrough connection; an idle connection owns no attempt.
type schedWSTurnCapture struct {
	mu     sync.Mutex
	active *schedAttemptCapture
}

func (t *schedWSTurnCapture) begin(s *OpenAIGatewayService, ctx context.Context, account *Account, payload []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active.finishUnfinished()
	t.active = s.beginSchedAttempt(ctx, account, payload, len(payload), true)
}
func (t *schedWSTurnCapture) observe(payload []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active.observe(payload)
	markSchedWSOutput(t.active, payload)
}
func (t *schedWSTurnCapture) finish(result *OpenAIForwardResult, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if result == nil && err == nil {
		err = errors.New("missing terminal event")
	}
	t.active.finish(result, err)
	if t.active != nil {
		FinishOpenAIStickyForward(t.active.ctx, t.active.accountID, result, err)
	}
	t.active = nil
}
func (t *schedWSTurnCapture) cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active.cancel()
	if t.active != nil {
		CompleteOpenAIStickyChange(t.active.ctx, t.active.accountID, false, true)
	}
}

func (a *schedAttemptCapture) markTerminal() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terminal = true
}
