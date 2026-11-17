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

// noteClassifiedSemanticOutput records successful delivery of an event already
// classified by the forwarding path. Spooling and chunk size cannot change the
// content decision; the bounded byte parser remains a fallback for other paths.
func (w *deliveryWriter) noteClassifiedSemanticOutput() {
	if w == nil || w.state == nil || w.Status() >= 400 || w.semantic.noted.Load() {
		return
	}
	w.semantic.mu.Lock()
	defer w.semantic.mu.Unlock()
	if !w.semantic.noted.Swap(true) {
		w.semantic.pending = nil
		w.state.NoteSemanticOutput(time.Now())
	}
}

func (w *deliveryWriter) observeSemanticOutput(p []byte, at time.Time) {
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
	if event == "error" || typ == "error" || typ == "response.failed" || resilience.ChatErrorField(root) {
		return false
	}
	if typ == "image_generation.partial_image" || typ == "image_generation.completed" {
		return root.Get("b64_json").String() != ""
	}
	if typ != "" || root.Get("choices").IsArray() {
		return resilience.ClassifyOutput("", event, data).Valid
	}
	return semanticJSONOutput(root)
}

func semanticJSONOutput(root gjson.Result) bool {
	if resilience.ChatErrorField(root) {
		return false
	}
	if root.Get("output").IsArray() || root.Get("content").IsArray() || root.Get("choices").IsArray() {
		return resilience.ClassifyOutput("", "", []byte(root.Raw)).Valid
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
