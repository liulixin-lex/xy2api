package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// openAIRawStreamTruncatedUpstreamMessage 是 raw CC 直转路径上游截断的 ops 消息。
const openAIRawStreamTruncatedUpstreamMessage = "Upstream Chat Completions stream ended before any terminal chunk"

// openAIRawStreamTerminalState separates actual finish/DONE from the compatible
// complete trailing meter. Only new generation reopens a meter-closed answer.
type openAIRawStreamTerminalState struct {
	strict                   bool
	failurePayload           string
	sawDataLine              bool
	sawDone                  bool
	sawUsage                 bool
	sawFinishReason          bool
	sawMalformed             bool
	pendingMalformed         bool
	sawUnfinishedGeneration  bool
	sawCompleteTrailingUsage bool
}

// ObserveDataLine accepts the complete data payload of an SSE frame.
func (t *openAIRawStreamTerminalState) ObserveDataLine(payload string) {
	if t == nil {
		return
	}
	t.sawDataLine = true
	if payload == "[DONE]" {
		t.pendingMalformed = false
		t.sawDone = true
		return
	}
	if !gjson.Valid(payload) || !gjson.Parse(payload).IsObject() {
		t.pendingMalformed = true
		return
	}
	t.pendingMalformed = false
	root := gjson.Parse(payload)
	if resilience.ChatErrorField(root) || root.Get("type").String() == "error" {
		if t.failurePayload == "" {
			t.failurePayload = payload
		}
		return
	}
	choices := root.Get("choices")
	for _, choice := range choices.Array() {
		delta := choice.Get("delta")
		generation := len(delta.Get("tool_calls").Array()) > 0 || delta.Get("function_call").IsObject()
		for _, field := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
			v := delta.Get(field)
			generation = generation || v.Type == gjson.String && v.Str != "" || v.IsArray() && len(v.Array()) > 0
		}
		if generation {
			t.sawUnfinishedGeneration = true
			t.sawCompleteTrailingUsage = false
		}
		finish := choice.Get("finish_reason")
		if finish.Type == gjson.Null || !finish.Exists() || finish.String() == "" {
			continue
		}
		if finish.Type == gjson.String {
			t.sawFinishReason = true
		} else {
			t.sawMalformed = true
		}
	}
	counts := apicompat.ParseOpenAIUsageCounts(root.Get("usage"))
	_, present := openAIResponsesUsageEvidence([]byte(payload), "upstream_sse_progressive")
	t.sawUsage = t.sawUsage || present
	if choices.IsArray() && len(choices.Array()) == 0 && counts.InputTokensPresent && counts.OutputTokensPresent {
		t.sawCompleteTrailingUsage = true
		t.sawUnfinishedGeneration = false
	}
}

func (t *openAIRawStreamTerminalState) Terminated() bool {
	return t != nil && t.failurePayload == "" && !t.sawMalformed && !t.pendingMalformed && (t.sawDone || t.sawFinishReason)
}

func (t *openAIRawStreamTerminalState) IsTruncated(clientOutputStarted bool) bool {
	if t == nil {
		return false
	}
	if t.failurePayload != "" || t.sawMalformed || t.pendingMalformed {
		return true
	}
	if t.Terminated() || t.sawCompleteTrailingUsage {
		return false
	}
	return t.sawUnfinishedGeneration || !t.sawDataLine && !clientOutputStarted
}

// newOpenAIRawStreamTruncatedFailoverError 处理"上游截断且尚未向客户端写出任何
// 字节"的情况：响应头还没提交，可以透明换号重试，客户端不会看到半截流。
func newOpenAIRawStreamTruncatedFailoverError(
	c *gin.Context,
	account *Account,
	upstreamRequestID string,
	cause error,
) *UpstreamFailoverError {
	recordOpenAIRawStreamTruncation(c, account, upstreamRequestID, cause, "failover")

	headers := http.Header{}
	if id := strings.TrimSpace(upstreamRequestID); id != "" {
		headers.Set("x-request-id", id)
	}
	return &UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    openAIRawStreamTruncatedErrorBody(cause),
		ResponseHeaders: headers,
	}
}

// recordOpenAIRawStreamTruncation 把上游截断记入 ops 上下文，使其在错误日志与
// 账号健康度中可见——这正是此前"HTTP 200 假成功"丢掉的信息。
func recordOpenAIRawStreamTruncation(
	c *gin.Context,
	account *Account,
	upstreamRequestID string,
	cause error,
	kind string,
) {
	if c == nil {
		return
	}
	message := openAIRawStreamTruncatedMessage(cause)
	platform := PlatformOpenAI
	accountID := int64(0)
	accountName := ""
	if account != nil {
		platform = account.Platform
		accountID = account.ID
		accountName = account.Name
	}

	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           platform,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               kind,
		Message:            message,
	})
}

// openAIRawStreamTruncatedMessage 拼出 ops 消息：干净 EOF 没有底层错误可带，
// 传输层错误（connection reset / http2 stream error）则保留原因以便定位。
func openAIRawStreamTruncatedMessage(cause error) string {
	if cause == nil || errors.Is(cause, ErrOpenAIUpstreamStreamTruncated) {
		return openAIRawStreamTruncatedUpstreamMessage
	}
	return openAIRawStreamTruncatedUpstreamMessage + ": " + cause.Error()
}

// openAIRawStreamTruncatedErrorBody 构造 failover 错误体，code/message 与
// 写出后走 openAIUpstreamStreamReadError 的客户端分类保持一致。
func openAIRawStreamTruncatedErrorBody(cause error) []byte {
	code, message := classifyOpenAIUpstreamStreamReadError(cause)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    code,
			"message": message,
		},
	})
	if err != nil {
		return []byte(`{"error":{"type":"upstream_error","code":"` + OpenAIUpstreamStreamTruncatedCode +
			`","message":"Upstream response stream ended before completion"}}`)
	}
	return body
}
