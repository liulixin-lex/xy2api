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
func classifySchedAttempt(in SchedAttemptFinish, failure *resilience.Outcome, output bool) SchedAttemptFinish {
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
	fields := schedErrorFields(o.Body)
	code, message := o.Code, o.Message
	if code == "" {
		code = fields.Get("code").String()
	}
	if message == "" {
		message = schedErrorMessage(o.Body)
	}
	capabilityStatus := o.StatusCode
	if o.Source == resilience.SourceStreamEvent {
		capabilityStatus = http.StatusBadRequest
	}
	result := classifyUpstreamOutcome(o)
	switch {
	case output || in.Detail == SchedAttemptDetailOutputIncomplete:
		in.Class = schedquality.ClassInterrupted
	case resilience.ClassifyStateRejection(code, message).RequestScoped() || schedMigrationMessage(message):
		in.Class = schedquality.ClassMigration400
	case IsUpstreamCapabilityFailure(capabilityStatus, o.Body):
		in.Class = schedquality.ClassCapability
	case result.Category == resilience.CategoryBalance && strings.Contains(strings.ToLower(message), "upstream image account"):
		in.Class = schedquality.ClassUpstream
	case result.Category == resilience.CategoryAuth && (!result.StatusOnly() || o.StatusCode == http.StatusUnauthorized) || result.Category == resilience.CategoryBalance:
		in.Class = schedquality.ClassAccountAccess
	case schedAccountCapacity(code, message):
		in.Class = schedquality.ClassAccountCapacity
	case result.Category == resilience.CategoryUpstreamRequestRestriction || o.StatusCode < 500 && (schedRequestMessage(message) || resilience.IsContextWindowError(message, o.Body) || result.Category == resilience.CategoryClientError && !result.StatusOnly()):
		in.Class = schedquality.ClassRequest
	case result.Category == resilience.CategoryCapacityShed:
		in.Class = schedquality.ClassUpstream
	case o.Source == resilience.SourceHTTP && o.StatusCode >= 500:
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

// IsUpstreamCapabilityFailure recognizes an explicit upstream model/protocol
// rejection. Callers must supply the upstream response, not a gateway status or
// request body. Ordinary parameter errors and ambiguous 404s are not evidence.
func IsUpstreamCapabilityFailure(status int, body []byte) bool {
	if status < 400 || status > 599 || len(body) > 1<<20 {
		return false
	}
	if resilience.IsStreamOnlyFailure(status, body) {
		return true
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	modelBody := body
	if !schedErrorFields(body).IsObject() {
		if message := schedErrorMessage(body); message != "" {
			// Apply the existing narrow model predicate to string envelopes too.
			encoded, _ := json.Marshal(message)
			modelBody = []byte(`{"error":{"message":` + string(encoded) + `}}`)
		}
	}
	if resilience.IsModelUnsupportedResponse(status, modelBody) || status == http.StatusUnauthorized && resilience.IsOpenAICompatibleModelNotFound401(modelBody) {
		return true
	}
	fields := schedErrorFields(body)
	for _, key := range []string{"code", "type"} {
		switch strings.ToLower(strings.TrimSpace(fields.Get(key).String())) {
		case "model_not_found", "model_not_supported", "unsupported_model", "protocol_not_supported", "request_type_not_supported":
			if status == http.StatusBadRequest || status == http.StatusNotFound {
				return true
			}
		case "model_unsupported", "unsupported_protocol", "unsupported_request_type", "unsupported_frontend", "unsupported_input_capability":
			return true
		}
	}
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	message := strings.ToLower(strings.Join(strings.Fields(schedErrorMessage(body)), " "))
	if schedUnsupportedModelMessage.MatchString(message) || schedProtocolTemplate.MatchString(message) {
		return true
	}
	for _, phrase := range []string{
		"this site does not support this request", "this upstream does not support this request",
		"this endpoint does not support this request", "unsupported frontend", "unsupported input capability",
		"站点不支持这种请求", "上游不支持这种请求",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// Only explicit error objects are eligible; do not scan echoed input or output.
func schedErrorFields(body []byte) gjson.Result {
	root := gjson.ParseBytes(body)
	if fields := root.Get("error"); fields.IsObject() {
		return fields
	}
	if fields := root.Get("response.error"); fields.IsObject() {
		return fields
	}
	return gjson.Result{}
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
