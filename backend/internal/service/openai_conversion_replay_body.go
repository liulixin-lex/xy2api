package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// This narrower gate belongs only to identity-conflict replay. It must never
// replace or widen requestBodyReplaySafe's transport-read retry policy.
func conversionReplayBodyReason(body []byte) string {
	if !gjson.ValidBytes(body) {
		return "unrecognized_input"
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() || root.Get("model").Type != gjson.String || strings.TrimSpace(root.Get("model").Str) == "" {
		return "unrecognized_input"
	}
	if reason := conversionReplayStateReason(root); reason != "" {
		return reason
	}
	if choice := root.Get("tool_choice"); choice.Exists() && choice.Type != gjson.Null {
		if choice.Type == gjson.String {
			if choice.Str != "auto" && choice.Str != "none" && choice.Str != "required" {
				return "unrecognized_tools"
			}
		} else if !choice.IsObject() || choice.Get("type").String() != "function" || choice.Get("name").Type != gjson.String || strings.TrimSpace(choice.Get("name").Str) == "" {
			return "unrecognized_tools"
		}
	}
	if tools := root.Get("tools"); tools.Exists() && tools.Type != gjson.Null {
		if !tools.IsArray() {
			return "unrecognized_tools"
		}
		for _, tool := range tools.Array() {
			if !tool.IsObject() || tool.Get("type").Type != gjson.String {
				return "unrecognized_tools"
			}
			switch tool.Get("type").Str {
			case "function":
			case "custom":
				return "custom_tools"
			case "web_search", "web_search_preview", "code_interpreter", "mcp", "file_search", "computer", "computer_use_preview", "image_generation", "shell", "apply_patch", "tool_search":
				return "provider_tools"
			default:
				return "unrecognized_tools"
			}
			if tool.Get("name").Type != gjson.String || strings.TrimSpace(tool.Get("name").Str) == "" {
				return "unrecognized_tools"
			}
			if params := tool.Get("parameters"); params.Exists() && !params.IsObject() {
				return "unrecognized_tools"
			}
			if strict := tool.Get("strict"); strict.Exists() && strict.Type != gjson.True && strict.Type != gjson.False && strict.Type != gjson.Null {
				return "unrecognized_tools"
			}
		}
	}
	input := root.Get("input")
	if input.Type == gjson.String {
		return ""
	}
	if !input.IsArray() {
		return "unrecognized_input"
	}
	for _, item := range input.Array() {
		if !item.IsObject() {
			return "unrecognized_input"
		}
		switch item.Get("type").String() {
		case "", "message":
			switch item.Get("role").String() {
			case "user", "assistant", "system", "developer":
			default:
				return "unrecognized_input"
			}
			content := item.Get("content")
			if content.Type == gjson.String {
				continue
			}
			if !content.IsArray() {
				return "unrecognized_input"
			}
			for _, part := range content.Array() {
				if !part.IsObject() {
					return "unrecognized_input"
				}
				switch part.Get("type").String() {
				case "input_text", "output_text", "input_image", "input_file", "refusal":
				default:
					return "unrecognized_input"
				}
			}
		case "function_call":
			if item.Get("name").Type != gjson.String || strings.TrimSpace(item.Get("name").Str) == "" || item.Get("call_id").Type != gjson.String || item.Get("call_id").Str == "" || item.Get("arguments").Type != gjson.String {
				return "unrecognized_input"
			}
		case "function_call_output":
			if item.Get("call_id").Type != gjson.String || item.Get("call_id").Str == "" || item.Get("output").Type != gjson.String {
				return "unrecognized_input"
			}
		case "reasoning":
			if !item.Get("summary").IsArray() {
				return "unrecognized_input"
			}
		default:
			return "unrecognized_input"
		}
	}
	return ""
}

func conversionReplayStateReason(root gjson.Result) string {
	for _, field := range []string{"previous_response_id", "conversation", "conversation_id", "thread_id", "continuation", "handoff", "protected_handoff", "context_management", "encrypted_content"} {
		value := root.Get(field)
		if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || value.Str != "") {
			return "state_continuation"
		}
	}
	for _, field := range []string{"input", "messages", "content"} {
		for _, item := range root.Get(field).Array() {
			if item.Get("type").String() == "item_reference" || item.Get("type").String() == "compaction" || item.Get("type").String() == "handoff" {
				return "state_continuation"
			}
			if item.IsObject() {
				if reason := conversionReplayStateReason(item); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
}
