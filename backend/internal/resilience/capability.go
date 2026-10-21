package resilience

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	CapabilityProtocolResponses = "responses"
	CapabilityProtocolChat      = "chat"
	CapabilityModeStreamOnly    = "stream_only"
	ReasonCapabilityStreamOnly  = "capability_stream_only"
)

// LearnedCapability is an administrator-visible, expiring observation. Manual
// capability settings are separate and are not implemented by this feature.
type LearnedCapability struct {
	Protocol  string    `json:"protocol"`
	Mode      string    `json:"mode"`
	ExpiresAt time.Time `json:"expires_at"`
}

type capabilityRequestKey struct{}
type capabilityRequest struct {
	protocol          string
	stream            bool
	excluded          sync.Map
	capabilityAttempt atomic.Bool
	otherAttempt      atomic.Bool
}

// WithCapabilityRequest records an explicitly parsed client mode. Callers
// omit it for images, compact, WebSocket and unsupported protocols.
func WithCapabilityRequest(ctx context.Context, protocol string, stream bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, capabilityRequestKey{}, &capabilityRequest{protocol: protocol, stream: stream})
}

func CapabilityRequestFrom(ctx context.Context) (protocol string, stream bool, ok bool) {
	if ctx == nil {
		return "", false, false
	}
	request, exists := ctx.Value(capabilityRequestKey{}).(*capabilityRequest)
	if !exists || request == nil {
		return "", false, false
	}
	return request.protocol, request.stream, exists && validCapabilityProtocol(request.protocol)
}

// NoteCapabilityExcluded tracks a neutral request-local exclusion, separate
// from account failures, so final selection errors and retry advice retain it.
func NoteCapabilityExcluded(ctx context.Context, accountID int64) {
	if ctx == nil || accountID <= 0 {
		return
	}
	if request, ok := ctx.Value(capabilityRequestKey{}).(*capabilityRequest); ok && request != nil {
		request.excluded.Store(accountID, struct{}{})
	}
}

// CapabilityExcluded checks one account, or any exclusion when accountID=0.
func CapabilityExcluded(ctx context.Context, accountID int64) bool {
	if ctx == nil {
		return false
	}
	request, ok := ctx.Value(capabilityRequestKey{}).(*capabilityRequest)
	if !ok || request == nil {
		return false
	}
	if accountID > 0 {
		_, found := request.excluded.Load(accountID)
		return found
	}
	found := false
	request.excluded.Range(func(_, _ any) bool { found = true; return false })
	return found
}

// NoteCapabilityAttempt records request-local causes without marking account
// or session health. A mixed failure must keep its ordinary retry guidance.
func NoteCapabilityAttempt(ctx context.Context, capability bool) {
	if _, _, ok := CapabilityRequestFrom(ctx); !ok {
		return
	}
	request := ctx.Value(capabilityRequestKey{}).(*capabilityRequest)
	if capability {
		request.capabilityAttempt.Store(true)
	} else {
		request.otherAttempt.Store(true)
	}
}

func CapabilityOnlyAttempts(ctx context.Context) bool {
	if _, _, ok := CapabilityRequestFrom(ctx); !ok {
		return false
	}
	request := ctx.Value(capabilityRequestKey{}).(*capabilityRequest)
	return request.capabilityAttempt.Load() && !request.otherAttempt.Load()
}

func validCapabilityProtocol(protocol string) bool {
	return protocol == CapabilityProtocolResponses || protocol == CapabilityProtocolChat
}

// IsStreamOnlyFailure recognizes only explicit error fields, never echoed
// request content. Malformed/truncated JSON cannot become a plain-text match.
func IsStreamOnlyFailure(status int, body []byte, outboundBodies ...[]byte) bool {
	if status != http.StatusBadRequest || len(body) == 0 || len(body) > inspectLimit {
		return false
	}
	type errorFields struct {
		Code    string          `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		Param   json.RawMessage `json:"param"`
	}
	var envelope struct {
		errorFields
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error errorFields `json:"error"`
		} `json:"response"`
	}
	trimmed := strings.TrimSpace(string(body))
	var fields errorFields
	if json.Valid(body) {
		if json.Unmarshal(body, &envelope) != nil {
			return false
		}
		fields = envelope.errorFields
		if len(envelope.Error) > 0 {
			var nested errorFields
			if json.Unmarshal(envelope.Error, &nested) == nil {
				fields = nested
			} else {
				var message string
				if json.Unmarshal(envelope.Error, &message) == nil {
					fields.Message = message
				}
			}
		}
		if fields.Code == "" && fields.Type == "" && fields.Message == "" {
			fields = envelope.Response.Error
		}
	} else {
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			return false
		}
		fields.Message = trimmed
	}
	if len(fields.Param) > 0 {
		var param string
		if json.Unmarshal(fields.Param, &param) != nil || param != "stream" {
			return false
		}
	}
	for _, outbound := range outboundBodies {
		if containsStreamOnlyRequestPhrase(outbound) {
			return false
		}
	}
	result := Classify(Outcome{Source: SourceHTTP, StatusCode: status, Code: fields.Code, Type: fields.Type, Message: fields.Message}, nil)
	if result.Hard() || result.ContentPolicy() {
		return false
	}
	for _, code := range []string{fields.Code, fields.Type} {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "streaming_required", "stream_required", "stream_only", "streaming_only", "non_streaming_not_supported":
			return true
		}
	}
	return containsStreamOnlyPhrase(fields.Message)
}

func containsStreamOnlyPhrase(text string) bool {
	message := strings.ToLower(strings.Join(strings.Fields(text), " "))
	for _, phrase := range []string{
		"only accepts streaming requests", "only supports streaming requests", "only streaming requests are supported",
		"stream must be true", `"stream" must be true`, "'stream' must be true", "stream=true is required",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// Error messages can echo user-supplied parameter values. Inspect decoded
// strings too, so JSON escaping cannot hide the triggering phrase.
func containsStreamOnlyRequestPhrase(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if containsStreamOnlyPhrase(string(body)) {
		return true
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch value := value.(type) {
		case string:
			return containsStreamOnlyPhrase(value)
		case []any:
			for _, child := range value {
				if contains(child) {
					return true
				}
			}
		case map[string]any:
			for key, child := range value {
				if containsStreamOnlyPhrase(key) || contains(child) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}
