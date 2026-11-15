package apicompat

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

func EffectiveRequestMaxOutputTokens(protocol string, raw []byte) (value *int, field string, err error) {
	var paths []string
	switch protocol {
	case "chat":
		paths = chatOutputTokenFields
	case "responses":
		paths = []string{"max_output_tokens"}
	case "anthropic":
		paths = []string{"max_tokens"}
	case "gemini":
		paths = []string{"generationConfig.maxOutputTokens", "generation_config.max_output_tokens"}
	default:
		return nil, "", unsupportedConversion("protocol", "unsupported input protocol")
	}
	var root map[string]json.RawMessage
	if !utf8.Valid(raw) || json.Unmarshal(raw, &root) != nil || root == nil {
		return nil, "", unsupportedConversion("body", "expected a complete JSON object")
	}
	return selectRequestOutputLimit(paths, func(path string) json.RawMessage {
		fields := strings.Split(path, ".")
		selected := root[fields[0]]
		if len(fields) > 1 {
			var parent map[string]json.RawMessage
			if json.Unmarshal(selected, &parent) != nil {
				return nil
			}
			selected = parent[fields[1]]
		}
		return selected
	})
}

// EffectiveChatMaxOutputTokens shares raw-ingress precedence without marshaling
// unrelated request fields (which may carry opaque converter-only values).
func EffectiveChatMaxOutputTokens(req *ChatCompletionsRequest) (value *int, field string, err error) {
	if req == nil {
		return nil, "", nil
	}
	fields := map[string]*int{"max_completion_tokens": req.MaxCompletionTokens, "max_tokens": req.MaxTokens, "max_output_tokens": req.MaxOutputTokens}
	return selectRequestOutputLimit(chatOutputTokenFields, func(name string) json.RawMessage {
		if fields[name] == nil {
			return nil
		}
		raw, _ := json.Marshal(*fields[name])
		return raw
	})
}

var chatOutputTokenFields = []string{"max_completion_tokens", "max_tokens", "max_output_tokens"}

func selectRequestOutputLimit(paths []string, lookup func(string) json.RawMessage) (*int, string, error) {
	for _, path := range paths {
		selected := lookup(path)
		if len(selected) == 0 || bytes.Equal(bytes.TrimSpace(selected), []byte("null")) {
			continue
		}
		var n int
		if json.Unmarshal(selected, &n) != nil || n <= 0 {
			return nil, path, unsupportedConversion(path, "expected a positive integer token limit")
		}
		return &n, path, nil
	}
	return nil, "", nil
}
