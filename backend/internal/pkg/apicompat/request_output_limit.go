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
		paths = []string{"max_completion_tokens", "max_tokens", "max_output_tokens"}
	case "responses":
		paths = []string{"max_output_tokens"}
	case "anthropic", "messages":
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
	for _, path := range paths {
		fields := strings.Split(path, ".")
		selected := root[fields[0]]
		if len(fields) > 1 {
			var parent map[string]json.RawMessage
			if json.Unmarshal(selected, &parent) != nil {
				continue
			}
			selected = parent[fields[1]]
		}
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
