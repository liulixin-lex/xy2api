package handler

import (
	"bufio"
	"bytes"
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
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/pkg/responseturn"
	middleware2 "github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/tidwall/gjson"
)

const nativeResponseExecutionKey = "native_response_execution"

// NativeResponseManager owns only this process's executions. It is intentionally
// absent from global singletons, persistent storage and another gateway owner.
func (h *OpenAIGatewayHandler) NativeResponseManager() *responseturn.Manager {
	h.nativeResponseOnce.Do(func() {
		if h.nativeResponseTurns == nil {
			h.nativeResponseTurns = responseturn.NewManager(responseturn.Config{})
		}
	})
	return h.nativeResponseTurns
}

type nativeResponseExecution struct {
	mu                sync.Mutex
	turn              *responseturn.Turn
	requestContext    context.Context
	recoveryRequested bool
	accountEligible   bool
	recoveryConfirmed bool
	accountID         int64
}

// Values are preserved explicitly, while cancellation is supplied by the host
// execution controller. Until live protocol proof confirms recovery, the client
// AfterFunc below still cancels this controller immediately.
type nativeExecutionContext struct {
	context.Context
	values context.Context
}

func (c nativeExecutionContext) Value(key any) any {
	if v := c.Context.Value(key); v != nil {
		return v
	}
	return c.values.Value(key)
}

func nativeResponseScope(c *gin.Context) (responseturn.Scope, *service.APIKey, bool) {
	key, ok := middleware2.GetAPIKeyFromContext(c)
	subject, subjectOK := middleware2.GetAuthSubjectFromContext(c)
	if !ok || !subjectOK || key == nil || key.User == nil || key.UserID != subject.UserID {
		return responseturn.Scope{}, nil, false
	}
	group := int64(0)
	if key.GroupID != nil {
		group = *key.GroupID
	}
	return responseturn.Scope{UserID: subject.UserID, APIKeyID: key.ID, GroupID: group, Interface: "responses"}, key, true
}

func (h *OpenAIGatewayHandler) handleNativeResponseCreate(c *gin.Context) bool {
	if _, worker := c.Get(nativeResponseExecutionKey); worker || !isBareOpenAIResponsesPath(c) {
		return false
	}
	policy := service.NativeStreamPolicyFromContext(c.Request.Context())
	if !policy.Delivery {
		return false
	}
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" && !policy.Recovery {
		return false
	}
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if tooLarge, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(tooLarge.Limit))
			return true
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return true
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if !gjson.ValidBytes(body) {
		return false
	}
	background := gjson.GetBytes(body, "background")
	stream := gjson.GetBytes(body, "stream")
	if key != "" && background.Type == gjson.True && (!stream.Exists() || stream.Type == gjson.False) {
		writeNativeResponseError(c, &responseturn.Error{Code: "unsupported_background_idempotency", Status: http.StatusBadRequest, Message: "Idempotent non-streaming background responses require native status polling, which is not enabled"})
		return true
	}
	recoveryRequested := policy.Recovery && background.Type == gjson.True && stream.Type == gjson.True && gjson.GetBytes(body, "store").Type != gjson.False
	if key == "" && !recoveryRequested {
		return false
	}
	scope, _, ok := nativeResponseScope(c)
	if !ok {
		h.errorResponse(c, 401, "authentication_error", "Invalid API key")
		return true
	}
	clientContext := c.Request.Context()
	control, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	base := nativeExecutionContext{Context: control, values: clientContext}
	deadline, _ := clientContext.Deadline()
	turn, created, err := h.NativeResponseManager().Create(responseturn.CreateOptions{Scope: scope, IdempotencyKey: key, Body: body,
		OnCancel: func(reason responseturn.Reason) { cancelNativeControlledReason(clientContext, reason) }, ControlContext: base, Deadline: deadline, PolicyVersion: strconv.FormatInt(policy.Version, 10)})
	if err != nil {
		writeNativeResponseError(c, err)
		return true
	}
	if !created {
		snapshot := turn.Snapshot()
		if len(snapshot.Result) > 0 && snapshot.State != responseturn.StateRunning && snapshot.State != responseturn.StateDetached {
			c.Data(http.StatusOK, "application/json", snapshot.Result)
		} else {
			writeNativeResponseError(c, responseturn.ErrActive)
		}
		return true
	}
	attachment, err := turn.Attach(scope, nil, false)
	if err != nil {
		writeNativeResponseError(c, err)
		return true
	}
	execution := &nativeResponseExecution{turn: turn, requestContext: clientContext, recoveryRequested: recoveryRequested}
	stopClient := context.AfterFunc(clientContext, func() {
		execution.mu.Lock()
		confirmed := execution.recoveryConfirmed
		execution.mu.Unlock()
		if !confirmed {
			turn.Cancel(responseturn.ReasonClientDetached)
		}
		attachment.Close()
	})
	defer stopClient()
	// No middleware-owned gin context is shared with the generation goroutine.
	executionContext, bindErr := service.TransferControlledExecutionContext(clientContext, turn.Context())
	if bindErr != nil {
		turn.Cancel(responseturn.ReasonClientDetached)
		attachment.Close()
		writeNativeResponseError(c, responseturn.ErrUnavailable)
		return true
	}
	worker := c.Copy()
	worker.Request = c.Request.Clone(service.WithNativeStreamDeferredFlush(executionContext))
	worker.Request.Body = io.NopCloser(bytes.NewReader(body))
	worker.Set(nativeResponseExecutionKey, execution)
	writer := newNativeJournalWriter(execution, worker.Request.Context(), h)
	worker.Writer = writer
	done := make(chan struct{})
	stopCancel := context.AfterFunc(turn.Context(), func() { h.cancelAcceptedNativeResponse(turn) })
	defer stopCancel()
	go func() {
		defer close(done)
		defer writer.finish()
		h.Responses(worker)
	}()
	h.relayNativeAttachment(c, attachment, writer)
	attachment.Close()
	// The original ControlledSchedulingMiddleware.Close and user/account release
	// defers remain alive until the one original worker exits, including offline
	// execution. Attachment requests never own these generation resources.
	<-done
	snapshot := turn.Snapshot()
	if snapshot.State == responseturn.StateCancelled {
		h.cancelAcceptedNativeResponse(turn)
	}
	return true
}

// Called at the existing selected-account site, before any dispatch. The same
// runtime, attempt budget, original deadline and account are retained.
func (h *OpenAIGatewayHandler) bindNativeResponseAccount(c *gin.Context, account *service.Account) error {
	raw, ok := c.Get(nativeResponseExecutionKey)
	if !ok {
		return nil
	}
	execution, ok := raw.(*nativeResponseExecution)
	if !ok || account == nil {
		return nil
	}
	execution.mu.Lock()
	defer execution.mu.Unlock()
	snapshot := execution.turn.Snapshot()
	if snapshot.OwnerAccountID != 0 && snapshot.OwnerAccountID != account.ID {
		return responseturn.ErrOwner
	}
	execution.accountID = account.ID
	supported, _ := account.Extra["openai_responses_supported"].(bool)
	execution.accountEligible = execution.recoveryRequested && account.IsOpenAIApiKey() && supported && account.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityResponses)

	return nil
}

func (e *nativeResponseExecution) acceptNativeProof(data []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recoveryConfirmed || !e.accountEligible {
		return
	}
	parsed := gjson.ParseBytes(data)
	typ := parsed.Get("type").String()
	if typ != "response.created" && typ != "response.in_progress" {
		return
	}
	sequence := parsed.Get("sequence_number")
	if sequence.Type != gjson.Number || sequence.Int() < 0 || sequence.Float() != float64(sequence.Int()) || parsed.Get("response.id").String() == "" || parsed.Get("response.background").Type != gjson.True {
		return
	}
	if err := e.turn.BindOwner(e.accountID, parsed.Get("response.id").String()); err != nil {
		return
	}
	if err := e.turn.ConfirmRecovery(responseturn.Capabilities{Protocol: "responses_http_sse", Verified: true, Retrieve: true, NativeCursor: true}); err == nil {
		e.recoveryConfirmed = true
	}
}

func writeNativeResponseError(c *gin.Context, err error) {
	native := responseturn.ErrUnavailable
	var described *responseturn.Error
	if errors.As(err, &described) {
		native = described
	}
	c.AbortWithStatusJSON(native.Status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": native.Code, "message": native.Message}})
}

func nativeResponsePathID(c *gin.Context, cancel bool) (string, bool) {
	path := strings.Trim(c.Param("subpath"), "/")
	if path == "" && c.Request != nil {
		if at := strings.LastIndex(c.Request.URL.Path, "/responses/"); at >= 0 {
			path = c.Request.URL.Path[at+len("/responses/"):]
		}
	}
	parts := strings.Split(path, "/")
	if cancel {
		if len(parts) != 2 || parts[1] != "cancel" {
			return "", false
		}
	} else if len(parts) != 1 {
		return "", false
	}
	id := parts[0]
	if id == "" || !strings.HasPrefix(id, "resp_") {
		return "", false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "", false
		}
	}
	return id, true
}

// ResponsesControlPost returns true only for the dedicated native cancel path;
// input_items, compact, input_tokens and unrelated subpaths retain dispatch.
func (h *OpenAIGatewayHandler) ResponsesControlPost(c *gin.Context) bool {
	if _, ok := nativeResponsePathID(c, true); !ok {
		return false
	}
	h.NativeResponseCancel(c)
	return true
}
func (h *OpenAIGatewayHandler) lookupNativeResponse(c *gin.Context, cancel bool) (*responseturn.Turn, bool) {
	scope, key, ok := nativeResponseScope(c)
	if !ok {
		h.errorResponse(c, 401, "authentication_error", "Invalid API key")
		return nil, false
	}
	id, ok := nativeResponsePathID(c, cancel)
	if !ok {
		writeNativeResponseError(c, responseturn.ErrNotFound)
		return nil, false
	}
	turn, err := h.NativeResponseManager().Lookup(scope, id)
	if err != nil {
		writeNativeResponseError(c, err)
		return nil, false
	}
	snapshot := turn.Snapshot()
	if key.Group != nil && key.Group.ModelAllowlistEnabled() && !key.Group.ModelAllowlist.Allows(snapshot.Model) {
		writeNativeResponseError(c, responseturn.ErrNotFound)
		return nil, false
	}
	return turn, true
}
func (h *OpenAIGatewayHandler) NativeResponseRetrieve(c *gin.Context) {
	turn, ok := h.lookupNativeResponse(c, false)
	if !ok {
		return
	}
	if c.Query("stream") != "true" {
		snapshot := turn.Snapshot()
		if len(snapshot.Result) > 0 {
			c.Data(200, "application/json", snapshot.Result)
			return
		}
		status := string(snapshot.State)
		if snapshot.State == responseturn.StateDetached {
			status = "in_progress"
		}
		c.JSON(200, gin.H{"id": snapshot.UpstreamResponseID, "object": "response", "status": status, "model": snapshot.Model, "output": []any{}})
		return
	}
	var cursor *int64
	if raw, exists := c.GetQuery("starting_after"); exists {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeNativeResponseError(c, responseturn.ErrCursor)
			return
		}
		cursor = &n
	}
	attachment, err := turn.Attach(turn.Snapshot().Scope, cursor, true)
	if err != nil {
		writeNativeResponseError(c, err)
		return
	}
	defer attachment.Close()
	h.relayNativeAttachment(c, attachment, nil)
}
func (h *OpenAIGatewayHandler) NativeResponseCancel(c *gin.Context) {
	turn, ok := h.lookupNativeResponse(c, true)
	if !ok {
		return
	}
	turn.Cancel(responseturn.ReasonUserStop)
	_ = service.CancelControlledRequest(turn.Context(), service.ControlledUserStop)
	h.cancelAcceptedNativeResponse(turn)
	snapshot := turn.Snapshot()
	c.JSON(200, gin.H{"id": snapshot.UpstreamResponseID, "object": "response", "status": snapshot.State})
}
func (h *OpenAIGatewayHandler) cancelAcceptedNativeResponse(turn *responseturn.Turn) {
	snapshot := turn.Snapshot()
	if snapshot.State != responseturn.StateCancelled || !turn.BeginUpstreamCancel() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	confirmed, err := h.gatewayService.CancelNativeResponse(ctx, snapshot.OwnerAccountID, snapshot.UpstreamResponseID, turn.MarkUpstreamCancelRequested)
	if err == nil && confirmed {
		turn.ConfirmUpstreamCancel()
	}
}

func (h *OpenAIGatewayHandler) relayNativeAttachment(c *gin.Context, a *responseturn.Attachment, source *nativeJournalWriter) {
	started := false
	for {
		readContext, stopRead := context.WithTimeout(c.Request.Context(), 15*time.Second)
		err := a.WriteNext(readContext, func(event responseturn.Event) error {
			if !started {
				status := http.StatusOK
				headers := http.Header{"Content-Type": []string{"text/event-stream"}}
				if source != nil {
					status, headers = source.headersSnapshot()
				}
				for name, values := range headers {
					if strings.EqualFold(name, "Content-Length") {
						continue
					}
					c.Writer.Header()[name] = append([]string(nil), values...)
				}
				c.Header("Cache-Control", "no-cache")
				c.Header("X-Accel-Buffering", "no")
				c.Status(status)
				started = true
			}
			// HTTP servers supporting ResponseController get a bounded write. Existing
			// proxy/server write deadlines remain authoritative when unsupported.
			controller := http.NewResponseController(c.Writer)
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			n, err := c.Writer.Write(event.Data)
			if err == nil && n != len(event.Data) {
				err = io.ErrShortWrite
			}
			if err == nil {
				c.Writer.Flush()
				if !event.Replay && !event.Local {
					service.RecordNativeAttachmentFlush(c.Request.Context(), event.ReceivedAt, time.Now())
				}
			}
			_ = controller.SetWriteDeadline(time.Time{})
			return err
		})
		stopRead()
		if errors.Is(err, context.DeadlineExceeded) && c.Request.Context().Err() == nil {
			if started && strings.Contains(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
				err = a.Write(func() error {
					controller := http.NewResponseController(c.Writer)
					_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
					_, writeErr := c.Writer.Write([]byte(": keepalive\n\n"))
					if writeErr == nil {
						c.Writer.Flush()
					}
					_ = controller.SetWriteDeadline(time.Time{})
					return writeErr
				})
				if err != nil {
					a.Detach(responseturn.ReasonSlowConsumer)
					return
				}
			}
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, responseturn.ErrAttachmentReplaced) {
				a.Detach(responseturn.ReasonSlowConsumer)
			}
			return
		}
	}
}

// This gin writer accepts complete native SSE events without withholding
// prelude events. Only incomplete network fragments are buffered. The normal
// protocol line limit remains with the existing forwarding parser.
type nativeJournalWriter struct {
	header     http.Header
	mu         sync.Mutex
	writeMu    sync.Mutex
	status     int
	size       int
	committed  bool
	sentHeader http.Header
	pending    []byte
	jsonBody   []byte
	execution  *nativeResponseExecution
	ctx        context.Context
	handler    *OpenAIGatewayHandler
}

func newNativeJournalWriter(e *nativeResponseExecution, ctx context.Context, h *OpenAIGatewayHandler) *nativeJournalWriter {
	return &nativeJournalWriter{header: make(http.Header), status: 200, size: -1, execution: e, ctx: ctx, handler: h}
}
func (w *nativeJournalWriter) Header() http.Header { return w.header }
func (w *nativeJournalWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.committed {
		w.status = code
	}
}
func (w *nativeJournalWriter) WriteHeaderNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.committed {
		w.committed = true
		w.size = 0
		w.sentHeader = w.header.Clone()
	}
}
func (w *nativeJournalWriter) Status() int                       { w.mu.Lock(); defer w.mu.Unlock(); return w.status }
func (w *nativeJournalWriter) Size() int                         { w.mu.Lock(); defer w.mu.Unlock(); return w.size }
func (w *nativeJournalWriter) Written() bool                     { w.mu.Lock(); defer w.mu.Unlock(); return w.committed }
func (w *nativeJournalWriter) Flush()                            { w.WriteHeaderNow() }
func (w *nativeJournalWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *nativeJournalWriter) CloseNotify() <-chan bool          { return nil }
func (w *nativeJournalWriter) Pusher() http.Pusher               { return nil }
func (w *nativeJournalWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("native HTTP journal does not support hijacking")
}
func (w *nativeJournalWriter) headersSnapshot() (int, http.Header) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.sentHeader.Clone()
}
func (w *nativeJournalWriter) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.WriteHeaderNow()
	w.mu.Lock()
	w.size += len(p)
	kind := w.sentHeader.Get("Content-Type")
	w.mu.Unlock()
	if !strings.Contains(strings.ToLower(kind), "text/event-stream") {
		// Native JSON responses are bounded by the same recovery event budget. This
		// path exists only for opt-in/idempotent create, not ordinary passthrough.
		if len(w.jsonBody)+len(p) > 64<<20 {
			return 0, errors.New("native response JSON exceeds bounded result buffer")
		}
		w.jsonBody = append(w.jsonBody, p...)
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	for {
		at := bytes.Index(w.pending, []byte("\n\n"))
		separator := 2
		if other := bytes.Index(w.pending, []byte("\r\n\r\n")); other >= 0 && (at < 0 || other < at) {
			at = other
			separator = 4
		}
		if at < 0 {
			break
		}
		frame := append([]byte(nil), w.pending[:at+separator]...)
		w.pending = w.pending[at+separator:]
		if err := w.publishFrame(frame); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
func (w *nativeJournalWriter) publishFrame(frame []byte) error {
	var data bytes.Buffer
	identity := ""
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("id:")) {
			identity = strings.TrimSpace(string(line[3:]))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if data.Len() > 0 {
				_ = data.WriteByte('\n')
			}
			_, _ = data.Write(bytes.TrimPrefix(line[5:], []byte(" ")))
		}
	}
	payload := data.Bytes()
	if len(payload) == 0 {
		_, err := w.execution.turn.Publish(w.ctx, responseturn.Event{Data: frame, Local: true})
		return err
	} // Local comments do not commit an upstream turn.
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return nil
	}
	if !gjson.ValidBytes(payload) {
		return errors.New("invalid native response SSE JSON")
	}
	w.execution.acceptNativeProof(payload)
	w.execution.mu.Lock()
	accountID := w.execution.accountID
	w.execution.mu.Unlock()
	if accountID > 0 {
		if err := w.execution.turn.BindOwner(accountID, ""); err != nil {
			return err
		}
	}

	parsed := gjson.ParseBytes(payload)
	responseID := parsed.Get("response.id").String()
	event := responseturn.Event{Data: frame, Identity: identity, ResponseID: responseID, ReceivedAt: service.ConsumeNativeStreamEventReadTime(w.ctx)}
	if n := parsed.Get("sequence_number"); n.Type == gjson.Number && n.Int() >= 0 && n.Float() == float64(n.Int()) {
		v := n.Int()
		event.NativeSequence = &v
	}
	typ := parsed.Get("type").String()
	event.Content = strings.HasSuffix(typ, ".delta") && (parsed.Get("delta").String() != "" || parsed.Get("delta").IsObject())
	switch typ {
	case "response.completed":
		event.Terminal = responseturn.StateCompleted
	case "response.failed", "error":
		event.Terminal = responseturn.StateFailed
	case "response.incomplete":
		event.Terminal = responseturn.StatePartial
	case "response.cancelled":
		event.Terminal = responseturn.StateCancelled
	}
	if event.Terminal != "" {
		if response := parsed.Get("response"); response.IsObject() {
			event.Result = []byte(response.Raw)
		}
	}
	_, err := w.execution.turn.Publish(w.ctx, event)
	return err
}
func (w *nativeJournalWriter) finish() {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if len(w.jsonBody) > 0 {
		status := w.Status()
		terminal := responseturn.StateCompleted
		if status >= 400 {
			terminal = responseturn.StateFailed
		}
		responseID := gjson.GetBytes(w.jsonBody, "id").String()
		if strings.HasPrefix(responseID, "resp_") {
			_ = w.execution.turn.BindOwner(w.execution.accountID, responseID)
		}
		_, _ = w.execution.turn.Publish(w.ctx, responseturn.Event{Data: w.jsonBody, ResponseID: responseID, Terminal: terminal, Result: w.jsonBody})
	}
	snapshot := w.execution.turn.Snapshot()
	if snapshot.State == responseturn.StateRunning || snapshot.State == responseturn.StateDetached {
		state := responseturn.StateFailed
		if snapshot.JournalEvents > 0 {
			state = responseturn.StatePartial
		}
		_ = w.execution.turn.Finish(state, responseturn.ReasonUpstreamFailure, nil)
	}
}

var _ gin.ResponseWriter = (*nativeJournalWriter)(nil)

func cancelNativeControlledReason(ctx context.Context, reason responseturn.Reason) {
	selected := service.ControlledClientDetached
	switch reason {
	case responseturn.ReasonUserStop:
		selected = service.ControlledUserStop
	case responseturn.ReasonAdminCancel:
		selected = service.ControlledAdminCancel
	case responseturn.ReasonLeaseLost:
		selected = service.ControlledLeaseLost
	case responseturn.ReasonSlowConsumer:
		selected = service.ControlledSlowConsumer
	case responseturn.ReasonStartupTimeout:
		selected = service.ControlledStartupTimeout
	case responseturn.ReasonContentTimeout:
		selected = service.ControlledContentTimeout
	case responseturn.ReasonUpstreamFailure, responseturn.ReasonConsistency:
		selected = service.ControlledUpstreamFailure
	}
	_ = service.CancelControlledRequest(ctx, selected)
}
func recordNativeResponseDiagnostics(c *gin.Context, result *service.OpenAIForwardResult) {
	scope, _, ok := nativeResponseScope(c)
	if !ok {
		return
	}
	responseID := ""
	if raw, ok := c.Get(nativeResponseExecutionKey); ok {
		if e, ok := raw.(*nativeResponseExecution); ok {
			responseID = e.turn.Snapshot().UpstreamResponseID
		}
	}
	diagnosticScope := service.NativeDiagnosticScope{UserID: scope.UserID, APIKeyID: scope.APIKeyID, GroupID: scope.GroupID}
	// Both are trace-only aliases. Neither can be used to look up a public turn.
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	service.RecordNativeStreamDiagnostics(c.Request.Context(), requestID, diagnosticScope, responseID)
	if result != nil && result.RequestID != requestID {
		service.RecordNativeStreamDiagnostics(c.Request.Context(), result.RequestID, diagnosticScope, responseID)
	}
}
