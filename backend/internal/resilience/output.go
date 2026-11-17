package resilience

import (
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// OutputClass describes model output, independently of transport commitment or
// terminal success. First-event timing retains the usage log's configured TTFT
// policy; Valid and Answer are the quality clocks. Unknown never means output.
type OutputClass struct {
	Known  bool
	Valid  bool
	Answer bool
}

// ClassifyOutput is the single content rule for attempt timing and request-level
// semantic delivery. Transport commitment may retain legacy exceptions. Strings
// must contain a nonblank, visible code point; format-only frames are preludes.
// An empty protocol infers the protocol from a complete upstream JSON event.
func ClassifyOutput(protocol, event string, payload []byte) OutputClass {
	if strings.TrimSpace(string(payload)) == "[DONE]" {
		return OutputClass{Known: true}
	}
	if !gjson.ValidBytes(payload) {
		return OutputClass{}
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() || !outputHasUniqueFields(root, 0) {
		return OutputClass{}
	}
	typ := root.Get("type").String()
	if typ == "" {
		typ = event
	} else if event != "" && typ != event && !(event == "response.metadata" && typ == "codex.response.metadata") {
		return OutputClass{}
	}
	if typ == "error" || typ == "ping" || typ == "keepalive" || ChatErrorField(root) {
		return OutputClass{Known: true}
	}
	if protocol == "" {
		switch {
		case strings.HasPrefix(typ, "response.") || strings.HasPrefix(typ, "codex.") || typ == "response" || root.Get("output").IsArray():
			protocol = "responses"
		case root.Get("choices").IsArray():
			protocol = "chat"
		case strings.HasPrefix(typ, "message") || strings.HasPrefix(typ, "content_block") || root.Get("content").IsArray():
			protocol = "messages"
		}
	}
	switch protocol {
	case "responses", "websocket":
		return classifyResponsesOutput(typ, root)
	case "chat":
		out := OutputClass{Known: root.Get("choices").IsArray()}
		for _, choice := range root.Get("choices").Array() {
			for _, field := range []string{"delta", "message"} {
				msg := choice.Get(field)
				out.merge(outputText(msg.Get("content"), true))
				for _, key := range []string{"refusal", "reasoning_content", "reasoning"} {
					out.merge(outputText(msg.Get(key), false))
				}
				out.merge(outputTool(msg.Get("function_call")))
				for _, call := range msg.Get("tool_calls").Array() {
					out.merge(outputTool(call.Get("function")))
				}
			}
			out.merge(outputText(choice.Get("text"), true))
		}
		return out
	case "messages", "anthropic":
		switch typ {
		case "message_start", "message_delta", "message_stop", "content_block_stop":
			return OutputClass{Known: true}
		case "content_block_start":
			return outputBlock(root.Get("content_block"))
		case "content_block_delta":
			delta := root.Get("delta")
			switch delta.Get("type").String() {
			case "text_delta":
				return outputText(delta.Get("text"), true)
			case "thinking_delta":
				return outputText(delta.Get("thinking"), false)
			case "input_json_delta":
				return outputText(delta.Get("partial_json"), false)
			case "signature_delta":
				return OutputClass{Known: true}
			}
		case "message", "":
			return outputBlocks(root.Get("content"))
		}
	}
	return OutputClass{}
}

// Duplicate keys are ambiguous to clients and gjson. Reuse the transport's
// object check at every level and leave ambiguous events to its legacy policy.
func outputHasUniqueFields(value gjson.Result, depth int) bool {
	if !value.IsObject() && !value.IsArray() {
		return true
	}
	if depth > responsesEmptyFieldMaxDepth || value.IsObject() && !responsesObjectHasUniqueFields(value) {
		return false
	}
	unique := true
	value.ForEach(func(_, child gjson.Result) bool { unique = outputHasUniqueFields(child, depth+1); return unique })
	return unique
}

func classifyResponsesOutput(typ string, root gjson.Result) OutputClass {
	switch typ {
	case "response.output_text.delta":
		return outputText(root.Get("delta"), true)
	case "response.output_text.done":
		return outputText(root.Get("text"), true)
	case "response.refusal.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.mcp_call_arguments.delta",
		"response.code_interpreter_call_code.delta", "response.apply_patch_call_input.delta", "response.shell_call_command.delta", "response.shell_call_output_content.delta":
		return outputText(root.Get("delta"), false)
	case "response.refusal.done":
		return outputText(root.Get("refusal"), false)
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		return outputText(root.Get("text"), false)
	case "response.code_interpreter_call_code.done":
		return outputText(root.Get("code"), false)
	case "response.image_generation_call.completed":
		out := outputText(root.Get("result"), false)
		out.Valid = out.Valid || nonemptyOutputString(root.Get("b64_json"))
		return out
	case "response.web_search_call.completed":
		action := root.Get("action")
		return OutputClass{Known: true, Valid: nonemptyOutputString(action.Get("query")) || nonemptyOutputString(action.Get("url")) || nonemptyOutputString(action.Get("pattern"))}
	case "response.image_generation_call.partial_image":
		return outputText(root.Get("partial_image_b64"), false)
	case "response.function_call_arguments.done", "response.mcp_call_arguments.done":
		return outputText(root.Get("arguments"), false)
	case "response.custom_tool_call_input.done":
		return outputText(root.Get("input"), false)
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return outputBlock(root.Get("part"))
	case "response.output_item.added", "response.output_item.done":
		return outputItem(root.Get("item"))
	case "response.completed", "response.done", "response.incomplete":
		return outputItems(root.Get("response.output"))
	case "response":
		return outputItems(root.Get("output"))
	default:
		if typ == "" && root.Get("output").IsArray() {
			return outputItems(root.Get("output"))
		}
		return OutputClass{Known: isKnownResponsesEventType(typ)}
	}
}

func (o *OutputClass) merge(other OutputClass) {
	// A partially understood event is unknown as a whole, including when a
	// recognized sibling contains output. Transport applies its legacy policy.
	if !o.Known || !other.Known {
		*o = OutputClass{}
		return
	}
	o.Valid = o.Valid || other.Valid
	o.Answer = o.Answer || other.Answer
}

func outputText(value gjson.Result, answer bool) OutputClass {
	if value.IsArray() {
		out := outputBlocks(value)
		if !answer {
			out.Answer = false
		}
		return out
	}
	valid := value.Type == gjson.String && hasOutputCodePoint(value.Str)
	return OutputClass{Known: true, Valid: valid, Answer: valid && answer}
}

func outputTool(tool gjson.Result) OutputClass {
	return OutputClass{Known: true, Valid: nonemptyOutputString(tool.Get("name")) || nonemptyOutputString(tool.Get("arguments"))}
}

func hasOutputCodePoint(text string) bool {
	for _, r := range text {
		if !unicode.IsSpace(r) && !unicode.Is(unicode.Cf, r) && !unicode.IsControl(r) &&
			!unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) && !unicode.Is(unicode.Variation_Selector, r) {
			return true
		}
	}
	return false
}

func nonemptyOutputString(value gjson.Result) bool {
	return value.Type == gjson.String && hasOutputCodePoint(value.Str)
}

func outputItems(items gjson.Result) OutputClass {
	out := OutputClass{Known: true}
	for _, item := range items.Array() {
		out.merge(outputItem(item))
	}
	return out
}

func outputItem(item gjson.Result) OutputClass {
	switch item.Get("type").String() {
	case "message":
		out := outputBlocks(item.Get("content"))
		if !out.Valid && !responsesObjectHasOnlyFields(item, "id", "type", "status", "role", "index", "phase", "content") {
			out.Known = false
		}
		return out
	case "reasoning":
		out := outputBlocks(item.Get("summary"))
		out.merge(outputBlocks(item.Get("content")))
		out.merge(outputText(item.Get("encrypted_content"), false))
		out.Answer = false
		return out
	case "tool_call":
		out := OutputClass{Known: true}
		for _, call := range item.Get("tool_calls").Array() {
			out.merge(outputTool(call.Get("function")))
		}
		return out
	case "function_call", "custom_tool_call":
		out := outputTool(item)
		out.Valid = out.Valid || nonemptyOutputString(item.Get("input"))
		return out
	case "mcp_call", "mcp_approval_request":
		return outputTool(item)
	case "web_search_call", "file_search_call", "code_interpreter_call", "image_generation_call", "local_shell_call", "shell_call", "apply_patch_call", "computer_call":
		return OutputClass{Known: true, Valid: true}
	case "compaction":
		return outputText(item.Get("encrypted_content"), false)
	}
	return OutputClass{}
}

func outputBlocks(blocks gjson.Result) OutputClass {
	out := OutputClass{Known: true}
	for _, block := range blocks.Array() {
		out.merge(outputBlock(block))
	}
	return out
}

func outputBlock(block gjson.Result) OutputClass {
	switch block.Get("type").String() {
	case "text", "output_text":
		return outputText(block.Get("text"), true)
	case "refusal":
		return outputText(block.Get("refusal"), false)
	case "thinking":
		return outputText(block.Get("thinking"), false)
	case "reasoning_text", "summary_text":
		return outputText(block.Get("text"), false)
	case "redacted_thinking":
		return outputText(block.Get("data"), false)
	case "tool_use", "server_tool_use":
		input := block.Get("input")
		return OutputClass{Known: true, Valid: nonemptyOutputString(block.Get("name")) || nonemptyOutputString(input) || (input.IsObject() && len(input.Map()) > 0)}
	}
	return OutputClass{}
}
