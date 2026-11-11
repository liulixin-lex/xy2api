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

type schedNoopRecorder struct{}
type schedNoopAttempt struct{}

func (schedNoopRecorder) Begin(SchedAttemptStart) SchedAttempt         { return schedNoopAttempt{} }
func (schedNoopRecorder) SetObserver(func(schedquality.AttemptSample)) {}
func (schedNoopRecorder) Flush(context.Context) error                  { return nil }
func (schedNoopAttempt) MarkFirstOutput(time.Time)                     {}
func (schedNoopAttempt) FinishOK(*SchedAttemptUsage)                   {}
func (schedNoopAttempt) FinishFault(string)                            {}
func (schedNoopAttempt) FinishTimeout(time.Duration)                   {}
func (schedNoopAttempt) FinishOther(string)                            {}

// Each send owns this handle. A parent's cleanup never finishes a recursive send.
type schedAttemptCapture struct {
	mu                                         sync.Mutex
	state                                      *resilience.RequestState
	ctx                                        context.Context
	attempt                                    SchedAttempt
	sendErr                                    error
	started                                    time.Time
	first, done, client                        bool
	expectTerminal, terminal, benignIncomplete bool
	failure                                    *resilience.Outcome
	usage                                      *SchedAttemptUsage
}

func (s *OpenAIGatewayService) beginSchedAttempt(ctx context.Context, account *Account, body []byte, stream bool) *schedAttemptCapture {
	if account == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	st := resilience.StateFrom(ctx)
	selection, ok := st.SchedSelection()
	if !ok || selection.AccountID != account.ID {
		return nil
	}
	groupID, sessionKey := st.SessionIdentity()
	effort := ""
	if parsed := extractOpenAIReasoningEffortFromBody(body); parsed != nil {
		effort = *parsed
	}
	in := SchedAttemptStart{AccountID: account.ID, GroupID: groupID, Model: selection.Model, StickyWarm: selection.StickyWarm,
		Effort: effort, BodyBytes: len(body), Stream: stream, SessionKey: sessionKey, RequestID: st.GatewayRequestID()}
	if test, ok := st.SchedTest().(*SchedTestAttempt); ok && test != nil {
		copy := *test
		in.Test = &copy
	}
	recorder := s.schedAttemptRecorder
	if recorder == nil {
		recorder = schedNoopRecorder{}
	}
	a := &schedAttemptCapture{state: st, ctx: ctx, started: time.Now(), attempt: recorder.Begin(in)}
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
	return &SchedAttemptUsage{PromptTokens: u.InputTokens, CacheReadTokens: u.CacheReadInputTokens}
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
			return
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
	if plan.Applied() {
		a.attempt.FinishOther("budget_cut")
	} else {
		a.attempt.FinishTimeout(time.Since(a.started))
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
	// This is the sole mapping from send evidence to scheduler outcomes.
	if a.client || a.ctx.Err() != nil || errors.Is(err, context.Canceled) {
		if a.first {
			a.attempt.FinishOK(a.usage)
		} else {
			a.attempt.FinishOther("client_cancel")
		}
		return
	}
	if a.benignIncomplete && a.failure == nil {
		a.attempt.FinishOK(a.usage)
		return
	}
	failure := a.failure
	if failure == nil && err != nil {
		var httpErr *UpstreamFailoverError
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && len(httpErr.ResponseBody) > 0 {
			failure = &resilience.Outcome{Source: resilience.SourceHTTP, StatusCode: httpErr.StatusCode, Body: httpErr.ResponseBody, Headers: httpErr.ResponseHeaders}
		}
	}
	if failure != nil {
		category := resilience.Classify(*failure, nil).Category
		switch category {
		case resilience.CategoryBalance, resilience.CategoryAuth, resilience.CategoryRateLimitReset, resilience.CategoryRateLimit, resilience.CategoryCapacityShed, resilience.CategoryModelUnsupported, resilience.CategoryClientError, resilience.CategoryUpstreamRequestRestriction:
			a.attempt.FinishOther(string(category))
			return
		case resilience.CategoryTransient, resilience.CategoryStall:
			a.attempt.FinishFault(string(category))
			return
		}
		a.attempt.FinishFault("unclassified")
		return
	}
	if err != nil || a.expectTerminal && !a.terminal || result != nil && (result.PartialFailure || result.UpstreamTerminalEvent == "response.failed") {
		a.attempt.FinishFault("unclassified")
		return
	}
	a.attempt.FinishOK(a.usage)
}

// FinishSchedAttemptAtHandlerExit only repairs outcomes invisible to the service.
func FinishSchedAttemptAtHandlerExit(ctx context.Context) {
	a := currentSchedAttempt(ctx)
	if a == nil {
		return
	}
	if ctx.Err() != nil {
		a.cancel()
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done {
		a.done = true
		a.attempt.FinishOther("unfinished")
		a.state.ClearCurrentAttemptIf(a)
	}
}
func schedTestRequest(ctx context.Context) bool {
	test, ok := resilience.StateFrom(ctx).SchedTest().(*SchedTestAttempt)
	return ok && test != nil
}
func observeSchedPayload(c *gin.Context, data []byte, eventType ...string) {
	if len(eventType) > 0 {
		data = []byte(openAICompatPayloadWithEventType(string(data), eventType[0]))
	}
	if c != nil && c.Request != nil {
		currentSchedAttempt(c.Request.Context()).observe(data)
	}
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
func (s *OpenAIGatewayService) sendSchedHTTP(ctx context.Context, account *Account, req *http.Request, proxyURL string, stream bool, owner **schedAttemptCapture) (*http.Response, error) {
	var body []byte
	if req.GetBody != nil {
		if r, err := req.GetBody(); err == nil {
			body, _ = io.ReadAll(r)
			_ = r.Close()
		}
	}
	*owner = s.beginSchedAttempt(ctx, account, body, stream)
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
func finishSchedRead(c *gin.Context, err error) {
	if c == nil || c.Request == nil {
		return
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	currentSchedAttempt(c.Request.Context()).finish(nil, err)
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
	t.active = s.beginSchedAttempt(ctx, account, payload, true)
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
	t.active = nil
}
func (t *schedWSTurnCapture) cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active.cancel()
}

func (a *schedAttemptCapture) markTerminal() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.terminal = true
}
