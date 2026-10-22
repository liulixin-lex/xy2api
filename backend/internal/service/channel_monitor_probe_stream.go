package service

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// probeProtocol 探测请求所用的协议族，决定请求体形状与回答的读法。
type probeProtocol string

const (
	// probeProtocolChat OpenAI Chat Completions 及兼容协议（Grok、Kimi、智谱、DeepSeek、MiniMax）。
	probeProtocolChat probeProtocol = "chat"
	// probeProtocolResponses OpenAI / Grok Responses。
	probeProtocolResponses probeProtocol = "responses"
	// probeProtocolAnthropic Anthropic Messages。
	probeProtocolAnthropic probeProtocol = "anthropic"
	// probeProtocolGemini Gemini generateContent / streamGenerateContent。
	probeProtocolGemini probeProtocol = "gemini"
)

// probeReading 一次读取回答的结论。
type probeReading struct {
	// Text 正文（已剥离推理标签、去掉首尾空白），用于失败时的 message。
	Text string
	// Reasoning 收到过推理输出：只算“有输出”，不参与答案校验。
	Reasoning bool
	// Answered 正文里找到了期望答案；replace 模式下为“正文完整且非空”。
	Answered bool
	// Ended 流正常结束或收到终止事件。
	Ended bool
	// StreamErr 流内（或 2xx 响应体里）的错误信息。
	StreamErr string
	// Stop finish_reason / stop_reason / incomplete 原因，仅用于诊断。
	Stop string
	// ReadErr 读取中断（连接重置、超时等）。
	ReadErr error
}

// probeAnswerTracker 边读边拼接正文并判断是否已拿到答案。
type probeAnswerTracker struct {
	protocol  probeProtocol
	expected  string // 空串 = replace 模式：正文完整且非空即可
	raw       strings.Builder
	reasoning bool
	textFinal bool // 正文已完整（finish_reason、stop_reason、终止事件、流结束）
	ended     bool
	answered  bool
	stop      string
	streamErr string
}

// readProbeStream 读 SSE 回答：边读边拼接正文，找到答案、遇到流内错误或流结束即返回。
// 注释行、ping、只带 role 的增量、usage、生命周期事件都不算回答。
func readProbeStream(r io.Reader, protocol probeProtocol, expected string) probeReading {
	t := &probeAnswerTracker{protocol: protocol, expected: expected}
	sc := bufio.NewScanner(io.LimitReader(r, monitorStreamMaxBytes))
	sc.Buffer(make([]byte, 0, 16*1024), monitorStreamLineMaxBytes)

	event := ""
	var data strings.Builder
	hasData := false
	dispatch := func() bool {
		if !hasData {
			return false
		}
		payload := data.String()
		data.Reset()
		hasData = false
		t.onEvent(event, payload)
		return t.settle()
	}

	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			done := dispatch()
			event = ""
			if done {
				return t.result(nil)
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = strings.TrimSpace(value)
		case "data":
			if hasData {
				_ = data.WriteByte('\n')
			}
			_, _ = data.WriteString(value)
			hasData = true
			// 多数实现一行一个完整 JSON：能单独解析就立即处理，不等空行。
			if p := data.String(); p == "[DONE]" || gjson.Valid(p) {
				if dispatch() {
					return t.result(nil)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return t.result(err)
	}
	if dispatch() {
		return t.result(nil)
	}
	t.ended, t.textFinal = true, true
	t.settle()
	return t.result(nil)
}

// readProbeJSON 读非流式 JSON 回答（也接受 Gemini 不带 alt=sse 时的 JSON 数组）。
func readProbeJSON(body []byte, protocol probeProtocol, expected string) probeReading {
	t := &probeAnswerTracker{protocol: protocol, expected: expected}
	trimmed := bytes.TrimSpace(body)
	switch {
	case len(trimmed) == 0:
	case trimmed[0] == '[' && gjson.ValidBytes(trimmed):
		gjson.ParseBytes(trimmed).ForEach(func(_, chunk gjson.Result) bool {
			t.onEvent("", chunk.Raw)
			return t.streamErr == ""
		})
	case gjson.ValidBytes(trimmed):
		t.onJSONBody(gjson.ParseBytes(trimmed))
	default:
		t.streamErr = "unexpected non-JSON response: " + truncateForErrorBody(string(trimmed))
	}
	t.ended, t.textFinal = true, true
	t.settle()
	return t.result(nil)
}

func (t *probeAnswerTracker) addText(s string) {
	if s == "" || t.raw.Len() >= monitorResponseMaxBytes {
		return
	}
	_, _ = t.raw.WriteString(s)
}

// boundary 在两段正文之间补一个分隔，避免 "40" 与下一段的数字拼成一个数。
func (t *probeAnswerTracker) boundary() {
	s := t.raw.String()
	if s == "" {
		return
	}
	if last := s[len(s)-1]; last != '\n' && last != ' ' {
		_ = t.raw.WriteByte('\n')
	}
}

func (t *probeAnswerTracker) visible() string {
	return stripThinkBlocks(t.raw.String())
}

// settle 判断是否可以结束读取：拿到答案、流内错误或流已结束。
func (t *probeAnswerTracker) settle() bool {
	if t.streamErr != "" {
		return true
	}
	if t.expected == "" {
		t.answered = t.textFinal && strings.TrimSpace(t.visible()) != ""
	} else {
		t.answered = probeAnswerFound(t.visible(), t.expected, t.textFinal)
	}
	return t.answered || t.ended
}

func (t *probeAnswerTracker) result(readErr error) probeReading {
	return probeReading{
		Text:      strings.TrimSpace(t.visible()),
		Reasoning: t.reasoning,
		Answered:  t.answered,
		Ended:     t.ended,
		StreamErr: t.streamErr,
		Stop:      t.stop,
		ReadErr:   readErr,
	}
}

// onEvent 处理一个 SSE 事件（或 JSON 数组里的一个分块）。
func (t *probeAnswerTracker) onEvent(event, data string) {
	data = strings.TrimSpace(data)
	if data == "[DONE]" {
		t.ended, t.textFinal = true, true
		return
	}
	if data == "" || !gjson.Valid(data) {
		if event == "error" {
			t.streamErr = "stream error event"
			if data != "" {
				t.streamErr = data
			}
		}
		return
	}
	root := gjson.Parse(data)
	switch t.protocol {
	case probeProtocolResponses:
		t.onResponsesEvent(event, root)
	case probeProtocolAnthropic:
		t.onAnthropicEvent(event, root)
	case probeProtocolGemini:
		t.onGeminiChunk(root)
	default:
		t.onChatChunk(event, root)
	}
}

// onChatChunk Chat Completions 分块（流式 delta 或非流式 message）。
func (t *probeAnswerTracker) onChatChunk(event string, root gjson.Result) {
	if e := root.Get("error"); probeErrorPresent(e) || event == "error" {
		if !probeErrorPresent(e) {
			e = root
		}
		t.streamErr = probeErrorText(e)
		return
	}
	root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		fullMessage := false
		if !delta.Exists() {
			delta = choice.Get("message")
			fullMessage = true
		}
		// 有的上游（如 MiniMax）在流的最后一块再给一次完整 message；已经收到增量正文时
		// 不能再拼上去，否则 "12" 会读成 "1212"。
		if !fullMessage || t.raw.Len() == 0 {
			t.addText(probeContentText(delta.Get("content")))
		}
		if text := choice.Get("text"); text.Type == gjson.String {
			t.addText(text.String())
		}
		for _, key := range []string{"reasoning_content", "reasoning", "reasoning_details", "thinking"} {
			if probeNonEmpty(delta.Get(key)) {
				t.reasoning = true
			}
		}
		if reason := choice.Get("finish_reason").String(); reason != "" {
			t.stop = reason
			t.textFinal = true
		}
		return true
	})
}

// onResponsesEvent Responses 流事件。
func (t *probeAnswerTracker) onResponsesEvent(event string, root gjson.Result) {
	typ := root.Get("type").String()
	if typ == "" {
		typ = event
	}
	switch {
	case typ == "response.output_text.delta":
		t.addText(root.Get("delta").String())
	case typ == "response.output_text.done":
		if t.raw.Len() == 0 {
			t.addText(root.Get("text").String())
		}
		t.boundary()
	case typ == "response.content_part.done":
		t.boundary()
	case strings.Contains(typ, "reasoning") && strings.HasSuffix(typ, ".delta"):
		if probeNonEmpty(root.Get("delta")) {
			t.reasoning = true
		}
	case typ == "response.completed" || typ == "response.incomplete" || typ == "response.done":
		resp := root.Get("response")
		if t.raw.Len() == 0 {
			t.addText(extractOpenAIResponsesText([]byte(resp.Raw)))
		}
		if probeResponsesHasReasoning(resp) {
			t.reasoning = true
		}
		if reason := resp.Get("incomplete_details.reason").String(); reason != "" {
			t.stop = reason
		}
		t.ended, t.textFinal = true, true
	case typ == "response.failed":
		e := root.Get("response.error")
		if !probeErrorPresent(e) {
			e = root.Get("error")
		}
		t.streamErr = "response failed"
		if probeErrorPresent(e) {
			t.streamErr = probeErrorText(e)
		}
	case typ == "error":
		e := root.Get("error")
		if !probeErrorPresent(e) {
			e = root
		}
		t.streamErr = probeErrorText(e)
	case typ == "":
		if e := root.Get("error"); probeErrorPresent(e) {
			t.streamErr = probeErrorText(e)
		}
	}
}

// onAnthropicEvent Anthropic Messages 流事件。
func (t *probeAnswerTracker) onAnthropicEvent(event string, root gjson.Result) {
	typ := root.Get("type").String()
	if typ == "" {
		typ = event
	}
	switch typ {
	case "content_block_start":
		block := root.Get("content_block")
		switch block.Get("type").String() {
		case "text":
			t.addText(block.Get("text").String())
		case "redacted_thinking":
			t.reasoning = true
		case "thinking":
			if probeNonEmpty(block.Get("thinking")) {
				t.reasoning = true
			}
		}
	case "content_block_delta":
		delta := root.Get("delta")
		switch delta.Get("type").String() {
		case "text_delta":
			t.addText(delta.Get("text").String())
		case "thinking_delta":
			if probeNonEmpty(delta.Get("thinking")) {
				t.reasoning = true
			}
		}
	case "content_block_stop":
		t.boundary()
	case "message_delta":
		if reason := root.Get("delta.stop_reason").String(); reason != "" {
			t.stop = reason
			t.textFinal = true
		}
	case "message_stop":
		t.ended, t.textFinal = true, true
	case "error":
		e := root.Get("error")
		if !probeErrorPresent(e) {
			e = root
		}
		t.streamErr = probeErrorText(e)
	default:
		if e := root.Get("error"); probeErrorPresent(e) {
			t.streamErr = probeErrorText(e)
		}
	}
}

// onGeminiChunk Gemini GenerateContentResponse（流式分块或完整响应）。
func (t *probeAnswerTracker) onGeminiChunk(root gjson.Result) {
	if e := root.Get("error"); probeErrorPresent(e) {
		t.streamErr = probeErrorText(e)
		return
	}
	if reason := root.Get("promptFeedback.blockReason").String(); reason != "" {
		t.streamErr = "prompt blocked: " + reason
		return
	}
	candidate := root.Get("candidates.0")
	candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
		if part.Get("thought").Bool() {
			if probeNonEmpty(part.Get("text")) {
				t.reasoning = true
			}
			return true
		}
		t.addText(part.Get("text").String())
		return true
	})
	if reason := candidate.Get("finishReason").String(); reason != "" {
		t.stop = reason
		t.textFinal = true
	}
}

// onJSONBody 非流式完整响应。
func (t *probeAnswerTracker) onJSONBody(root gjson.Result) {
	if e := root.Get("error"); probeErrorPresent(e) {
		t.streamErr = probeErrorText(e)
		return
	}
	switch t.protocol {
	case probeProtocolResponses:
		if root.Get("status").String() == "failed" {
			t.streamErr = "response failed"
			return
		}
		t.addText(extractOpenAIResponsesText([]byte(root.Raw)))
		t.reasoning = probeResponsesHasReasoning(root)
		t.stop = root.Get("incomplete_details.reason").String()
	case probeProtocolAnthropic:
		t.addText(extractAnthropicMonitorText([]byte(root.Raw)))
		root.Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "thinking", "redacted_thinking":
				t.reasoning = true
			}
			return true
		})
		t.stop = root.Get("stop_reason").String()
	case probeProtocolGemini:
		t.onGeminiChunk(root)
	default:
		t.onChatChunk("", root)
	}
}

// probeContentText 取 Chat content：字符串，或分段数组里的文本段。
func probeContentText(v gjson.Result) string {
	if v.Type == gjson.String {
		return v.String()
	}
	if !v.IsArray() {
		return ""
	}
	var b strings.Builder
	v.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "", "text", "output_text":
			_, _ = b.WriteString(part.Get("text").String())
		}
		return true
	})
	return b.String()
}

func probeResponsesHasReasoning(resp gjson.Result) bool {
	found := false
	resp.Get("output").ForEach(func(_, item gjson.Result) bool {
		found = item.Get("type").String() == "reasoning"
		return !found
	})
	return found
}

func probeNonEmpty(v gjson.Result) bool {
	switch {
	case !v.Exists():
		return false
	case v.Type == gjson.String:
		return strings.TrimSpace(v.String()) != ""
	case v.IsArray():
		return len(v.Array()) > 0
	case v.IsObject():
		return len(v.Map()) > 0
	default:
		return v.Type != gjson.Null && v.Type != gjson.False
	}
}

func probeErrorPresent(e gjson.Result) bool {
	return e.IsObject() || (e.Type == gjson.String && strings.TrimSpace(e.String()) != "")
}

// probeErrorText 从错误对象里取出可读信息：message（附带 status / code / type）。
func probeErrorText(e gjson.Result) string {
	if e.Type == gjson.String {
		return e.String()
	}
	message := e.Get("message").String()
	kind := ""
	for _, key := range []string{"status", "code", "type"} {
		if v := e.Get(key).String(); v != "" && v != "error" {
			kind = v
			break
		}
	}
	switch {
	case message != "" && kind != "":
		return message + " (" + kind + ")"
	case message != "":
		return message
	case kind != "":
		return kind
	default:
		return truncateForErrorBody(e.Raw)
	}
}
