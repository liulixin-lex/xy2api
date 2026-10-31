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
