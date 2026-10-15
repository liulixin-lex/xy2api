package resilience

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Protocol is the inbound client protocol of a request.
type Protocol int

// Inbound protocols.
const (
	ProtocolUnknown Protocol = iota
	ProtocolResponses
	ProtocolChat
	ProtocolAnthropic
	ProtocolGemini
)

// String returns a stable protocol name for logs.
func (p Protocol) String() string {
	switch p {
	case ProtocolResponses:
		return "responses"
	case ProtocolChat:
		return "chat"
	case ProtocolAnthropic:
		return "anthropic"
	case ProtocolGemini:
		return "gemini"
	}
	return "unknown"
}

var (
	commentHeartbeat   = []byte(":\n\n")
	anthropicHeartbeat = []byte("event: ping\ndata: {\"type\":\"ping\"}\n\n")
)

// HeartbeatFrame returns the pre-output heartbeat of a protocol, or nil when
// the protocol must not receive heartbeats (Gemini SDKs reject SSE comment
// lines; design §7.3).
func HeartbeatFrame(p Protocol) []byte {
	switch p {
	case ProtocolResponses, ProtocolChat:
		return commentHeartbeat
	case ProtocolAnthropic:
		return anthropicHeartbeat
	}
	return nil
}

// Terminal describes a committed-stream failure (design §7.6).
type Terminal struct {
	Category Category
	// Status is the HTTP status the failure would have had before the
	// stream was committed; it is exposed as error.status.
	Status int
	// Message is the client-facing message without the retry hint.
	Message string
	// RetryAfter feeds "Please try again in Ns." (clamped to 2-30 s).
	RetryAfter time.Duration
	// SequenceNumber is the Responses event sequence number to use.
	SequenceNumber int
	// ResponseID is the Responses id to report; generated when empty.
	ResponseID string
	// Model is reported in the Responses object when known.
	Model string
}

// TerminalRetrySeconds clamps a retry hint to the 2-30 s range used in
// terminal frames and Retry-After headers.
func TerminalRetrySeconds(d time.Duration) int {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 2 {
		secs = 2
	}
	if secs > 30 {
		secs = 30
	}
	return secs
}

// terminalCode maps a category to the client-facing error code. Rate limits
// and busy upstreams use rate_limit_exceeded (Codex waits and retries);
// everything else uses server_error, which Codex retries as well.
func terminalCode(c Category) string {
	switch c {
	case CategoryRateLimit, CategoryRateLimitReset:
		return "rate_limit_exceeded"
	}
	return "server_error"
}

func terminalStatus(t Terminal) int {
	if t.Status >= 400 {
		return t.Status
	}
	switch t.Category {
	case CategoryRateLimit, CategoryRateLimitReset:
		return http.StatusTooManyRequests
	case CategoryFirstOutputTimeout, CategoryStall:
		return http.StatusGatewayTimeout
	}
	return http.StatusServiceUnavailable
}

func terminalMessage(t Terminal) string {
	msg := strings.TrimSpace(t.Message)
	if msg == "" {
		switch t.Category {
		case CategoryStall:
			msg = "Upstream stream stalled"
		case CategoryFirstOutputTimeout:
			msg = "Upstream did not respond in time"
		case CategoryRateLimit, CategoryRateLimitReset:
			msg = "Upstream is busy"
		default:
			msg = "Service temporarily unavailable"
		}
	}
	msg = strings.TrimRight(msg, ". ")
	return fmt.Sprintf("%s. Please try again in %ds.", msg, TerminalRetrySeconds(t.RetryAfter))
}

// TerminalFrame renders the terminal SSE frame of a committed stream. The JSON
// payload is a single line with a stable code and the original HTTP semantic
// status in error.status.
func TerminalFrame(p Protocol, t Terminal) []byte {
	code := terminalCode(t.Category)
	status := terminalStatus(t)
	msg := terminalMessage(t)
	switch p {
	case ProtocolResponses:
		id := t.ResponseID
		if id == "" {
			id = "resp_" + randomHex(16)
		}
		resp := map[string]any{
			"id":         id,
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "failed",
			"output":     []any{},
			"error":      map[string]any{"code": code, "message": msg, "status": status},
		}
		if t.Model != "" {
			resp["model"] = t.Model
		}
		payload, _ := json.Marshal(map[string]any{
			"type":            "response.failed",
			"sequence_number": t.SequenceNumber,
			"response":        resp,
		})
		return frame("response.failed", payload)
	case ProtocolChat:
		errType := "server_error"
		if code == "rate_limit_exceeded" {
			errType = "rate_limit_error"
		}
		payload, _ := json.Marshal(map[string]any{
			"error": map[string]any{"type": errType, "code": code, "message": msg, "status": status},
		})
		out := make([]byte, 0, len(payload)+32)
		out = append(out, "data: "...)
		out = append(out, payload...)
		out = append(out, "\n\ndata: [DONE]\n\n"...)
		return out
	case ProtocolAnthropic:
		errType := "api_error"
		switch t.Category {
		case CategoryRateLimit, CategoryRateLimitReset, CategoryCapacityShed:
			errType = "overloaded_error"
		}
		payload, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": errType, "message": msg, "status": status},
		})
		return frame("error", payload)
	}
	return nil
}

func frame(event string, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+len(event)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, "\ndata: "...)
	out = append(out, payload...)
	out = append(out, "\n\n"...)
	return out
}
