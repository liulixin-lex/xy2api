package service

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/liulixin-lex/xy2api/internal/resilience"
	"github.com/tidwall/gjson"
)

// SSE delivery classification (design §8, review R03).
//
// Only the OpenAI Responses path held its lifecycle events back until the
// first semantic output. Every other stream writes opening events as soon
// as the upstream answers: the Messages conversion and Anthropic streams a
// message_start and pings, the Chat conversion a role chunk, Grok and
// passthrough streams response.created and comments. The byte count then
// called a client that left before any content "delivered", so the
// undelivered rules (cancel the upstream, no charge) did not apply.
//
// The delivery writer reads the blocks of an event-stream response until
// the first content block. Only a narrow list of blocks counts as opening;
// anything else, including errors and [DONE], is content, so an unknown
// block can only fall back to the earlier count. The events themselves go
// out unchanged and the failover checks do not use this.

const (
	sseKindUnknown int32 = iota
	sseKindEventStream
	sseKindOther
)

// ssePendingLimit bounds the bytes held while waiting for a block end; a
// longer block is content.
const ssePendingLimit = 64 << 10

type sseDeliveryState struct {
	kind    atomic.Int32
	content atomic.Bool

	mu      sync.Mutex
	pending []byte
}

// observing reports whether writes still need classifying.
func (st *sseDeliveryState) observing() bool {
	return !st.content.Load() && st.kind.Load() != sseKindOther
}

// observeStream classifies the bytes written to an event-stream response
// until its first content block.
func (w *deliveryWriter) observeStream(p []byte) {
	st := &w.stream
	if st.content.Load() {
		return
	}
	switch st.kind.Load() {
	case sseKindOther:
		return
	case sseKindUnknown:
		kind := sseKindOther
		if strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
			kind = sseKindEventStream
		}
		st.kind.CompareAndSwap(sseKindUnknown, kind)
		if st.kind.Load() != sseKindEventStream {
			return
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.content.Load() {
		return
	}
	st.pending = append(st.pending, p...)
	for {
		end, sep := sseBlockEnd(st.pending)
		if end < 0 {
			break
		}
		block := st.pending[:end]
		st.pending = st.pending[end+sep:]
		if !sseBlockIsOpening(block) {
			st.content.Store(true)
			st.pending = nil
			return
		}
	}
	if len(st.pending) > ssePendingLimit {
		st.content.Store(true)
		st.pending = nil
	}
}

// sseBlockEnd returns the end of the first complete SSE block and the length
// of its separator, or -1.
func sseBlockEnd(p []byte) (int, int) {
	best, sep := -1, 0
	for _, s := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n"), []byte("\r\r")} {
		if i := bytes.Index(p, s); i >= 0 && (best < 0 || i < best) {
			best, sep = i, len(s)
		}
	}
	return best, sep
}

// sseBlockIsOpening reports whether a complete SSE block carries no content:
// comments, pings, keepalive events, Anthropic message_start and an empty
// text or thinking content_block_start, Responses created/in_progress/queued,
// and Chat chunks that only announce the role. Blank blocks are opening too.
func sseBlockIsOpening(block []byte) bool {
	event := ""
	var data []byte
	for _, raw := range bytes.Split(block, []byte("\n")) {
		line := bytes.TrimRight(raw, "\r")
		switch {
		case len(bytes.TrimSpace(line)) == 0, line[0] == ':':
			continue
		case bytes.HasPrefix(line, []byte("event:")):
			event = strings.TrimSpace(string(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimSpace(line[len("data:"):])...)
		default:
			// id:, retry: and unknown fields carry no content of their own.
			continue
		}
	}
	if len(data) == 0 {
		switch event {
		case "", "ping", "keepalive":
			return true
		}
		return false
	}
	if !gjson.ValidBytes(data) {
		return false
	}
	typ := gjson.GetBytes(data, "type").String()
	if typ == "" {
		typ = event
	}
	if resilience.Current().NativePreludeWhitelistOn() && (strings.HasPrefix(typ, "response.") || typ == "keepalive" || typ == "codex.rate_limits") {
		return resilience.ClassifyStreamEvent("responses", event, data).Kind == resilience.Prelude
	}
	if resilience.Current().PreludeStagingOn() {
		if gjson.GetBytes(data, "object").String() == "chat.completion.chunk" {
			return resilience.ClassifyStreamEvent("chat", event, data).Kind == resilience.Prelude
		}
		if typ == "message_start" || typ == "ping" || strings.HasPrefix(typ, "content_block_") {
			return resilience.ClassifyStreamEvent("messages", event, data).Kind == resilience.Prelude
		}
	}
	switch typ {
	case "ping", "keepalive", "message_start",
		"response.created", "response.in_progress", "response.queued":
		return true
	case "content_block_start":
		block := gjson.GetBytes(data, "content_block")
		switch block.Get("type").String() {
		case "text":
			return block.Get("text").String() == ""
		case "thinking":
			return block.Get("thinking").String() == ""
		}
		return false
	}
	if gjson.GetBytes(data, "object").String() == "chat.completion.chunk" {
		return chatChunkIsOpening(data)
	}
	return false
}

// chatChunkIsOpening reports a Chat chunk that only announces the role: no
// content, reasoning, tool call, finish reason or usage.
func chatChunkIsOpening(data []byte) bool {
	if u := gjson.GetBytes(data, "usage"); u.Exists() && u.Type != gjson.Null {
		return false
	}
	choices := gjson.GetBytes(data, "choices")
	if !choices.IsArray() || len(choices.Array()) == 0 {
		return false
	}
	for _, ch := range choices.Array() {
		if f := ch.Get("finish_reason"); f.Exists() && f.Type != gjson.Null {
			return false
		}
		delta := ch.Get("delta")
		opening := true
		delta.ForEach(func(key, value gjson.Result) bool {
			switch key.String() {
			case "role":
				return true
			case "content", "reasoning_content", "reasoning", "refusal":
				if value.Type == gjson.Null || value.String() == "" {
					return true
				}
			}
			opening = false
			return false
		})
		if !opening {
			return false
		}
	}
	return true
}
