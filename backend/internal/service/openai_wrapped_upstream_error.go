package service

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// openAIWrappedUpstreamErrorStatus recognizes the two relay-wrapped error
// frame shapes and returns the upstream status they carry. sseEvent is the raw
// SSE `event:` value of the frame ("" when absent), payload its data JSON.
//
//   - wrapped:   one of the validated shapes; status is in 400–599.
//   - malformed: looks like a wrapped frame but fails validation; callers
//     treat it as an ordinary protocol failure and count it.
func openAIWrappedUpstreamErrorStatus(sseEvent string, payload []byte) (status int, wrapped bool, malformed bool) {
	eventType := strings.TrimSpace(sseEvent)
	payloadType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if !gjson.ValidBytes(payload) {
		if payloadType == "proxy.upstream_error" ||
			(payloadType == "response.failed" && strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()) == "upstream_error") ||
			(eventType == "error" && bytes.Contains(payload, []byte(`"proxy.upstream_error"`))) ||
			(bytes.Contains(payload, []byte(`"response.failed"`)) && bytes.Contains(payload, []byte(`"upstream_error"`))) {
			return 0, false, true
		}
		return 0, false, false
	}

	// E1 is deliberately tied to the raw SSE event field. A data.type alone
	// must never turn an ordinary response.failed/error payload into a wrapper.
	if payloadType == "proxy.upstream_error" {
		if eventType != "error" {
			return 0, false, true
		}
		status, ok := strictJSONIntegerStatus(payload, "status")
		if !ok || status < http.StatusBadRequest || status > 599 {
			return 0, false, true
		}
		return status, true, false
	}

	// E2 is identified by its data payload, with the string code and integer
	// status_code required to agree exactly.
	if payloadType != "response.failed" || strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()) != "upstream_error" {
		return 0, false, false
	}
	codeResult := gjson.GetBytes(payload, "response.error.code")
	if codeResult.Type != gjson.String || codeResult.Str == "" {
		return 0, false, true
	}
	for _, r := range codeResult.Str {
		if r < '0' || r > '9' {
			return 0, false, true
		}
	}
	code, err := strconv.Atoi(codeResult.Str)
	if err != nil {
		return 0, false, true
	}
	status, ok := strictJSONIntegerStatus(payload, "response.error.status_code")
	if !ok || status < http.StatusBadRequest || status > 599 || status != code {
		return 0, false, true
	}
	return status, true, false
}

func strictJSONIntegerStatus(payload []byte, path string) (int, bool) {
	result := gjson.GetBytes(payload, path)
	if result.Type != gjson.Number || result.Raw == "" {
		return 0, false
	}
	for i, r := range result.Raw {
		if i == 0 && r == '-' {
			continue
		}
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	status, err := strconv.Atoi(result.Raw)
	return status, err == nil
}

func openAIWrappedUpstreamErrorClassificationBody(payload []byte) []byte {
	upstreamBody := gjson.GetBytes(payload, "upstream_body")
	if upstreamBody.Type == gjson.String {
		body := []byte(upstreamBody.Str)
		if gjson.ValidBytes(body) {
			return body
		}
	} else if upstreamBody.Exists() && gjson.ValidBytes([]byte(upstreamBody.Raw)) {
		return []byte(upstreamBody.Raw)
	}
	return payload
}

func logOpenAIWrappedErrorMalformed(ctx context.Context, account *Account, path string) {
	fields := []zap.Field{zap.String("path", strings.TrimSpace(path))}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(ctx).Warn("resilience.wrapped_error_malformed", fields...)
}
