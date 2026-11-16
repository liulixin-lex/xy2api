package service

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const OpenAIUpstreamProtocolErrorCode = "upstream_protocol_error"

type OpenAIResponsesTerminal struct {
	Seen, Valid, Success, Failure      bool
	ErrorPresent                       bool
	EventType, Status, Reason, Message string
	WrappedStatus                      int
}

// BenignIncomplete separates expected output limits from interrupted generation.
func (t OpenAIResponsesTerminal) BenignIncomplete() bool {
	return t.Valid && t.Status == "incomplete" && (t.Reason == "max_output_tokens" || t.Reason == "content_filter")
}

// QualityFailure is independent of Failure, which preserves the forwarding
// protocol's decision to deliver incomplete responses without retrying them.
func (t OpenAIResponsesTerminal) QualityFailure() bool {
	return t.Seen && t.Failure || t.ErrorPresent || t.Valid && t.Status == "incomplete" && !t.BenignIncomplete()
}

func openAIResponsesMeaningfulError(errObj gjson.Result) bool {
	if !errObj.IsObject() {
		return false
	}
	for _, key := range []string{"message", "type", "code"} {
		v := errObj.Get(key)
		if v.Type == gjson.String && strings.TrimSpace(v.Str) != "" || key == "code" && v.Type == gjson.Number {
			return true
		}
	}
	return false
}

func ClassifyOpenAIResponsesTerminal(eventName string, data []byte) (result OpenAIResponsesTerminal) {
	eventName = strings.TrimSpace(eventName)
	defer func() {
		if gjson.ValidBytes(data) {
			result.ErrorPresent = openAIResponsesMeaningfulError(gjson.GetBytes(data, "error"))
		}
	}()
	candidate := func(kind string) bool {
		switch kind {
		case "response.completed", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.done", "error", "proxy.upstream_error":
			return true
		}
		return false
	}
	invalid := func(reason string) OpenAIResponsesTerminal {
		result.Failure = true
		result.Valid = false
		result.Success = false
		result.Reason = reason
		return result
	}
	// A partial payload type identifies a candidate only; it never authenticates success.
	result.Seen = candidate(eventName) || candidate(gjson.GetBytes(data, "type").String()) || candidate("response."+gjson.GetBytes(data, "status").String())
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return OpenAIResponsesTerminal{Reason: "missing_response_terminal"}
	}
	if !utf8.Valid(data) {
		return invalid("invalid_utf8")
	}
	if !gjson.ValidBytes(data) {
		return invalid("malformed_json")
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return invalid("invalid_envelope")
	}
	rawType := root.Get("type")
	if rawType.Exists() && rawType.Type != gjson.String {
		return invalid("invalid_envelope")
	}
	payloadType := ""
	if rawType.Type == gjson.String {
		payloadType = strings.TrimSpace(rawType.Str)
	}
	result.Seen = result.Seen || candidate(payloadType)
	result.EventType = payloadType
	if result.EventType == "" {
		result.EventType = eventName
	}
	wrapped, ok, malformed := openAIWrappedUpstreamErrorStatus(eventName, data)
	if malformed {
		result.Seen = true
		return invalid("malformed_wrapped_error")
	}
	if ok && payloadType == "proxy.upstream_error" {
		return OpenAIResponsesTerminal{Seen: true, Valid: true, Failure: true, EventType: "error", Reason: "upstream_failed", Message: root.Get("message").String(), WrappedStatus: wrapped}
	}
	response := root.Get("response")
	// A non-streaming response carries the same terminal facts at the root.
	// Keep structured error handling below identical for JSON and SSE.
	jsonResponse := false
	if !candidate(payloadType) && !response.IsObject() {
		switch root.Get("status").String() {
		case "completed", "incomplete", "failed", "cancelled", "canceled":
			response, jsonResponse = root, true
		}
	}
	rawStatus := response.Get("status")
	status := ""
	if rawStatus.Type == gjson.String {
		status = rawStatus.Str
	}
	if status == "canceled" {
		status = "cancelled"
	}
	normalize := func(kind string) string {
		if kind == "response.canceled" {
			return "response.cancelled"
		}
		if kind == "response.done" && (status == "completed" || status == "incomplete" || status == "failed" || status == "cancelled") {
			return "response." + status
		}
		return kind
	}
	p, e := normalize(payloadType), normalize(eventName)
	// Generic SSE names do not override an explicit payload type. Keep strict
	// wrapper validation above and reject disagreements between response ends.
	if p != "" && e != "" && p != e && e != "error" && candidate(p) && candidate(e) {
		return invalid("type_status_conflict")
	}
	kind := p
	if kind == "" {
		kind = e
	}
	result.EventType = kind
	// Preserve the forwarding decision for typed Responses envelopes.
	if kind == "" || kind == "error" {
		errObj := root.Get("error")
		if openAIResponsesMeaningfulError(errObj) {
			return OpenAIResponsesTerminal{Seen: true, Valid: true, Failure: true, EventType: "error", Reason: "upstream_failed", Message: errObj.Get("message").String()}
		}
	}
	if kind == "error" {
		for _, key := range []string{"message", "code"} {
			v := root.Get(key)
			if v.Type == gjson.String && strings.TrimSpace(v.Str) != "" || key == "code" && v.Type == gjson.Number {
				return OpenAIResponsesTerminal{Seen: true, Valid: true, Failure: true, EventType: "error", Reason: "upstream_failed", Message: root.Get("message").String()}
			}
		}
		result.Seen = false
		return result
	}
	if jsonResponse {
		kind = "response." + status
		result.EventType = kind
	}
	if !candidate(kind) {
		return result
	}
	if !response.IsObject() {
		return invalid("missing_response")
	}
	if kind == "response.done" {
		if status == "" {
			return invalid("missing_status")
		}
		return invalid("unknown_status")
	}
	expected := strings.TrimPrefix(kind, "response.")
	if rawStatus.Exists() && (rawStatus.Type != gjson.String || status != expected) {
		return invalid("type_status_conflict")
	}
	if status == "" {
		status = expected
	}
	result.Seen, result.Valid, result.Status = true, true, status
	result.WrappedStatus = wrapped
	result.Message = response.Get("error.message").String()
	switch status {
	case "completed":
		result.Success = true
	case "failed":
		result.Failure = true
		result.Reason = "upstream_failed"
	case "cancelled":
		result.Failure = true
		result.Reason = "upstream_cancelled"
	case "incomplete":
		result.Reason = response.Get("incomplete_details.reason").String()
	default:
		return invalid("unknown_status")
	}
	return result
}
