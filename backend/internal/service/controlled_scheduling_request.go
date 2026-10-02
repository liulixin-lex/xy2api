package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

type controlledSchedulingContextKey struct{}

// ControlledRequest is shared by all conversion, OAuth and failover paths for
// one logical request. Credentials and request content are never retained.
type ControlledRequest struct {
	mu                 sync.Mutex
	ID                 string
	Started            time.Time
	ClientDeadline     time.Time
	clientContext      context.Context
	Model              string
	Protocol           string
	Reasoning          string
	Stream             bool
	ContextTokens      int64 // -1 means unknown; never infer tokens from byte length.
	SessionID          string
	ReplaySafe         bool
	policyLoaded       bool
	Mode               scheduling.ModeSnapshot
	modeResolved       bool
	Auxiliary          bool
	Policy             scheduling.Policy
	Profile            scheduling.LatencyProfile
	Ledger             *scheduling.AttemptLedger
	Decision           scheduling.Decision
	decisionPending    bool
	fallback           bool
	owner              bool
	ownerAccountID     int64
	control            *ControlledSchedulingService
	history            []SchedulingAttemptTrace
	semanticAt         time.Time
	answerAt           time.Time
	retainedExecutions int
	closeRequested     bool
	closed             bool
	finish             func()
	currentAttemptID   string
	gateRejections     int
	admissionRejected  map[int64]bool // Unsent gate failures; never count as upstream attempts.
	lastBackoffAttempt int
	currentDispatch    *controlledDispatch
	httpCommitted      bool
	attemptCommitted   bool
	semanticSeen       bool
	NativeDelivery     bool
	cancelReason       ControlledCancelReason
	executionCancel    context.CancelFunc
	firstReadToFlushMS *float64
	maxReadToFlushMS   float64
	firstFlushAt       time.Time
	flushCount         int64

	localFailureStarted   bool
	localFailureDelivered bool
	localFailureCancel    context.CancelFunc
}

type SchedulingAttemptTrace struct {
	AccountID              int64                   `json:"account_id"`
	AttemptID              string                  `json:"attempt_id"`
	Priority               int                     `json:"priority"`
	Reason                 string                  `json:"reason"`
	Started                time.Time               `json:"started_at"`
	Outcome                string                  `json:"outcome"`
	FirstEventMS           *int64                  `json:"first_event_ms,omitempty"`
	FirstSemanticMS        *int64                  `json:"first_semantic_ms,omitempty"`
	FirstAnswerMS          *int64                  `json:"first_answer_ms,omitempty"`
	OverallFirstSemanticMS *int64                  `json:"overall_first_semantic_ms,omitempty"`
	RemainingBudgetMS      *int64                  `json:"remaining_budget_ms,omitempty"`
	StopReason             string                  `json:"stop_reason"`
	PolicyVersion          int64                   `json:"policy_version"`
	MetricVersion          string                  `json:"metric_version"`
	HeadersMS              *int64                  `json:"headers_ms,omitempty"`
	AttemptCommittedMS     *int64                  `json:"attempt_committed_ms,omitempty"`
	SendCertainty          ControlledSendCertainty `json:"send_certainty,omitempty"`
	CancelReason           ControlledCancelReason  `json:"cancel_reason,omitempty"`
	FirstProtocolEventMS   *int64                  `json:"first_protocol_event_ms,omitempty"`
	FirstContentMS         *int64                  `json:"first_content_ms,omitempty"`
	UnknownEventCount      int64                   `json:"unknown_event_count,omitempty"`
}

func NewControlledRequestContext(ctx context.Context, protocol string) context.Context {
	deadline, _ := ctx.Deadline()
	r := &ControlledRequest{ID: uuid.NewString(), Started: time.Now(), ClientDeadline: deadline, clientContext: ctx, Protocol: scheduling.CanonicalTransport(protocol), Reasoning: "unknown", ContextTokens: -1, ReplaySafe: true, NativeDelivery: NativeStreamDeliveryEnabled(ctx)}
	return context.WithValue(ctx, controlledSchedulingContextKey{}, r)
}
func controlledRequest(ctx context.Context) *ControlledRequest {
	r, _ := ctx.Value(controlledSchedulingContextKey{}).(*ControlledRequest)
	return r
}

// Preserve ingress tracing in the existing attempt-metadata write. These are
// correlation labels only: they never replace the independent ledger ID, prove
// ownership, or enable replay. Outbound header rewrites cannot change them.
func (r *ControlledRequest) addTraceMetrics(metrics map[string]any) {
	r.mu.Lock()
	ctx, started := r.clientContext, r.Started
	r.mu.Unlock()
	if !started.IsZero() {
		metrics["request_started_at"] = started.UTC().Format(time.RFC3339Nano)
	}
	if ctx == nil {
		return
	}
	for key, name := range map[ctxkey.Key]string{
		ctxkey.RequestID: "http_request_id", ctxkey.ClientRequestID: "client_request_id",
	} {
		value, _ := ctx.Value(key).(string)
		value = strings.TrimSpace(value)
		if value != "" && len(value) <= 64 && utf8.ValidString(value) {
			metrics[name] = value
		}
	}
}

// Freeze WS metadata before selecting the first account. Waiting for a client
// response.create on an idle socket is not part of that turn's first-output D.
func CaptureControlledWSFirstRequest(ctx context.Context, body []byte, received time.Time) {
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.policyLoaded {
		r.mu.Unlock()
		return
	}
	r.Protocol = "ws"
	r.Started = received
	r.mu.Unlock()
	r.metadata(body)
}

// Capture bounded metadata while the existing handler reads the body. This does
// not pre-read, rewind, expand the body limit, or retain sensitive content.
type schedulingMetadataBody struct {
	io.ReadCloser
	request   *ControlledRequest
	buf       bytes.Buffer
	truncated bool
}

func (b *schedulingMetadataBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if !b.truncated && n > 0 {
		if b.buf.Len()+n <= 256*1024 {
			_, _ = b.buf.Write(p[:n])
		} else {
			b.truncated = true
			b.request.mu.Lock()
			b.request.ReplaySafe = false
			b.request.mu.Unlock()
			b.buf.Reset()
		}
	}
	if err == io.EOF && !b.truncated {
		b.request.metadata(b.buf.Bytes())
		b.buf.Reset()
	}
	return n, err
}

// CaptureControlledRequestMetadata reuses the handler's validated body, model
// and stream (including Gemini URL/action). No context token count is inferred.
func CaptureControlledRequestMetadata(ctx context.Context, body []byte, model string, stream bool) {
	r := controlledRequest(ctx)
	if r == nil {
		return
	}
	r.metadata(body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.policyLoaded {
		r.Model = model
		r.Stream = stream
	}
}
func (r *ControlledRequest) metadata(body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.policyLoaded {
		return
	}
	fields, replaySafe, valid := readSchedulingMetadata(bytes.NewReader(body), r.Protocol, "root")
	if !valid {
		r.Reasoning = "unknown"
		r.ReplaySafe = false
		return
	}
	r.ReplaySafe = replaySafe
	if raw, present := fields["model"]; present && json.Unmarshal(raw, &r.Model) != nil {
		r.ReplaySafe = false
	}
	if raw, present := fields["stream"]; present && json.Unmarshal(raw, &r.Stream) != nil {
		r.ReplaySafe = false
	}
	r.Reasoning = controlledReasoningLabel(r.Protocol, fields)
}

// Metadata labels describe explicit client choices, not inferred model defaults.
// In particular, token budgets never imply low/medium/high reasoning effort.
func controlledReasoningLabel(protocol string, fields map[string]json.RawMessage) string {
	var level, budget json.RawMessage
	configured := false
	switch protocol {
	case "gemini":
		raw, present := controlledMetadataAlias(fields, "generationConfig", "generation_config")
		if !present {
			return "default"
		}
		config, valid := controlledMetadataObject(raw)
		if !valid {
			return "unknown"
		}
		raw, present = controlledMetadataAlias(config, "thinkingConfig", "thinking_config")
		if !present {
			return "default"
		}
		thinking, valid := controlledMetadataObject(raw)
		if !valid {
			return "unknown"
		}
		configured = true
		level, _ = controlledMetadataAlias(thinking, "thinkingLevel", "thinking_level")
		budget, _ = controlledMetadataAlias(thinking, "thinkingBudget", "thinking_budget")
	case "messages":
		if raw, present := fields["thinking"]; present {
			configured = true
			thinking, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			var kind string
			if json.Unmarshal(thinking["type"], &kind) != nil {
				return "unknown"
			}
			switch strings.ToLower(strings.TrimSpace(kind)) {
			case "disabled":
				return "none"
			case "enabled", "adaptive":
				budget = thinking["budget_tokens"]
			default:
				return "unknown"
			}
		}
		if raw, present := fields["output_config"]; present {
			output, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			level = output["effort"]
			configured = configured || level != nil
		}
	default:
		if raw, present := fields["reasoning"]; present {
			reasoning, valid := controlledMetadataObject(raw)
			if !valid {
				return "unknown"
			}
			level = reasoning["effort"]
		}
		if level == nil {
			level = fields["reasoning_effort"]
		}
		configured = level != nil
	}
	label := ""
	if level != nil {
		var effort string
		if json.Unmarshal(level, &effort) != nil {
			return "unknown"
		}
		effort = strings.ToLower(strings.TrimSpace(effort))
		valid := false
		switch protocol {
		case "gemini":
			valid = effort == "minimal" || effort == "low" || effort == "medium" || effort == "high"
		case "messages":
			valid = effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh" || effort == "max"
		default:
			valid = effort == "none" || effort == "minimal" || effort == "low" || effort == "medium" || effort == "high" || effort == "xhigh"
		}
		if !valid {
			return "unknown"
		}
		label = effort
	}
	if budget != nil {
		value, err := strconv.ParseInt(strings.TrimSpace(string(budget)), 10, 64)
		if err != nil {
			return "unknown"
		}
		if label != "" {
			label += "|"
		}
		label += "budget:" + strconv.FormatInt(value, 10)
	}
	if label != "" {
		return label
	}
	if configured {
		return "unknown"
	}
	return "default"
}

func controlledMetadataObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	err := json.Unmarshal(raw, &object)
	return object, err == nil && object != nil
}

func controlledMetadataAlias(fields map[string]json.RawMessage, camel, snake string) (json.RawMessage, bool) {
	if raw, present := fields[camel]; present {
		return raw, true
	}
	raw, present := fields[snake]
	return raw, present
}

func (r *ControlledRequest) markSemantic(at time.Time, answer bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.semanticSeen = true
	if r.semanticAt.IsZero() {
		r.semanticAt = at
	}
	if answer && r.answerAt.IsZero() {
		r.answerAt = at
	}
	if r.Ledger != nil && !r.NativeDelivery {
		r.Ledger.MarkSemanticCommit()
	}
}
func (r *ControlledRequest) Close() {
	r.mu.Lock()
	r.closeRequested = true
	if r.closed || r.retainedExecutions > 0 {
		r.mu.Unlock()
		return
	}
	r.closed = true
	fn := r.finish
	executionCancel := r.executionCancel
	r.finish = nil
	r.executionCancel = nil
	ctrl := r.control
	d := r.Decision
	pending := r.decisionPending
	r.decisionPending = false
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
	if executionCancel != nil {
		executionCancel()
	}
	if pending && ctrl != nil {
		parent := r.clientContext
		if parent == nil {
			parent = context.Background()
		}
		cleanup, stop := controlledPreparationContext(parent, r)
		ctrl.releaseDecisionContext(cleanup, d)
		stop()
	}
	r.mu.Lock()
	semantic, attempt := r.semanticAt, r.currentAttemptID
	metrics := map[string]any{}
	if r.NativeDelivery {
		metrics["http_committed"] = r.httpCommitted
		metrics["attempt_committed"] = r.attemptCommitted
		metrics["semantic_seen"] = r.semanticSeen || !semantic.IsZero()
		if r.firstReadToFlushMS != nil {
			metrics["gateway_read_to_flush_ms"] = *r.firstReadToFlushMS
			metrics["gateway_read_to_flush_max_ms"] = r.maxReadToFlushMS
			metrics["gateway_flush_count"] = r.flushCount
			metrics["first_downstream_flush_at"] = r.firstFlushAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if !semantic.IsZero() {
		metrics["overall_first_semantic_ms"] = semantic.Sub(r.Started).Milliseconds()
	}
	r.mu.Unlock()
	if ctrl != nil && attempt != "" && len(metrics) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = ctrl.Store.RecordAttemptMetrics(ctx, attempt, metrics)
	}
}

// Install one ledger before body parsing and queues. WS adapters start a new
// request context for each response.create, rather than sharing a connection one.
func ControlledSchedulingMiddleware(readers ...func(context.Context) (scheduling.ModeSnapshot, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		originalWriter := c.Writer
		defer func() { c.Writer = originalWriter }()
		protocol := "http"
		if strings.Contains(c.Request.URL.Path, "messages") {
			protocol = "messages"
		} else if strings.Contains(c.Request.URL.Path, "chat/completions") {
			protocol = "chat"
		} else if strings.Contains(c.Request.URL.Path, "responses") {
			protocol = "responses"
		} else if strings.Contains(c.Request.URL.Path, "generateContent") || strings.Contains(c.Request.URL.Path, "streamGenerateContent") {
			protocol = "gemini"
		}
		ctx := NewControlledRequestContext(c.Request.Context(), protocol)
		r := controlledRequest(ctx)
		r.Auxiliary = schedulingAuxiliaryRequest(c.Request)
		if len(readers) > 0 && readers[0] != nil {
			readCtx, readDone := context.WithTimeout(ctx, 3*time.Second)
			mode, err := readers[0](readCtx)
			readDone()
			if err != nil || !mode.Mode.Valid() {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "service_unavailable", "message": "Scheduling mode is temporarily unavailable"}})
				return
			}
			r.Mode, r.modeResolved = mode, true
			if mode.Mode == scheduling.ModeSub2API {
				c.Request = c.Request.WithContext(ctx)
				if r.NativeDelivery {
					c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r, localRequestID: c.Writer.Header().Get("X-Request-ID")}
					defer r.Close()
				}
				c.Next()
				return
			}
		}
		c.Header("X-Scheduling-Request-Id", r.ID)
		c.Request = c.Request.WithContext(ctx)
		if c.Request.Body != nil {
			c.Request.Body = &schedulingMetadataBody{ReadCloser: c.Request.Body, request: r}
		}
		c.Writer = &schedulingResponseWriter{ResponseWriter: c.Writer, request: r, localRequestID: c.Writer.Header().Get("X-Request-ID")}
		defer r.Close()
		c.Next()
	}
}

type schedulingResponseWriter struct {
	gin.ResponseWriter
	request  *ControlledRequest
	parser   semanticEventParser
	writeMu  sync.Mutex
	writeErr error
	// RequestLogger sets this local trace before the scheduling middleware. It
	// is not an upstream generation identity and cannot commit a heartbeat.
	localRequestID string
}

func (w *schedulingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *schedulingResponseWriter) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.request.mu.Lock()
	finished := w.request.localFailureStarted
	w.request.mu.Unlock()
	if finished {
		return 0, rejectControlledStreamWrite(scheduling.ErrCommitted)
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if err := w.commitIdentityHeaders(); err != nil {
		w.writeErr = rejectControlledStreamWrite(err)
		return 0, w.writeErr
	}
	if w.request.NativeDelivery && len(p) > 0 && w.Status() < 400 {
		if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") || controlledSSEHasProtocolBytes(p) {
			w.request.mu.Lock()
			d := w.request.currentDispatch
			w.request.mu.Unlock()
			if d != nil {
				if err := d.tryCommitAttempt(); err != nil {
					return 0, rejectControlledStreamWrite(err)
				}
			} else {
				w.request.mu.Lock()
				w.request.commitAttemptLocked()
				w.request.mu.Unlock()
			}
		}
	}
	n, err := w.ResponseWriter.Write(p)
	if w.Written() || n > 0 {
		w.markHTTPCommitted()
	}
	if n > 0 && w.Status() < 400 {
		if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			w.parser.Feed(p[:n], func(semantic, answer, terminal bool) {
				if semantic {
					w.request.markSemantic(time.Now(), answer)
				}
			})
		} else {
			w.request.markSemantic(time.Now(), false)
		}
	}
	return n, err
}
func (w *schedulingResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *schedulingResponseWriter) markHTTPCommitted() {
	w.request.mu.Lock()
	w.request.httpCommitted = true
	w.request.mu.Unlock()
}

func (w *schedulingResponseWriter) commitIdentityHeaders() error {
	if !w.request.NativeDelivery || w.Status() >= 400 {
		return nil
	}
	for _, key := range []string{"X-Request-Id", "Request-Id", "Openai-Request-Id", "Openai-Response-Id", "X-Response-Id"} {
		value := w.Header().Get(key)
		if value == "" || (key == "X-Request-Id" && value == w.localRequestID) {
			continue
		}
		w.request.mu.Lock()
		d := w.request.currentDispatch
		w.request.mu.Unlock()
		if d != nil {
			return d.tryCommitAttempt()
		}
		w.request.mu.Lock()
		defer w.request.mu.Unlock()
		if w.request.cancelReason.excludesProviderHealth() {
			return context.Canceled
		}
		w.request.commitAttemptLocked()
		return nil
	}
	return nil
}

func (w *schedulingResponseWriter) WriteHeader(code int) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.ResponseWriter.WriteHeader(code)
	// Gin stages status until WriteHeaderNow, Write or Flush.
	if w.Written() {
		w.markHTTPCommitted()
	}
}

func (w *schedulingResponseWriter) WriteHeaderNow() {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.writeErr != nil {
		return
	}
	if err := w.commitIdentityHeaders(); err != nil {
		w.writeErr = rejectControlledStreamWrite(err)
		return
	}
	w.ResponseWriter.WriteHeaderNow()
	if w.Written() {
		w.markHTTPCommitted()
	}
}

func (w *schedulingResponseWriter) Flush() {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.writeErr != nil {
		return
	}
	if err := w.commitIdentityHeaders(); err != nil {
		w.writeErr = rejectControlledStreamWrite(err)
		return
	}
	w.ResponseWriter.Flush()
	if w.Written() {
		w.markHTTPCommitted()
	}
}

var _ http.ResponseWriter = (*schedulingResponseWriter)(nil)
