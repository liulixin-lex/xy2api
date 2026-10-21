package resilience

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
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
	protocol string
	stream   bool
	excluded sync.Map
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

func validCapabilityProtocol(protocol string) bool {
	return protocol == CapabilityProtocolResponses || protocol == CapabilityProtocolChat
}

// IsStreamOnlyFailure recognizes only explicit error fields, never echoed
// request content. Malformed/truncated JSON cannot become a plain-text match.
func IsStreamOnlyFailure(status int, body []byte) bool {
	if status != http.StatusBadRequest || len(body) == 0 || len(body) > inspectLimit {
		return false
	}
	type errorFields struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
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
	message := strings.ToLower(strings.Join(strings.Fields(fields.Message), " "))
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
