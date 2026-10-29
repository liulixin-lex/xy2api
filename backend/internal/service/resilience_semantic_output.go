package service

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// Observability excludes error/DONE frames that the billing delivery probe
// counts. Only successfully written bytes reach this parser.
type semanticDeliveryState struct {
	noted     atomic.Bool
	mu        sync.Mutex
	pending   []byte
	oversized bool
}

func (w *deliveryWriter) observeSemanticOutput(p []byte) {
	st := &w.semantic
	if st.noted.Load() || w.state == nil || w.Status() >= 400 {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.noted.Load() {
		return
	}
	st.pending = append(st.pending, p...)
	stream := strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream")
	note := func() {
		st.noted.Store(true)
		st.pending = nil
		w.state.NoteSemanticOutput(time.Now())
	}
	if !stream {
		if semanticJSONOutput(gjson.ParseBytes(st.pending)) {
			note()
			return
		}
		if len(st.pending) > ssePendingLimit {
			st.pending = st.pending[:ssePendingLimit]
		}
		return
	}
	for {
		end, sep := sseBlockEnd(st.pending)
		if end < 0 {
			break
		}
		if !st.oversized && semanticSSEBlock(st.pending[:end]) {
			note()
			return
		}
		st.pending = st.pending[end+sep:]
		st.oversized = false
	}
	if len(st.pending) > ssePendingLimit {
		if !st.oversized && semanticSSEBlock(st.pending) {
			note()
			return
		}
		st.pending = append([]byte(nil), st.pending[len(st.pending)-3:]...)
		st.oversized = true
	}
}

func semanticSSEBlock(block []byte) bool {
	event, data := sseBlockFields(block)
	root := gjson.ParseBytes(data)
	typ := root.Get("type").String()
	if typ == "" {
		typ = event
	}
	if typ == "error" || typ == "response.failed" || resilience.ChatErrorField(root) {
		return false
	}
	if strings.HasPrefix(typ, "response.") {
		return openAIStreamDataStartsVisibleOutput(string(data), typ)
	}
	switch typ {
	case "image_generation.partial_image", "image_generation.completed":
		return root.Get("b64_json").String() != ""
	case "content_block_start":
		return semanticMessageBlock(root.Get("content_block"))
	case "content_block_delta":
		delta := root.Get("delta")
		for _, key := range []string{"text", "thinking", "partial_json"} {
			if delta.Get(key).String() != "" {
				return true
			}
		}
		return false
	}
	return semanticJSONOutput(root)
}

func semanticMessageBlock(block gjson.Result) bool {
	switch block.Get("type").String() {
	case "text", "input_text", "output_text":
		text := block.Get("text")
		return text.Type == gjson.String && text.Str != ""
	case "thinking":
		return block.Get("thinking").String() != ""
	case "tool_use", "server_tool_use":
		return block.Get("name").String() != ""
	default:
		return block.Get("data").String() != "" || block.Get("source.data").String() != ""
	}
}

func semanticJSONOutput(root gjson.Result) bool {
	if resilience.ChatErrorField(root) {
		return false
	}
	for _, item := range root.Get("output").Array() {
		if openAIStreamItemHasVisibleOutput(item) {
			return true
		}
	}
	for _, block := range root.Get("content").Array() {
		if semanticMessageBlock(block) {
			return true
		}
	}
	for _, ch := range root.Get("choices").Array() {
		for _, key := range []string{"delta", "message"} {
			msg := ch.Get(key)
			for _, field := range []string{"content", "reasoning_content", "reasoning", "refusal"} {
				if semanticChatContent(msg.Get(field)) {
					return true
				}
			}
			for _, call := range msg.Get("tool_calls").Array() {
				if call.Get("function.name").String() != "" || call.Get("function.arguments").String() != "" {
					return true
				}
			}
		}
		if ch.Get("text").String() != "" {
			return true
		}
	}
	for _, candidate := range root.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").String() != "" || part.Get("functionCall.name").String() != "" || part.Get("inlineData.data").String() != "" {
				return true
			}
		}
	}
	for _, img := range root.Get("data").Array() {
		if img.Get("b64_json").String() != "" || img.Get("url").String() != "" {
			return true
		}
	}
	return false
}

func semanticChatContent(value gjson.Result) bool {
	if value.Type == gjson.String {
		return value.Str != ""
	}
	if value.IsArray() {
		for _, block := range value.Array() {
			if semanticMessageBlock(block) {
				return true
			}
		}
	}
	return false
}
