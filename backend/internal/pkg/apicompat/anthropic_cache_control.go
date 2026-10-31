package apicompat

import (
	"encoding/json"
	"github.com/tidwall/sjson"
)

func applyMessageCacheControl(blocks []AnthropicContentBlock, cc *AnthropicCacheControl) []AnthropicContentBlock {
	if cc != nil && len(blocks) > 0 && blocks[len(blocks)-1].CacheControl == nil {
		blocks[len(blocks)-1].CacheControl = cc
	}
	return blocks
}

func applyMessageCacheControlJSON(content json.RawMessage, cc *AnthropicCacheControl) json.RawMessage {
	if cc == nil {
		return content
	}
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		var text string
		if json.Unmarshal(content, &text) != nil {
			return content
		}
		raw, _ := json.Marshal(AnthropicContentBlock{Type: "text", Text: text})
		blocks = []json.RawMessage{raw}
	}
	if len(blocks) == 0 {
		return content
	}
	var last map[string]json.RawMessage
	if json.Unmarshal(blocks[len(blocks)-1], &last) != nil {
		return content
	}
	if _, exists := last["cache_control"]; exists {
		return content
	}
	raw, err := json.Marshal(cc)
	if err != nil {
		return content
	}
	blocks[len(blocks)-1], err = sjson.SetRawBytes(blocks[len(blocks)-1], "cache_control", raw)
	if err != nil {
		return content
	}
	out, err := json.Marshal(blocks)
	if err != nil {
		return content
	}
	return out
}

func (cc *AnthropicCacheControl) UnmarshalJSON(data []byte) error {
	type wire AnthropicCacheControl
	var v wire
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*cc = AnthropicCacheControl(v)
	if err := json.Unmarshal(data, &cc.Extra); err != nil {
		return err
	}
	for _, name := range []string{"type", "ttl", "scope"} {
		delete(cc.Extra, name)
	}
	return nil
}

func (cc AnthropicCacheControl) MarshalJSON() ([]byte, error) {
	type wire AnthropicCacheControl
	raw, err := json.Marshal(wire(cc))
	if err != nil || len(cc.Extra) == 0 {
		return raw, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for k, v := range cc.Extra {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}
	return json.Marshal(fields)
}

// applyChatMessageCacheControl carries a Chat message-level marker onto its
// converted Responses items. Message items keep the existing rule. A turn
// without a message item (a tool or legacy function result, or an assistant
// turn holding only tool_calls) marks its last item, which becomes the
// tool_result or tool_use block that ends the turn. Tool output is flattened
// to text, so a result without a message-level marker lifts its last part
// marker instead. Tool items keep the marker in chatCacheControl: the same
// Responses input also feeds OpenAI Responses upstreams, which must not get it.
func applyChatMessageCacheControl(items []ResponsesInputItem, m ChatMessage) {
	hasMessage := false
	for i := range items {
		if items[i].Type == "message" {
			items[i].CacheControl = m.CacheControl
			hasMessage = true
		}
	}
	if hasMessage || len(items) == 0 {
		return
	}
	last := &items[len(items)-1]
	switch last.Type {
	case "function_call":
		last.chatCacheControl = m.CacheControl
	case "function_call_output":
		last.chatCacheControl = m.CacheControl
		if last.chatCacheControl == nil {
			last.chatCacheControl = lastPartCacheControl(m.Content)
		}
	}
}

// chatInputCacheControls indexes the Chat tool markers by input position, so
// ResponsesToAnthropicRequest can restore them after decoding the marshaled
// input. It returns nil when no tool item is marked.
func chatInputCacheControls(items []ResponsesInputItem) []*AnthropicCacheControl {
	var out []*AnthropicCacheControl
	for i := range items {
		if items[i].chatCacheControl != nil {
			if out == nil {
				out = make([]*AnthropicCacheControl, len(items))
			}
			out[i] = items[i].chatCacheControl
		}
	}
	return out
}

// restoreChatCacheControls is the inverse of chatInputCacheControls. An input
// of another length was replaced after conversion and gets nothing.
func restoreChatCacheControls(items []ResponsesInputItem, ccs []*AnthropicCacheControl) {
	if len(ccs) != len(items) {
		return
	}
	for i := range items {
		items[i].chatCacheControl = ccs[i]
	}
}

// responsesItemCacheControl returns the marker of a function_call: the item
// marker, otherwise the marker carried from a Chat message.
func responsesItemCacheControl(item ResponsesInputItem) *AnthropicCacheControl {
	if item.CacheControl != nil {
		return item.CacheControl
	}
	return item.chatCacheControl
}

// responsesToolOutputCacheControl returns the single marker of a
// function_call_output. Gateway cache passes read only block-level markers,
// so a marker inside the output is lifted onto the tool_result: the item
// marker wins, then a Chat message marker, then the last marked output part.
func responsesToolOutputCacheControl(item ResponsesInputItem) *AnthropicCacheControl {
	if cc := responsesItemCacheControl(item); cc != nil {
		return cc
	}
	return lastPartCacheControl(item.outputRaw)
}

// lastPartCacheControl reads markers from the client's parts before any
// filtering, so a marker on a dropped part still marks its result. Thinking
// parts never carry a usable marker.
func lastPartCacheControl(raw json.RawMessage) *AnthropicCacheControl {
	var parts []struct {
		Type         string                 `json:"type"`
		CacheControl *AnthropicCacheControl `json:"cache_control"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].CacheControl != nil && parts[i].Type != "thinking" && parts[i].Type != "redacted_thinking" {
			return parts[i].CacheControl
		}
	}
	return nil
}
