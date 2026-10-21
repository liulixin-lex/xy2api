package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// Inbound protocols of the pre-output keepalive (design §7.2, §7.3).
const (
	PreOutputProtocolResponses = "responses"
	PreOutputProtocolChat      = "chat"
	PreOutputProtocolAnthropic = "anthropic"
)

var (
	preOutputCommentFrame = []byte(":\n\n")
	preOutputPingFrame    = []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")
)

// StartPreOutputKeepalive arms the delayed pre-output heartbeat of a
// streaming request: if nothing reached the client firstDelay from now, the
// SSE headers are committed and a protocol heartbeat follows every interval
// until the first semantic byte or the end of the request (design §7.2).
//
// It shares the compact keepalive machinery and context key, so every
// writer that already handles a committed compact keepalive (failover size
// checks, terminal frames) handles it too. It is a no-op when a keepalive is
// already present; the returned stop function is idempotent.
func StartPreOutputKeepalive(c *gin.Context, protocol string, firstDelay, interval time.Duration) func() {
	if c == nil || interval <= 0 {
		return func() {}
	}
	if _, exists := c.Get(openAICompactSSEKeepaliveKey); exists {
		return func() {}
	}
	frame := preOutputCommentFrame
	if protocol == PreOutputProtocolAnthropic {
		frame = preOutputPingFrame
	}
	stop := startSSEKeepalive(c, firstDelay, interval, frame, protocol)

	return stop
}

// noteCommitted counts a pre-output heartbeat that committed the response
// (called with k.mu held).
func (k *openAICompactSSEKeepalive) noteCommitted() {
	if k.protocol == "" {
		return
	}
	resilience.Count(resilience.MetricKeepaliveCommitted, k.metricGroup, 0, k.protocol)
	slog.Info("resilience.keepalive_committed", append(resilience.PreOutputEventFields(k.ctx), "group_id", k.metricGroup, "protocol", k.protocol, "edge", k.edge)...)
}

// PreOutputKeepaliveCommitted reports, without stopping it, whether the
// request's keepalive has committed the response headers.
func PreOutputKeepaliveCommitted(c *gin.Context) bool {
	k := keepaliveFrom(c)
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.started
}

// StopPreOutputKeepaliveIfUncommitted stops a heartbeat that has not
// committed the response yet (an official OAuth account was selected after a
// switch, design §7.2); a committed one keeps running.
func StopPreOutputKeepaliveIfUncommitted(c *gin.Context) {
	k := keepaliveFrom(c)
	if k == nil {
		return
	}
	k.mu.Lock()
	if !k.started {
		k.markStoppedLocked()
	}
	k.mu.Unlock()
}

func keepaliveFrom(c *gin.Context) *openAICompactSSEKeepalive {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	if !ok {
		return nil
	}
	k, _ := value.(*openAICompactSSEKeepalive)
	return k
}

// noteCommittedErrorStatus records an error status written after the
// pre-output heartbeat committed 200; the status line is already on the
// wire, so the call is swallowed and the body becomes a terminal frame.
func (k *openAICompactSSEKeepalive) noteCommittedErrorStatus(code int) bool {
	if k == nil || code < http.StatusBadRequest {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.protocol == "" || !k.started {
		return false
	}
	k.errStatus = code
	return true
}

// replaceCommittedErrorBody writes the protocol terminal frame instead of an
// error body that followed a swallowed error status (design §7.6). Later
// writes of the same body are dropped so the stream ends cleanly.
func (k *openAICompactSSEKeepalive) replaceCommittedErrorBody(w gin.ResponseWriter, data []byte) (int, bool) {
	if k == nil {
		return 0, false
	}
	k.mu.Lock()
	if k.protocol == "" || k.errStatus == 0 {
		k.mu.Unlock()
		return 0, false
	}
	if k.errReplaced {
		k.mu.Unlock()
		return len(data), true
	}
	k.errReplaced = true
	ctx, protocol, status := k.ctx, k.protocol, k.errStatus
	k.mu.Unlock()
	// The writer stopped the heartbeat before entering here. Resolve outside
	// k.mu so an advisor may safely inspect a request delivery snapshot.
	if _, err := w.Write(preOutputTerminalFrame(ctx, protocol, status, data)); err == nil {
		w.Flush()
	}
	return len(data), true
}

// preOutputTerminalFrame converts an error response body into the terminal
// frame of the inbound protocol. It replaces an error the gateway wrote
// while the client had seen heartbeats only, so a Responses frame opens the
// response with response.created first.
func preOutputTerminalFrame(ctx context.Context, protocol string, status int, body []byte) []byte {
	errType, code, message := committedErrorFields(status, body)
	switch protocol {
	case PreOutputProtocolAnthropic:
		payload, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": errType, "message": message},
		})
		return []byte("event: error\ndata: " + string(payload) + "\n\n")
	case PreOutputProtocolChat:
		errObj := map[string]any{"type": errType, "message": message, "status": status}
		if code != "" {
			errObj["code"] = code
		}
		payload, _ := json.Marshal(map[string]any{"error": errObj})
		return []byte("data: " + string(payload) + "\n\ndata: [DONE]\n\n")
	default:
		if code == "" {
			code = "server_error"
			if status == http.StatusTooManyRequests {
				code = "rate_limit_exceeded"
			}
		}
		// The client has seen heartbeats only: open the response first.
		id, createdAt := "resp_"+randomKeepaliveHex(12), time.Now().Unix()
		payload, _ := json.Marshal(map[string]any{
			"type":            "response.failed",
			"sequence_number": 1,
			"response": map[string]any{
				"id":         id,
				"object":     "response",
				"created_at": createdAt,
				"status":     "failed",
				"output":     []any{},
				"error":      openAIResponseFailureError(ctx, errType, code, message),
			},
		})
		return append(ResponsesCreatedFrame(id, "", createdAt), "event: response.failed\ndata: "+string(payload)+"\n\n"...)
	}
}

// ResponsesCreatedFrame is the response.created event (sequence number 0)
// that opens a Responses stream the gateway ends itself before any event
// reached the client. Strict SDKs (openai-python, openai-node) reject a
// stream whose first event is not response.created and report that
// instead of the gateway's error.
func ResponsesCreatedFrame(id, model string, createdAt int64) []byte {
	response := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": createdAt,
		"status":     "in_progress",
		"output":     []any{},
	}
	if model != "" {
		response["model"] = model
	}
	payload, _ := json.Marshal(map[string]any{"type": "response.created", "sequence_number": 0, "response": response})
	return []byte("event: response.created\ndata: " + string(payload) + "\n\n")
}

// committedErrorFields extracts type, code and message from an OpenAI- or
// Anthropic-style error body, falling back to the status text.
func committedErrorFields(status int, body []byte) (string, string, string) {
	errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	code := strings.TrimSpace(gjson.GetBytes(body, "error.code").String())
	message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if errType == "" {
		switch {
		case status == http.StatusTooManyRequests:
			errType = "rate_limit_error"
		case (status == 529 || status == http.StatusServiceUnavailable) && !resilience.Current().ErrorsOn():
			errType = "overloaded_error"
		case status >= http.StatusInternalServerError:
			errType = "api_error"
		default:
			errType = "invalid_request_error"
		}
	}
	return errType, code, message
}

func randomKeepaliveHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(b)
}

// openAIResponseFailureError is shared by the three local failed-frame writers.
// It never forwards upstream headers or computes a candidate recommendation.
func openAIResponseFailureError(ctx context.Context, errType, code, message string) map[string]any {
	errBody := map[string]any{"code": code, "message": message}
	if resilience.Current().FailedFrameRetryAfterOn() {
		if resilience.ClassifyWithSettings(resilience.Outcome{Source: resilience.SourceStreamEvent, Type: errType, Code: code, Message: message}, resilience.Current()).ContentPolicy() {
			resilience.SetRetryAdvice(ctx, resilience.RetryAdvice{Suppress: true, Source: "content_rejection"})
		}
		advice := resilience.ResolveRetryAdvice(ctx)
		errBody["message"] = advice.Message(message)
		if headers := advice.Headers(); len(headers) > 0 {
			errBody["headers"] = headers
		}
	}
	return errBody
}
