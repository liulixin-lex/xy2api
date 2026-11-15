package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatOutputPart is an ordered text or thinking segment in a Chat response.
// It is deliberately local to the bridge: request content and ChatDelta retain
// their existing wire contracts.
type ChatOutputPart struct {
	Kind string
	Text string
}

// DecodeChatOutputContent accepts the output dialects supported by the bridge.
func DecodeChatOutputContent(raw json.RawMessage) ([]ChatOutputPart, error) {
	return decodeChatOutputContent(raw, nil, "content")
}

func decodeChatOutputContent(raw json.RawMessage, record func(ConversionNotice), field string) ([]ChatOutputPart, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return []ChatOutputPart{{Kind: "text", Text: value}}, nil
	}
	var parts []map[string]json.RawMessage
	if raw[0] != '[' {
		return nil, fmt.Errorf("chat output content must be a string, array or null")
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	out := make([]ChatOutputPart, 0, len(parts))
	for i, part := range parts {
		var kind, value string
		if err := json.Unmarshal(part["type"], &kind); err != nil {
			return nil, fmt.Errorf("content[%d].type: %w", i, err)
		}
		switch kind {
		case "text":
			var err error
			value, err = bridgeChatOutputText(part["text"])
			if err != nil {
				return nil, fmt.Errorf("content[%d].text: %w", i, err)
			}
		case "thinking":
			thinking := bytes.TrimSpace(part["thinking"])
			if len(thinking) > 0 && thinking[0] == '[' {
				var nested []json.RawMessage
				if err := json.Unmarshal(thinking, &nested); err != nil {
					return nil, fmt.Errorf("content[%d].thinking: %w", i, err)
				}
				var joined strings.Builder
				for j, raw := range nested {
					text, err := bridgeChatThinkingText(raw)
					if err != nil {
						return nil, fmt.Errorf("content[%d].thinking[%d]: %w", i, j, err)
					}
					joined.WriteString(text)
				}
				value = joined.String()
			} else {
				var err error
				value, err = bridgeChatThinkingText(thinking)
				if err != nil {
					return nil, fmt.Errorf("content[%d].thinking: %w", i, err)
				}
			}
		default:
			if kind == "" {
				return nil, fmt.Errorf("content[%d].type: expected a nonempty string", i)
			}
			if record != nil {
				record(ConversionNotice{Field: fmt.Sprintf("%s[%d]", field, i), Action: "omit"})
			}
			continue
		}
		out = append(out, ChatOutputPart{Kind: kind, Text: value})
	}
	return out, nil
}

func bridgeChatOutputText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", fmt.Errorf("expected a string")
	}
	var text string
	err := json.Unmarshal(raw, &text)
	return text, err
}

func bridgeChatThinkingText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return "", err
		}
		raw = object["text"]
	}
	return bridgeChatOutputText(raw)
}

// DecodeChatBridgeChunk preserves content arrays separately while using the
// existing decoder for all other fields. Neither the caller's bytes nor the
// default ChatDelta decoder are changed.
func DecodeChatBridgeChunk(raw []byte) (*ChatCompletionsChunk, map[int]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, nil, err
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(object["choices"], &choices); err != nil && len(object["choices"]) > 0 {
		return nil, nil, err
	}
	arrays := make(map[int]json.RawMessage)
	for i, choice := range choices {
		var delta map[string]json.RawMessage
		if len(choice["delta"]) == 0 {
			continue
		}
		if err := json.Unmarshal(choice["delta"], &delta); err != nil {
			return nil, nil, err
		}
		content := bytes.TrimSpace(delta["content"])
		if len(content) == 0 || content[0] != '[' {
			continue
		}
		if _, err := DecodeChatOutputContent(content); err != nil {
			return nil, nil, err
		}
		arrays[i] = append(json.RawMessage(nil), content...)
		delete(delta, "content")
		choice["delta"], _ = json.Marshal(delta)
	}
	object["choices"], _ = json.Marshal(choices)
	copyRaw, err := json.Marshal(object)
	if err != nil {
		return nil, nil, err
	}
	var chunk ChatCompletionsChunk
	if err := json.Unmarshal(copyRaw, &chunk); err != nil {
		return nil, nil, err
	}
	return &chunk, arrays, nil
}
