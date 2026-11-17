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
	openaiwsv2 "github.com/liulixin-lex/xy2api/internal/service/openai_ws_v2"
	"github.com/tidwall/gjson"
)

// SchedAttemptDetailOutputIncomplete identifies delivered output that did not
// end normally; error attribution maps it to an interrupted output.
const SchedAttemptDetailOutputIncomplete = "OUTPUT_INCOMPLETE"

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
	stream                                     bool
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
	a := &schedAttemptCapture{state: st, ctx: ctx, accountID: account.ID, started: time.Now(), stream: stream, attempt: schedAttemptNoop{}}
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
		CacheReadTokensPresent: u.CacheReadInputTokensPresent || u.CacheReadInputTokens > 0,
		CacheCreationTokens:    creation,
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

// classifySchedTerminal adapts only capture evidence. Forwarding keeps its SSE
// envelope validation: bare JSON must not become an SSE terminal there.
func classifySchedTerminal(data []byte) OpenAIResponsesTerminal {
	terminal := ClassifyOpenAIResponsesTerminal("", data)
	if !gjson.ValidBytes(data) {
		return terminal
	}
	root := gjson.ParseBytes(data)
	typ := root.Get("type").String()
	if !root.IsObject() || root.Get("response").Exists() {
		return terminal
	}
	var envelope []byte
	if typ == "" || typ == "response" {
		switch status := root.Get("status").String(); status {
		case "completed", "incomplete", "failed", "cancelled", "canceled":
			envelope = []byte(`{"type":"response.` + status + `","response":` + string(data) + `}`)
		}
	} else if isOpenAIWSTerminalEvent(typ) {
		// WS readers accept terminal types without a response shell. HTTP
		// readers can still reject them: their returned error wins in finish.
		envelope = []byte(`{"type":"` + typ + `","response":{}}`)
		if typ == "response.done" {
			envelope = []byte(`{"type":"response.done","response":{"status":"completed"}}`)
		}
	}
	if envelope != nil {
		adapted := ClassifyOpenAIResponsesTerminal("", envelope)
		adapted.ErrorPresent = adapted.ErrorPresent || terminal.ErrorPresent
		return adapted
	}
	return terminal
}

func (a *schedAttemptCapture) observe(data []byte, eventType ...string) {
	if a == nil {
		return
	}
	typHint := ""
	if len(eventType) > 0 {
		typHint = eventType[0]
	}
	a.observeOutput(data, time.Now(), typHint)
	if typHint != "" {
		data = []byte(openAICompatPayloadWithEventType(string(data), typHint))
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
	terminalEvent := typ
	if terminalEvent == "" {
		terminalEvent = "response." + root.Get("status").String()
	}
	if isOpenAIWSTerminalEvent(terminalEvent) || terminalEvent == "error" {
		a.mu.Lock()
		a.terminalEvent = terminalEvent
		a.mu.Unlock()
	}
	terminal := classifySchedTerminal(data)
	if terminal.Seen {
		a.mu.Lock()
		a.terminal = a.terminal || terminal.Valid
		// Some Responses transports accept a typed provisional error followed
		// by a completed response. Keep their final decision: explicit failed
		// responses remain sticky, and a consumer error still fails finish.
		if terminal.Success && a.failure != nil && a.failure.Source == resilience.SourceStreamEvent && openAIResponsesTypedBareError(a.failure.Body) {
			a.failure = nil
		}
		a.mu.Unlock()
	}
	// Readers retain response.done as a successful envelope regardless of its
	// nested status. A forwarding error still takes precedence in finish.
	if typ == "response.done" {
		return
	}
	if terminal.BenignIncomplete() {
		a.mu.Lock()
		a.benignIncomplete = true
		a.mu.Unlock()
		return
	}
	if !terminal.QualityFailure() {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return
	}
	// A later provisional error must not replace an authoritative failure;
	// otherwise a subsequent completed event could incorrectly erase it.
	if a.failure != nil && (a.failure.Source != resilience.SourceStreamEvent || !openAIResponsesTypedBareError(a.failure.Body)) {
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
	// The reader can prove a complete JSON document despite an SSE header.
	// Such responses do not require an SSE terminal (status may be omitted).
	jsonComplete := result != nil && result.ResponseFactsKnown && result.ResponseBodyKind == "json"
	success := !canceled && a.ctx.Err() == nil && err == nil && a.failure == nil &&
		(!a.expectTerminal || a.terminal || jsonComplete) &&
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
			terminal := classifySchedTerminal(failure.Body)
			if failure.Source == resilience.SourceStreamEvent && terminal.Valid && terminal.Status == "incomplete" && !terminal.BenignIncomplete() {
				detail = SchedAttemptDetailOutputIncomplete
			}
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
	a.observe(data, eventType...)
}
func markSchedWSOutput(a *schedAttemptCapture, payload []byte) {
	if a == nil {
		return
	}
	a.observeOutput(payload, time.Now())
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
	if t.active != nil {
		typ := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		if openaiwsv2.IsTokenEvent(typ) {
			ms := int(time.Since(t.active.started).Milliseconds())
			markSchedFirstEvent(t.active, t.active.started, &ms)
		}
	}
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
	if resilience.ClassifyOutput("responses", "", payload).Valid {
		resilience.StateFrom(ctx).NoteSemanticOutput(time.Now())
	}
}
