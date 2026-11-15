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
	EventType, Status, Reason, Message string
	WrappedStatus                      int
}

func ClassifyOpenAIResponsesTerminal(eventName string, data []byte) OpenAIResponsesTerminal {
	eventName = strings.TrimSpace(eventName)
	result := OpenAIResponsesTerminal{}
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
	result.Seen = candidate(eventName)
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
	if p != "" && e != "" && p != e && (candidate(p) || candidate(e)) {
		return invalid("type_status_conflict")
	}
	kind := p
	if kind == "" {
		kind = e
	}
	result.EventType = kind
	// An untyped structured error is failure evidence, never a trusted HTTP code.
	if (kind == "" || kind == "error") && root.Get("error").IsObject() {
		errObj := root.Get("error")
		meaningful := false
		for _, key := range []string{"message", "type", "code"} {
			v := errObj.Get(key)
			if v.Type == gjson.String && strings.TrimSpace(v.Str) != "" || key == "code" && v.Type == gjson.Number {
				meaningful = true
			}
		}
		if meaningful {
			return OpenAIResponsesTerminal{Seen: true, Valid: true, Failure: true, EventType: "error", Reason: "upstream_failed", Message: errObj.Get("message").String()}
		}
	}
	if kind == "error" {
		result.Seen = false
		return result
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
