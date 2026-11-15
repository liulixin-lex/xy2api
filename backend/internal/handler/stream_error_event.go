package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/liulixin-lex/xy2api/internal/pkg/ctxkey"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// writeResponsesFailedSSEWithStatus emits a `response.failed` SSE event in the OpenAI
// Responses API protocol after the stream has already started.
//
// 必要性：一旦 SSE 头和任意数据（例如等待槽位时的 ping comment）已经 flush，
// HTTP 200 状态码就被固化。此后若网关需要回报错误，只能继续通过 SSE 事件传达。
// 通用的 `event: error` 帧不是 Responses 协议规定的终止事件，
// Codex CLI 等严格 SDK 会因为没收到 `response.completed/failed/incomplete/cancelled`
// 而抛出 "stream closed before response.completed"。
//
// 字段集对齐 apicompat.makeResponsesCompletedEvent：id/object/model/status/output/error。
// sequence_number 始终写出（未知时为 0）：OpenAI spec 标可选，但 grok-build 当必填。
//
// 返回 true 表示已尝试 SSE 写出（不论 Write 是否成功，caller 都应直接 return）。
// 返回 false 表示 writer 不支持 Flusher，无法以 SSE 形式回报错误；
// 此时 caller 也无法回退到 JSON（HTTP 200 已固化），通常意味着连接已经损坏，
// 应当让请求处理函数 return，由上层关闭连接。
func writeResponsesFailedSSEWithStatus(c *gin.Context, status int, errType, code, message string) bool {
	return writeResponsesFailedSSEWithOpeningStatus(c, status, errType, code, message, false)
}

// forceOpening covers queue pings on compatible ingress, including when
// resilience is disabled or delivery tracking was not installed.
func writeResponsesFailedSSEWithOpeningStatus(c *gin.Context, status int, errType, code, message string, forceOpening bool) bool {
	if service.ProtocolTerminalClaimed(c) {
		return true
	}
	return writeHandlerProtocolError(c, service.PreOutputProtocolResponses, status, errType, code, "", message)
}

//nolint:unused // Retains the legacy wrapper signature for handler integrations.
func writeResponsesFailedSSEWithOpeningResult(c *gin.Context, errType, code, message string, forceOpening bool) (bool, bool) {
	if service.ProtocolTerminalClaimed(c) {
		return true, false
	}
	wrote := writeHandlerProtocolError(c, service.PreOutputProtocolResponses, http.StatusBadGateway, errType, code, "", message)
	return wrote, wrote
}

// inboundIsResponses 判断当前请求是否落在任意 Responses 路由上
// （不区分 root 还是 compact 变体）。
//
// 不能直接用 GetInboundEndpoint(c) == EndpointResponses 比较，因为
// GetInboundEndpoint/NormalizeInboundEndpoint 会把 compact 变体归一化为
// 单独的 EndpointResponsesCompact（而不是 EndpointResponses），
// 而本函数在这里只关心“是不是 Responses 家族的请求”，
// 不需要区分 root/compact，所以不能用那个等值比较。
//
// 这里改用 FullPath 的后缀/子串判断，一次性覆盖 root 和 compact 的所有变体：
//   - /v1/responses
//   - /v1/responses/compact
//   - /responses
//   - /responses/compact
//   - /backend-api/codex/responses
//   - /backend-api/codex/responses/compact
//
// 对于通配路由（如 "/v1/responses/*action"）注册的 FullPath 本身就带有
// "/responses/" 子串（例如 "/v1/responses/*action"），所以下面的
// strings.Contains(p, "/responses/") 分支同样能覆盖这些通配路由，
// 不需要额外处理通配符本身。
func inboundIsResponses(c *gin.Context) bool {
	if c == nil {
		return false
	}
	p := strings.TrimRight(c.FullPath(), "/")
	if p == "" && c.Request != nil && c.Request.URL != nil {
		p = strings.TrimRight(c.Request.URL.Path, "/")
	}
	if p == "" {
		return false
	}
	return strings.HasSuffix(p, "/responses") || strings.Contains(p, "/responses/")
}

// synthesizeResponseID 为合成的 response.failed 事件生成一个稳定的 id。
// 优先复用 server 端生成的 request_id（存在 request.Context 里，由 request_logger 写入），
// 以便客户端报错能与 server 日志关联；缺失时回退 uuid。
func synthesizeResponseID(c *gin.Context) string {
	if c != nil && c.Request != nil {
		if rid, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
			if rid = strings.TrimSpace(rid); rid != "" {
				return "resp_" + strings.ReplaceAll(rid, "-", "")
			}
		}
	}
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// requestModel 取当前请求的 inbound model（由 setOpsRequestContext 写入）。
// 缺失时返回 ""；caller 据此决定是否忽略该字段。
func requestModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(opsModelKey); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// mapResponsesErrorCode 把内部 errType 映射为 Responses 协议常见的 error.code。
// 无明确映射时原样返回，保证至少可读。
func mapResponsesErrorCode(errType, code string) string {
	if mapped := resilienceResponsesCode(errType, code); mapped != "" {
		return mapped
	}
	if code != "" {
		return code
	}
	switch errType {
	case "rate_limit_error":
		return "rate_limit_exceeded"
	case "invalid_request_error":
		return "invalid_request"
	case "permission_error":
		return "permission_denied"
	case "authentication_error":
		return "authentication_failed"
	case "upstream_error":
		return "upstream_error"
	case "server_error", "api_error", "":
		return "server_error"
	default:
		return errType
	}
}
