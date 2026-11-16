package service

import (
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// IsUpstreamCapabilityFailure recognizes an explicit upstream model/protocol
// rejection. Callers must supply the upstream response, not a gateway status or
// request body. Ordinary parameter errors and ambiguous 404s are not evidence.
func IsUpstreamCapabilityFailure(status int, body []byte) bool {
	if status < 400 || status > 599 || len(body) > 1<<20 || !gjson.ValidBytes(body) {
		return false
	}
	if resilience.IsModelUnsupportedResponse(status, body) || resilience.IsStreamOnlyFailure(status, body) {
		return true
	}
	fields := schedErrorFields(body)
	for _, key := range []string{"code", "type"} {
		switch strings.ToLower(strings.TrimSpace(fields.Get(key).String())) {
		case "model_unsupported", "model_not_found", "model_not_supported", "unsupported_model",
			"unsupported_protocol", "protocol_not_supported", "unsupported_request_type", "request_type_not_supported",
			"unsupported_frontend", "unsupported_input_capability":
			return true
		}
	}
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	message := strings.ToLower(strings.Join(strings.Fields(fields.Get("message").String()), " "))
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
