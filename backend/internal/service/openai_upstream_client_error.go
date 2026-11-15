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

// isOpenAIDeterministicClientError 判断上游状态码是否表示「请求本身非法」。
//
// 只认 400：同一份请求体换任何账号、重试多少次都会得到同样的结果。
//   - 401/402/403 是网关运营方的凭据/账单问题，继续包成 502，不向客户端暴露上游账号状态。
//   - 404/405 既可能是模型不存在，也可能是上游 base_url 配错，同属运营方问题，保持现状。
//   - 429 已有独立分支映射成 429。
//   - 413 在更上面就按 request-body-too-large 走 failover 了。
func isOpenAIDeterministicClientError(statusCode int) bool {
	return statusCode == http.StatusBadRequest
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
	errType, code, param, message := UpstreamClientErrorFields(body, upstreamMsg)
	errorPayload := gin.H{"type": errType, "message": message}
	if code != "" {
		errorPayload["code"] = code
	}
	if param != "" {
		errorPayload["param"] = param
	}

	writeOpenAICollaborationErrorJSON(c, statusCode, gin.H{"error": errorPayload})
}

// WriteOpenAIUpstreamClientError preserves a structured deterministic upstream
// client error when the handler has exhausted all eligible accounts.
func WriteOpenAIUpstreamClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	writeOpenAIUpstreamClientError(c, statusCode, body, upstreamMsg)
}

// UpstreamClientErrorFields projects safe scalar fields and the already-redacted
// message. It never falls back to an unsanitized message in the upstream body.
func UpstreamClientErrorFields(body []byte, upstreamMsg string) (errType, code, param, message string) {
	errType = openAIUpstreamClientErrorFallbackType
	if utf8.Valid(body) && gjson.ValidBytes(body) {
		if v := gjson.GetBytes(body, "error.type"); v.Type == gjson.String && strings.TrimSpace(v.Str) != "" {
			errType = strings.TrimSpace(v.Str)
		}
		candidates := []gjson.Result{gjson.GetBytes(body, "error.code"), openAITopLevelStreamError(body).Get("code")}
		inner := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
		if strings.HasPrefix(inner, "{") {
			if last := strings.LastIndex(inner, "}"); last >= 0 && gjson.Valid(inner[:last+1]) {
				candidates = append(candidates, gjson.Get(inner[:last+1], "error.code"))
			}
		}
		for _, v := range candidates {
			if v.Type == gjson.String || v.Type == gjson.Number {
				if value := strings.TrimSpace(v.String()); value != "" {
					code = value
					break
				}
			}
		}
		if v := gjson.GetBytes(body, "error.param"); v.Type == gjson.String {
			param = strings.TrimSpace(v.Str)
		}
	}
	message = strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = openAIUpstreamClientErrorFallbackMessage
	}
	return
}

// isToolHistoryUpstreamClientError recognizes only the established structured
// HTTP 400 tool-history rejection, without validating local history.
//
//nolint:unused // M6/M8 own the passthrough call sites after this prerequisite.
func isToolHistoryUpstreamClientError(status int, body []byte) bool {
	if status != http.StatusBadRequest || !utf8.Valid(body) || !gjson.ValidBytes(body) {
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
