package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/domain/schedquality"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// classifySchedAttempt keeps quality attribution separate from resilience's
// immediate retry/credential policy. An unexplained 4xx is not proof of fault.
func classifySchedAttempt(in SchedAttemptFinish, failure *resilience.Outcome, output bool, allowModel401 bool) SchedAttemptFinish {
	if in.Outcome == schedquality.OutcomeOK {
		in.Class = schedquality.ClassNone
		return in
	}
	if in.Class != schedquality.ClassNone {
		return in
	}
	in.Class = schedquality.ClassUnknown
	if failure == nil {
		if output || in.Detail == SchedAttemptDetailOutputIncomplete {
			in.Class = schedquality.ClassInterrupted
		}
		return in
	}
	o := *failure
	if o.StatusCode > 0 && (o.Source == resilience.SourceHTTP || o.Source == resilience.SourceStreamEvent) {
		in.HTTPStatus, in.StatusOrigin = o.StatusCode, schedquality.OriginUpstream
	}
	code, message := o.Code, o.Message
	if code == "" {
		code = openAIStreamErrorField(o.Body, "code").String()
	}
	if message == "" {
		message = schedErrorMessage(o.Body)
	}
	isEvent := o.Source == resilience.SourceStreamEvent
	if isEvent {
		// HTTPStatus above remains the actual response head (usually 200).
		// An explicit event status controls attribution only.
		o.StatusCode = UpstreamErrorEventStatus(o.Body)
	}
	capability := classifyUpstreamCapabilityFailure(o.StatusCode, o.Body, allowModel401)
	if isEvent {
		capability = ClassifyUpstreamCapabilityEventFailure(o.Body)
	}
	if o.Code == "" {
		o.Code = code
	}
	if o.Message == "" {
		o.Message = message
	}
	if o.Type == "" {
		o.Type = openAIStreamErrorField(o.Body, "type").String()
	}
	result := classifyUpstreamOutcome(o)
	switch {
	case output || in.Detail == SchedAttemptDetailOutputIncomplete:
		in.Class = schedquality.ClassInterrupted
	case resilience.ClassifyStateRejection(code, message).RequestScoped() || schedMigrationMessage(message):
		in.Class = schedquality.ClassMigration400
	case capability != UpstreamCapabilityNone:
		in.Class = schedquality.ClassCapability
	case result.Category == resilience.CategoryBalance && strings.Contains(strings.ToLower(message), "upstream image account"):
		in.Class = schedquality.ClassUpstream
	case !isEvent && o.StatusCode == http.StatusUnauthorized && !allowModel401:
		in.Class = schedquality.ClassAccountAccess
	case result.Category == resilience.CategoryAuth && (!result.StatusOnly() || o.StatusCode == http.StatusUnauthorized) || result.Category == resilience.CategoryBalance:
		in.Class = schedquality.ClassAccountAccess
	case schedAccountCapacity(code, message):
		in.Class = schedquality.ClassAccountCapacity
	case result.Category == resilience.CategoryUpstreamRequestRestriction || o.StatusCode < 500 && (schedRequestMessage(message) || resilience.IsContextWindowError(message, o.Body) || result.Category == resilience.CategoryClientError && !result.StatusOnly()):
		in.Class = schedquality.ClassRequest
	case result.Category == resilience.CategoryCapacityShed:
		in.Class = schedquality.ClassUpstream
	case (o.Source == resilience.SourceHTTP || isEvent) && o.StatusCode >= 500:
		in.Class = schedquality.ClassUpstream
	case o.Source == resilience.SourceNetwork && o.Err != nil && !errors.Is(o.Err, context.Canceled) && !errors.Is(o.Err, context.DeadlineExceeded):
		in.Class = schedquality.ClassUpstream
	}
	return in
}

func schedMigrationMessage(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "function_call_output") && strings.Contains(m, "matching the") && strings.Contains(m, "tool call") {
		return true
	}
	for _, phrase := range []string{"no tool call found for function call output", "tool_use ids were found without tool_result", "tool_result blocks must have a corresponding tool_use", "previous_response_id is not supported", "previous_response_id is only supported on responses websocket", "previous response not found"} {
		if strings.Contains(m, phrase) {
			return true
		}
	}
	return false
}

func schedAccountCapacity(code, message string) bool {
	switch strings.ToLower(code) {
	case "account_limit_exceeded", "account_rate_limit_exceeded", "concurrency_limit_exceeded", "account_pool_exhausted", "credentials_exhausted", "model_cooldown":
		return true
	}
	m := strings.ToLower(strings.Join(strings.Fields(message), " "))
	if schedAccountLimitTemplate.MatchString(m) || schedPoolTemplate.MatchString(m) {
		return true
	}
	for _, phrase := range []string{"account limit reached", "account rate limit", "concurrency limit exceeded", "concurrency limit reached", "no available credentials", "no available accounts"} {
		if strings.Contains(m, phrase) {
			return true
		}
	}
	if strings.Contains(m, "all credentials") || strings.Contains(m, "all accounts") {
		for _, exhausted := range []string{"exhausted", "rate limited", "rate-limited", "unavailable", "cooldown", "cooling down"} {
			if strings.Contains(m, exhausted) {
				return true
			}
		}
	}
	return false
}

// UpstreamCapabilityScope describes what the refusal rules out. Request
// features (including ambiguous wording) never justify a model/protocol mark.
type UpstreamCapabilityScope uint8

const (
	UpstreamCapabilityNone UpstreamCapabilityScope = iota
	UpstreamCapabilityModel
	UpstreamCapabilityProtocol
	UpstreamCapabilityRequestFeature
)

func (s UpstreamCapabilityScope) Markable() bool {
	return s == UpstreamCapabilityModel || s == UpstreamCapabilityProtocol
}

// ClassifyUpstreamCapabilityFailure reads a real HTTP refusal. The 401 model
// exception requires the same account qualification as the rate-limit layer.
func ClassifyUpstreamCapabilityFailure(account *Account, status int, body []byte) UpstreamCapabilityScope {
	return classifyUpstreamCapabilityFailure(status, body, account != nil && account.Type == AccountTypeAPIKey && account.IsOpenAICompatible())
}

func classifyUpstreamCapabilityFailure(status int, body []byte, allowModel401 bool) UpstreamCapabilityScope {
	if status < 400 || status > 599 || len(body) > 1<<20 {
		return UpstreamCapabilityNone
	}
	if status == http.StatusUnauthorized && !allowModel401 {
		return UpstreamCapabilityNone
	}
	if !gjson.ValidBytes(body) {
		if resilience.IsStreamOnlyFailure(status, body) {
			return UpstreamCapabilityProtocol
		}
		return UpstreamCapabilityNone
	}
	fields := schedErrorFields(body)
	modelBody := body
	if fields.IsObject() {
		// The flat event's type names the envelope, not its error category.
		if fields.Get("type").String() == "error" {
			encoded, _ := json.Marshal(map[string]any{"error": map[string]any{
				"code": openAIStreamErrorField(body, "code").Value(), "message": schedErrorMessage(body), "param": openAIStreamErrorField(body, "param").Value(),
			}})
			modelBody = encoded
		}
	} else if message := schedErrorMessage(body); message != "" {
		encoded, _ := json.Marshal(message)
		modelBody = []byte(`{"error":{"message":` + string(encoded) + `}}`)
	}
	message := strings.ToLower(strings.Join(strings.Fields(schedErrorMessage(body)), " "))
	// These templates describe dimensions absent from capability routing keys.
	if status == 400 || status == 404 {
		if schedProtocolTemplate.MatchString(message) || strings.Contains(message, "unsupported frontend") || strings.Contains(message, "unsupported input capability") {
			return UpstreamCapabilityRequestFeature
		}
	}
	for _, key := range []string{"code", "type"} {
		switch strings.ToLower(strings.TrimSpace(openAIStreamErrorField(body, key).String())) {
		case "unsupported_frontend", "unsupported_input_capability", "unsupported_request_type":
			return UpstreamCapabilityRequestFeature
		case "request_type_not_supported":
			if status == 400 || status == 404 {
				return UpstreamCapabilityRequestFeature
			}
		case "model_unsupported":
			return UpstreamCapabilityModel
		case "unsupported_protocol":
			return UpstreamCapabilityProtocol
		case "protocol_not_supported":
			if status == 400 || status == 404 {
				return UpstreamCapabilityProtocol
			}
		case "model_not_found", "model_not_supported", "unsupported_model":
			if status == 400 || status == 404 {
				return UpstreamCapabilityModel
			}
		}
	}
	if resilience.IsStreamOnlyFailure(status, modelBody) {
		return UpstreamCapabilityProtocol
	}
	if resilience.IsModelUnsupportedResponse(status, modelBody) || status == http.StatusUnauthorized && allowModel401 && resilience.IsOpenAICompatibleModelNotFound401(modelBody) {
		return UpstreamCapabilityModel
	}
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return UpstreamCapabilityNone
	}
	if schedUnsupportedModelMessage.MatchString(message) {
		return UpstreamCapabilityModel
	}
	for _, phrase := range []string{
		"this site does not support this request", "this upstream does not support this request",
		"this endpoint does not support this request", "站点不支持这种请求", "上游不支持这种请求",
	} {
		if strings.Contains(message, phrase) {
			return UpstreamCapabilityRequestFeature
		}
	}
	return UpstreamCapabilityNone
}

// IsUpstreamCapabilityFailure is a conservative boolean view for callers
// without account qualification. Learning must consume the scoped API instead.
func IsUpstreamCapabilityFailure(status int, body []byte) bool {
	return ClassifyUpstreamCapabilityFailure(nil, status, body) != UpstreamCapabilityNone
}

// Envelope fields win; root fields are eligible only for an explicit error event.
func schedErrorFields(body []byte) gjson.Result {
	if object := openAIStreamErrorObject(body); object.IsObject() {
		return object
	}
	return openAITopLevelStreamError(body)
}

func schedRequestMessage(message string) bool {
	m := strings.ToLower(message)
	for _, phrase := range []string{"invalid parameter", "invalid request parameter", "invalid type", "request too large", "request body too large", "request entity too large", "payload too large"} {
		if strings.Contains(m, phrase) {
			return true
		}
	}
	return false
}

// Fixed template words identify a refusal; provider/model placeholders are
// deliberately opaque and never interpreted as status or account evidence.
var schedUnsupportedModelMessage = regexp.MustCompile(`^model .+ is (?:not supported by any .+ account in this .+|not available for this .+)[.!]?$`)

func schedErrorMessage(body []byte) string {
	if message := schedErrorFields(body).Get("message"); message.Type == gjson.String {
		return message.String()
	}
	root := gjson.ParseBytes(body)
	for _, path := range []string{"error", "response.error", "detail"} {
		if message := root.Get(path); message.Type == gjson.String {
			return message.String()
		}
	}
	return ""
}

var schedAccountLimitTemplate = regexp.MustCompile(`\baccount(?: \S+)? limit reached\b`)
var schedPoolTemplate = regexp.MustCompile(`(?:^all available accounts are .+ please retry\b|^all credentials for model \S+ are .+)`)
var schedProtocolTemplate = regexp.MustCompile(`(?:\bfrontend requests .+ are not supported on this .+|\bsupports text and https input_image content only\b)`)

// UpstreamErrorEventStatus is shared by quality attribution and capability
// learning. Only explicit integer 400..599 values in supported error envelopes
// count; an event without one uses the established 400 fallback. This semantic
// status never replaces the real HTTP response head in a captured sample.
func UpstreamErrorEventStatus(payload []byte) int {
	if status, ok := openAIModelAvailabilityExplicitStatus(payload); ok {
		return status
	}
	return http.StatusBadRequest
}

// IsUpstreamCapabilityEventFailure excludes the HTTP 401 compatibility
// exception: an in-band event has not established that account qualification.
func IsUpstreamCapabilityEventFailure(payload []byte) bool {
	return ClassifyUpstreamCapabilityEventFailure(payload) != UpstreamCapabilityNone
}

// ClassifyUpstreamCapabilityEventFailure shares scope and event-status evidence
// with quality attribution; the HTTP account-qualified 401 exception is absent.
func ClassifyUpstreamCapabilityEventFailure(payload []byte) UpstreamCapabilityScope {
	return classifyUpstreamCapabilityFailure(UpstreamErrorEventStatus(payload), payload, false)
}
