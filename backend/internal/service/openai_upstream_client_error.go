package service

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIUpstreamClientErrorFallbackType 是上游没给 error.type 时的兜底值。
// 与 handleCompatErrorResponse 对 400 的取值保持一致。
const openAIUpstreamClientErrorFallbackType = "invalid_request_error"

// openAIUpstreamClientErrorFallbackMessage 是上游连 message 都没给时的兜底文案。
// 仍然比 "Upstream request failed" 明确：它说明拒绝来自请求本身，而不是链路故障。
const openAIUpstreamClientErrorFallbackMessage = "Upstream rejected the request"

// isOpenAIDeterministicClientError selects the narrow statuses whose final
// response preserves upstream fields. In particular, 408 does not prove an
// invalid request; retry and account-health classification remain independent.
func isOpenAIDeterministicClientError(statusCode int) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusRequestTimeout, http.StatusConflict, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// writeOpenAIUpstreamClientError 以 OpenAI 错误体形状回写确定性客户端错误。
//
// 保留上游的 type/code/param：客户端靠 param 定位是哪个字段非法（上游会给出形如
// input[8].tools[1].tools[2].parameters 的路径），靠 code 判断是否值得重试。归一成
// {type:"upstream_error", message:"Upstream request failed"} 会把这些信息全部抹掉。
//
// upstreamMsg 由调用方传入，调用方已做过 sanitizeUpstreamErrorMessage 与
// redactAgentIdentitySensitiveBody；这里不重复清洗，也不回落读取原始 body 的
// message，避免绕开那两道脱敏。
func writeOpenAIUpstreamClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	errType, code, param, message := upstreamClientErrorFields(statusCode, body, upstreamMsg)
	WriteTargetProtocolError(c, PreOutputProtocolResponses, statusCode, errType, code, param, message)
}

// upstreamClientErrorFields accepts scalar metadata only. The message comes
// exclusively from the caller's redaction path, never from the original body.
func upstreamClientErrorFields(status int, body []byte, upstreamMsg string) (errType, code, param, message string) {
	errType = openAIUpstreamClientErrorFallbackType
	switch status {
	case http.StatusRequestTimeout:
		errType, code = "upstream_error", "request_timeout"
	case http.StatusConflict:
		code = "conflict"
	case http.StatusUnprocessableEntity:
		code = "unprocessable_entity"
	}
	if utf8.Valid(body) && gjson.ValidBytes(body) {
		object := gjson.GetBytes(body, "error")
		if value := object.Get("type"); value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
			errType = strings.TrimSpace(value.Str)
		}
		if value := object.Get("param"); value.Type == gjson.String {
			param = strings.TrimSpace(value.Str)
		}
		candidates := []gjson.Result{object.Get("code"), openAITopLevelStreamError(body).Get("code")}
		inner := strings.TrimSpace(object.Get("message").String())
		if strings.HasPrefix(inner, "{") {
			if end := strings.LastIndex(inner, "}"); end >= 0 && gjson.Valid(inner[:end+1]) {
				candidates = append(candidates, gjson.Get(inner[:end+1], "error.code"))
			}
		}
		for _, value := range candidates {
			if (value.Type == gjson.String || value.Type == gjson.Number) && strings.TrimSpace(value.String()) != "" {
				code = strings.TrimSpace(value.String())
				break
			}
		}
	}
	message = strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = openAIUpstreamClientErrorFallbackMessage
	}
	return
}

// WriteOpenAIUpstreamClientError preserves a structured deterministic upstream
// client error when the handler has exhausted all eligible accounts.
func WriteOpenAIUpstreamClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	writeOpenAIUpstreamClientError(c, statusCode, body, upstreamMsg)
}

// isToolHistoryUpstreamClientError recognizes only the established structured
// HTTP 400 tool-history rejection, without validating local history.
//
//nolint:unused // M6/M8 own the passthrough call sites after this prerequisite.
func isToolHistoryUpstreamClientError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest || !utf8.Valid(body) || !gjson.ValidBytes(body) {
		return false
	}
	object := gjson.GetBytes(body, "error")
	if !object.IsObject() {
		return false
	}
	message := object.Get("message")
	if message.Type != gjson.String {
		return false
	}
	matched := false
	for _, prefix := range []string{"No tool call found for function call output", "No tool output found for function call"} {
		if message.Str == prefix || strings.HasPrefix(message.Str, prefix+" ") || strings.HasPrefix(message.Str, prefix+".") || strings.HasPrefix(message.Str, prefix+":") {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	code := object.Get("code")
	if code.Exists() && code.Type != gjson.Null && (code.Type != gjson.String || code.Str != "" && code.Str != "invalid_request_error") {
		return false
	}
	param := object.Get("param")
	if !param.Exists() || param.Type == gjson.Null {
		return true
	}
	if param.Type != gjson.String {
		return false
	}
	if param.Str == "" {
		return true
	}
	for _, root := range []string{"input", "messages"} {
		if param.Str == root || strings.HasPrefix(param.Str, root+"[") || strings.HasPrefix(param.Str, root+".") {
			return true
		}
	}
	return false
}
