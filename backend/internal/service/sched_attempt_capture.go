package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
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
	terminalEvent                              string
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
	if !record && (change == nil || change.target != account.ID) && !st.GatewayRequestEnabled() {
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
	a.attempt = s.schedAttemptRecorder.Begin(in)
	st.SetCurrentAttempt(a)
	return a
}

func (a *schedAttemptCapture) finishRecord(in SchedAttemptFinish) {
	var input, read, creation *int
	if in.Usage != nil {
		input, creation = &in.Usage.InputTokens, &in.Usage.CacheCreationTokens
		if in.Usage.CacheReadTokensPresent {
			read = &in.Usage.CacheReadTokens
		}
	}
	a.state.NoteGatewayAttempt(in.Outcome == schedquality.OutcomeOK, in.Detail, input, read, creation)
	a.attempt.Finish(in)
}
func currentSchedAttempt(ctx context.Context) *schedAttemptCapture {
	a, _ := resilience.StateFrom(ctx).CurrentAttempt().(*schedAttemptCapture)
	return a
}
func (a *schedAttemptCapture) MarkFirstOutput(at time.Time) {
	// RequestState's dependency-free AttemptMarker uses the valid-output bridge.
	a.MarkOutput(OutputValid, at)
}

func (a *schedAttemptCapture) MarkOutput(kind SchedOutputKind, at time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done || (kind == OutputValid && a.first) {
		return
	}
	if kind == OutputValid {
		a.first = true
	}
	a.attempt.MarkOutput(kind, at)
}
func schedUsage(u *OpenAIUsage) *SchedAttemptUsage {
	if u == nil || u.InputTokens <= 0 {
		return nil
	}
	read, creation := max(u.CacheReadInputTokens, 0), max(u.CacheCreationInputTokens, 0)
	return &SchedAttemptUsage{
		PromptTokens:           u.InputTokens,
		InputTokens:            max(u.InputTokens-read-creation, 0),
		CacheReadTokens:        read,
		CacheReadTokensPresent: u.CacheReadInputTokensPresent,
		CacheCreationTokens:    creation,
	}
}

func isBenignOpenAIIncomplete(payload []byte) bool {
	root := gjson.ParseBytes(payload)
	typ := strings.TrimSpace(root.Get("type").String())
	if typ != "response.incomplete" && strings.TrimSpace(root.Get("status").String()) != "incomplete" {
		return false
	}
	reason := strings.TrimSpace(root.Get("response.incomplete_details.reason").String())
	if reason == "" {
		reason = strings.TrimSpace(root.Get("incomplete_details.reason").String())
	}
	return reason == "max_output_tokens" || reason == "content_filter"
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
	} else if typ == "message_delta" {
		// Native Anthropic readers also accept a valid stop_reason without
		// message_stop. Use their protocol evidence, not transport EOF.
		var terminal anthropicBufferedEvidence
		terminal.observe(typ, string(data))
		if terminal.stopped {
			a.markTerminal()
		}
	}
	status := root.Get("status").String()
	terminalEvent := typ
	if terminalEvent == "" && (status == "completed" || status == "failed" || status == "incomplete" || status == "cancelled" || status == "canceled") {
		terminalEvent = "response." + status
	}
	if isOpenAIWSTerminalEvent(terminalEvent) || terminalEvent == "error" {
		a.mu.Lock()
		a.terminalEvent = terminalEvent
		a.mu.Unlock()
	}
	if isBenignOpenAIIncomplete(data) {
		a.mu.Lock()
		a.benignIncomplete = true
		a.mu.Unlock()
		return
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
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: string(resilience.CategoryClientCancel), Usage: a.usage})
	} else if plan.Applied() || a.first {
		detail := string(classifyUpstreamOutcome(resilience.Outcome{Source: resilience.SourceNetwork, Err: context.DeadlineExceeded}).Category)
		if a.first {
			detail = string(resilience.CategoryStall)
		}
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: detail, Usage: a.usage})
	} else {
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.OutcomeTimeout, Detail: string(resilience.CategoryFirstOutputTimeout), Waited: time.Since(a.started), Usage: a.usage})
	}
	a.state.ClearCurrentAttemptIf(a)
}
func (a *schedAttemptCapture) finish(result *OpenAIForwardResult, err error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if result != nil {
		result.BenignIncomplete = result.BenignIncomplete || a.benignIncomplete
		if result.UpstreamTerminalEvent == "" {
			result.UpstreamTerminalEvent = a.terminalEvent
		}
	}
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
		(!a.expectTerminal || a.terminal) &&
		(result == nil || result.CompletedForDelivery())

	noteOpenAIStickyAttemptOutcome(a, success)
	// Client cancellation is an explicit delivery fact, never a generic deadline.
	if canceled {
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.Outcome(5), Detail: string(resilience.CategoryClientCancel), Usage: a.usage})
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
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: detail, Usage: a.usage})
		return
	}
	a.finishRecord(SchedAttemptFinish{Outcome: schedquality.OutcomeOK, Usage: a.usage})
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
		a.finishRecord(SchedAttemptFinish{Outcome: schedquality.OutcomeAccountFault, Detail: string(resilience.CategoryTransient), Usage: a.usage})
		a.state.ClearCurrentAttemptIf(a)
	}
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
		a.MarkOutput(OutputValid, time.Now())
	}
}

// sendSchedHTTP is used only by the enumerated text forwarding paths.
func (s *OpenAIGatewayService) sendSchedHTTP(ctx context.Context, account *Account, req *http.Request, proxyURL string, body []byte, stream bool, owner **schedAttemptCapture) (*http.Response, error) {
	st := resilience.StateFrom(ctx)
	*owner = nil
	if st != nil {
		// Legacy service calls may carry state only for stream accounting and
		// deliberately detach their first send. Revalidate admitted requests
		// and handler retries without changing that existing behavior.
		if err := ctx.Err(); err != nil && (st.Budget() != nil || st.LastUpstreamFailure() != nil) {
			if !errors.Is(err, context.Canceled) {
				return nil, schedHTTPPreDispatchFailure(ctx)
			}
			return nil, err
		}
		now := time.Now()
		if hint, ok := st.AccountRetryNotBefore(account.ID); ok && now.Before(hint.NotBefore) {
			return nil, schedHTTPPreDispatchFailure(ctx)
		}
		// Handler retries are admitted after their wait. In-service repairs
		// have a separate grant, including encrypted repair on the last attempt;
		// do not revoke that grant by checking the handler attempt count again.
	}
	st.SetUpstreamRecoveryHint(nil)
	st.SetUpstreamAttemptTarget(nil)
	if target := schedHTTPAttemptTarget(account, req); target != nil {
		st.SetUpstreamAttemptTarget(target)
	}
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

func schedHTTPPreDispatchFailure(ctx context.Context) *UpstreamFailoverError {
	previous := resilience.StateFrom(ctx).LastUpstreamFailure()
	var failure *UpstreamFailoverError
	if errors.As(previous, &failure) && failure != nil {
		copy := *failure
		copy.PreDispatchSelectionInvalidated = true
		copy.NextAccountAction = NextAccountStop
		copy.cause = previous
		return &copy
	}
	return &UpstreamFailoverError{
		StatusCode: http.StatusServiceUnavailable, ClientStatusCode: http.StatusServiceUnavailable,
		ClientMessage: "Service temporarily unavailable", NextAccountAction: NextAccountStop,
		PreDispatchSelectionInvalidated: true, cause: previous,
	}
}

// schedHTTPAttemptTarget describes the actual built endpoint, without request
// credentials or path/query data. Unknown endpoint shapes keep protocol unknown.
func schedHTTPAttemptTarget(account *Account, req *http.Request) *resilience.UpstreamAttemptTarget {
	if account == nil || account.ID <= 0 || req == nil || req.URL == nil {
		return nil
	}
	u := req.URL
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())
	if (scheme != "http" && scheme != "https") || host == "" {
		return nil
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return nil
		}
		port = strconv.Itoa(n)
	}
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	protocol := ""
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/responses"), strings.HasSuffix(path, "/responses/compact"):
		protocol = PreOutputProtocolResponses
	case strings.HasSuffix(path, "/chat/completions"):
		protocol = PreOutputProtocolChat
	case strings.HasSuffix(path, "/messages"):
		protocol = PreOutputProtocolAnthropic
	}
	return &resilience.UpstreamAttemptTarget{AccountID: account.ID, Protocol: protocol, Origin: scheme + "://" + host}
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
	resilience.StateFrom(ctx).NoteGatewaySend(account.ID)
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

// noteGatewayWSOutput runs after a successful client write, unlike the attempt
// clock which measures receipt from upstream.
func noteGatewayWSOutput(ctx context.Context, payload []byte) {
	root := gjson.ParseBytes(payload)
	disposition := resilience.ResponsesEventOutputDisposition(strings.TrimSpace(root.Get("type").String()), payload)
	if disposition.Visible || semanticJSONOutput(root) {
		resilience.StateFrom(ctx).NoteSemanticOutput(time.Now())
	}
}
