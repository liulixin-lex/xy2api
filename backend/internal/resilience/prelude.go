package resilience

import (
	"math"
	"strings"

	"github.com/tidwall/gjson"
)

// EventKind separates replayable opening events from content and terminals.
type EventKind uint8

const (
	Unknown EventKind = iota
	Prelude
	Content
	TerminalSuccess
	TerminalFailure
)

// EventClass describes one complete event. Retryable is only meaningful for
// TerminalFailure; callers must still check cancellation and committed output.
type EventClass struct {
	Kind      EventKind
	Retryable bool
}

// ResponsesOutputDisposition is the payload-local decision used by Responses
// opening, visibility, and first-token checks. Matched distinguishes the
// handled event shapes from events governed by the existing protocol rules.
type ResponsesOutputDisposition struct {
	Matched          bool
	Kind             EventKind
	Visible          bool
	SemanticOutput   bool
	BlankPlaceholder bool
}

// ResponsesEventOutputDisposition applies the same per-event rule everywhere.
// Unknown events with unambiguous metadata stay staged. Outside response.* and
// codex.*, strings are also metadata because clients ignore those event types.
func ResponsesEventOutputDisposition(eventType string, payload []byte) ResponsesOutputDisposition {
	typ := strings.TrimSpace(eventType)
	declaredType := typ
	root := gjson.ParseBytes(payload)
	validObject := gjson.ValidBytes(payload) && root.IsObject()
	if validObject {
		if payloadType := strings.TrimSpace(root.Get("type").String()); payloadType != "" {
			if strings.HasPrefix(declaredType, "response.") && payloadType != declaredType &&
				(declaredType != "response.metadata" || payloadType != "codex.response.metadata") {
				return responsesVisibleDisposition()
			}
			typ = payloadType
		}
	}
	if !isKnownResponsesEventType(typ) {
		if validObject && typ != "" && (declaredType == "" || declaredType == typ) &&
			responsesUnknownEventHasOnlyMetadata(root, typ) {
			return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
		}
		return responsesVisibleDisposition()
	}
	if !strings.HasPrefix(typ, "response.") {
		switch typ {
		case "error", "keepalive", "codex.response.metadata", "codex.rate_limits":
			return ResponsesOutputDisposition{}
		default:
			return responsesVisibleDisposition()
		}
	}
	interrupted := typ == "response.incomplete" && root.Get("response.incomplete_details.reason").String() == "interrupted"
	if typ == "response.failed" || (typ == "response.incomplete" && !interrupted) {
		return ResponsesOutputDisposition{}
	}
	if validObject && !responsesObjectHasOnlyFields(root) {
		return responsesVisibleDisposition()
	}
	if typ == "response.completed" || typ == "response.done" || interrupted {
		return ResponsesOutputDisposition{
			Matched: true, Kind: TerminalSuccess, Visible: true,
			SemanticOutput: !validObject || responsesCompletedHasSemanticOutput(root),
		}
	}
	if !validObject {
		return ResponsesOutputDisposition{Matched: true, Kind: Content, Visible: true, SemanticOutput: true}
	}

	switch typ {
	case "response.created", "response.in_progress", "response.queued":
		return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
	case "response.metadata":
		if Current().NativePreludeWhitelistOn() {
			return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
		}
		return ResponsesOutputDisposition{}
	case "response.output_text.delta":
		if !responsesObjectHasOnlyFields(root, "type", "sequence_number", "item_id", "output_index", "content_index", "delta", "logprobs", "obfuscation") {
			return responsesVisibleDisposition()
		}
		return responsesTextDisposition(root.Get("delta"))
	case "response.output_text.done":
		if !responsesObjectHasOnlyFields(root, "type", "sequence_number", "item_id", "output_index", "content_index", "text", "logprobs") {
			return responsesVisibleDisposition()
		}
		return responsesTextDisposition(root.Get("text"))
	case "response.content_part.added", "response.content_part.done":
		if !responsesObjectHasOnlyFields(root, "type", "sequence_number", "item_id", "output_index", "content_index", "part") {
			return responsesVisibleDisposition()
		}
		part := root.Get("part")
		if !part.IsObject() {
			return responsesVisibleDisposition()
		}
		if strings.TrimSpace(part.Get("type").String()) != "output_text" {
			return responsesVisibleDisposition()
		}
		if !responsesObjectHasOnlyFields(part, "type", "text", "annotations", "logprobs") || !responsesAnnotationsAreEmpty(part.Get("annotations")) {
			return responsesVisibleDisposition()
		}
		return responsesTextDisposition(part.Get("text"))
	case "response.reasoning_summary_part.added":
		if !responsesObjectHasOnlyFields(root, "type", "sequence_number", "item_id", "output_index", "content_index", "part") {
			return responsesVisibleDisposition()
		}
		part := root.Get("part")
		if !part.IsObject() || strings.TrimSpace(part.Get("type").String()) != "summary_text" ||
			!responsesObjectHasOnlyFields(part, "type", "text") {
			return responsesVisibleDisposition()
		}
		text := part.Get("text")
		if text.Type != gjson.String {
			return responsesVisibleDisposition()
		}
		if text.Str == "" {
			return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
		}
		return responsesVisibleDisposition()
	case "response.output_item.added", "response.output_item.done":
		if !responsesObjectHasOnlyFields(root, "type", "sequence_number", "output_index", "item") {
			return responsesVisibleDisposition()
		}
		item := root.Get("item")
		if !item.IsObject() {
			return responsesVisibleDisposition()
		}
		// Preserve the existing empty structural preludes only for .added.
		// A non-message .done is a completed output, even with empty arguments.
		if typ == "response.output_item.done" && item.Get("type").String() != "message" {
			return responsesVisibleDisposition()
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "message":
			return responsesMessageItemDisposition(item)
		case "reasoning":
			if !responsesObjectHasOnlyFields(item, "id", "type", "status", "index", "summary", "encrypted_content") {
				return responsesVisibleDisposition()
			}
			if encrypted := item.Get("encrypted_content"); encrypted.Exists() && encrypted.Type != gjson.Null && (encrypted.Type != gjson.String || encrypted.Str != "") {
				return responsesVisibleDisposition()
			}
			summary := item.Get("summary")
			if summary.Exists() && (!summary.IsArray() || len(summary.Array()) != 0) {
				return responsesVisibleDisposition()
			}
			return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
		case "function_call":
			return responsesEmptyStringItemDisposition(item, "arguments", "id", "type", "status", "index", "call_id", "name", "arguments")
		case "custom_tool_call":
			return responsesEmptyStringItemDisposition(item, "input", "id", "type", "status", "index", "call_id", "name", "input")
		case "compaction":
			return responsesEmptyStringItemDisposition(item, "encrypted_content", "id", "type", "status", "index", "encrypted_content")
		default:
			return responsesVisibleDisposition()
		}
	default:
		return responsesVisibleDisposition()
	}
}

// responsesKnownEventTypeNames is the source of truth for events that retain
// their established Responses handling. It covers the official OpenAI
// ResponseStreamEvent union plus gateway heartbeats/errors and legacy event
// spellings already handled here. The payload marker sub2api_placeholder is
// handled by the existing known output_item classifier, not as an event type.
const responsesKnownEventTypeNames = `
response.audio.delta
response.audio.done
response.audio.transcript.delta
response.audio.transcript.done
response.audio_transcript.delta
response.audio_transcript.done
response.output_audio.delta
response.output_audio.done
response.output_audio_transcript.delta
response.output_audio_transcript.done
response.code_interpreter_call_code.delta
response.code_interpreter_call_code.done
response.code_interpreter_call.in_progress
response.code_interpreter_call.interpreting
response.code_interpreter_call.completed
response.compaction.compacting
response.completed
response.content_part.added
response.content_part.done
response.created
response.done
response.failed
response.incomplete
response.in_progress
response.queued
response.cancelled
response.canceled
response.metadata
response.output_item.added
response.output_item.done
response.output_text.annotation.added
response.output_text.delta
response.output_text.done
response.refusal.delta
response.refusal.done
response.function_call_arguments.delta
response.function_call_arguments.done
response.custom_tool_call_input.delta
response.custom_tool_call_input.done
response.reasoning_summary_part.added
response.reasoning_summary_part.done
response.reasoning_summary_text.delta
response.reasoning_summary_text.done
response.reasoning_text.delta
response.reasoning_text.done
response.file_search_call.in_progress
response.file_search_call.searching
response.file_search_call.completed
response.web_search_call.in_progress
response.web_search_call.searching
response.web_search_call.completed
response.web_search_call.failed
response.computer_call.in_progress
response.computer_call.completed
response.local_shell_call.in_progress
response.local_shell_call.completed
response.local_shell_call.failed
response.apply_patch_call.in_progress
response.apply_patch_call.completed
response.apply_patch_call.failed
response.apply_patch_call_input.delta
response.apply_patch_call_input.done
response.image_generation_call.in_progress
response.image_generation_call.generating
response.image_generation_call.completed
response.image_generation_call.partial_image
response.mcp_call_arguments.delta
response.mcp_call_arguments.done
response.mcp_call.in_progress
response.mcp_call.failed
response.mcp_call.completed
response.mcp_list_tools.in_progress
response.mcp_list_tools.failed
response.mcp_list_tools.completed
response.shell_call_command.added
response.shell_call_command.delta
response.shell_call_command.done
response.shell_call_output_content.delta
response.shell_call_output_content.done
error
keepalive
ping
codex.response.metadata
codex.rate_limits
`

var responsesKnownEventTypeSet = func() map[string]struct{} {
	known := make(map[string]struct{})
	for _, eventType := range strings.Fields(responsesKnownEventTypeNames) {
		known[eventType] = struct{}{}
	}
	return known
}()

func isKnownResponsesEventType(eventType string) bool {
	_, known := responsesKnownEventTypeSet[eventType]
	return known
}

// Validate unknown extensions separately so their recursive duplicate-key
// checks and relaxed strings cannot change known G2a/G2c event handling.
func responsesUnknownEventHasOnlyMetadata(root gjson.Result, eventType string) bool {
	if !responsesObjectHasUniqueFields(root) {
		return false
	}
	allowStrings := !strings.HasPrefix(eventType, "response.") && !strings.HasPrefix(eventType, "codex.")
	valid := true
	root.ForEach(func(key, field gjson.Result) bool {
		switch key.String() {
		case "type", "id", "response_id", "item_id":
			valid = field.Type == gjson.String
		case "sequence_number":
			valid = field.Type == gjson.Number && field.Num >= 0 && field.Num == math.Trunc(field.Num)
		default:
			valid = responsesUnknownFieldIsMetadata(field, 0, allowStrings)
		}
		return valid
	})
	return valid
}

func responsesUnknownFieldIsMetadata(value gjson.Result, depth int, allowStrings bool) bool {
	switch value.Type {
	case gjson.Null, gjson.Number, gjson.True, gjson.False:
		return true
	case gjson.String:
		return allowStrings || value.Str == ""
	}
	if !value.IsArray() && !value.IsObject() {
		return false
	}
	depth++
	if depth > responsesEmptyFieldMaxDepth || (value.IsObject() && !responsesObjectHasUniqueFields(value)) {
		return false
	}
	valid := true
	value.ForEach(func(_, child gjson.Result) bool {
		valid = responsesUnknownFieldIsMetadata(child, depth, allowStrings)
		return valid
	})
	return valid
}

func responsesTextDisposition(text gjson.Result) ResponsesOutputDisposition {
	if !text.Exists() || text.Type != gjson.String {
		return responsesVisibleDisposition()
	}
	if strings.TrimSpace(text.Str) == "" {
		return ResponsesOutputDisposition{Matched: true, Kind: Prelude, BlankPlaceholder: true}
	}
	return responsesVisibleDisposition()
}

func responsesMessageItemDisposition(item gjson.Result) ResponsesOutputDisposition {
	if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) != "message" ||
		strings.TrimSpace(item.Get("role").String()) != "assistant" {
		return responsesVisibleDisposition()
	}
	if !responsesObjectHasOnlyFields(item, "id", "type", "status", "role", "index", "phase", "content") {
		return responsesVisibleDisposition()
	}
	content := item.Get("content")
	if !content.Exists() || !content.IsArray() {
		return responsesVisibleDisposition()
	}
	for _, part := range content.Array() {
		if !part.IsObject() {
			return responsesVisibleDisposition()
		}
		switch strings.TrimSpace(part.Get("type").String()) {
		case "output_text":
			if !responsesObjectHasOnlyFields(part, "type", "text", "annotations", "logprobs") || !responsesAnnotationsAreEmpty(part.Get("annotations")) {
				return responsesVisibleDisposition()
			}
			text := part.Get("text")
			if text.Type != gjson.String || strings.TrimSpace(text.Str) != "" {
				return responsesVisibleDisposition()
			}
		case "refusal":
			return responsesVisibleDisposition()
		default:
			return responsesVisibleDisposition()
		}
	}
	return ResponsesOutputDisposition{Matched: true, Kind: Prelude, BlankPlaceholder: true}
}

func responsesEmptyStringItemDisposition(item gjson.Result, valueField string, allowed ...string) ResponsesOutputDisposition {
	if !responsesObjectHasOnlyFields(item, allowed...) {
		return responsesVisibleDisposition()
	}
	value := item.Get(valueField)
	if value.Type == gjson.String && value.Exists() && value.Str == "" {
		return ResponsesOutputDisposition{Matched: true, Kind: Prelude}
	}
	return responsesVisibleDisposition()
}

func responsesObjectHasOnlyFields(value gjson.Result, allowed ...string) bool {
	if !responsesObjectHasUniqueFields(value) {
		return false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	valid := true
	value.ForEach(func(key, field gjson.Result) bool {
		name := key.String()
		if len(allowed) > 0 {
			if _, ok := allowedSet[name]; !ok {
				if !responsesFieldValueIsEmpty(field) {
					valid = false
					return false
				}
				return true
			}
		}
		// Fields may be absent, but a supplied structural field must have its
		// wire type. Ambiguous JSON must never hide potential client output.
		switch name {
		case "type", "id", "item_id", "response_id", "status", "role", "phase", "call_id", "name", "arguments", "input", "text", "delta", "obfuscation":
			valid = field.Type == gjson.String
		case "sequence_number", "output_index", "content_index", "summary_index", "index":
			valid = field.Type == gjson.Number && field.Num >= 0 && field.Num == math.Trunc(field.Num)
		case "part", "item", "response", "metadata":
			valid = field.IsObject()
		case "content", "summary", "output":
			valid = field.IsArray()
		case "annotations", "logprobs":
			valid = field.IsArray() && len(field.Array()) == 0
		case "encrypted_content":
			valid = field.Type == gjson.String || field.Type == gjson.Null
		}
		return valid
	})
	return valid
}

const responsesEmptyFieldMaxDepth = 32

// Extra fields use the recursive empty-value rule within a bounded number of
// array/object levels, without any structural field exemptions. Scalar
// booleans and numbers are protocol metadata rather than user-visible text and
// therefore do not count as content. Non-empty strings, including whitespace,
// remain visible.
func responsesFieldValueIsEmpty(value gjson.Result) bool {
	return responsesFieldValueIsEmptyAtDepth(value, 0)
}

func responsesFieldValueIsEmptyAtDepth(value gjson.Result, depth int) bool {
	switch value.Type {
	case gjson.Null:
		return true
	case gjson.String:
		return value.Str == ""
	case gjson.Number, gjson.True, gjson.False:
		return true
	}
	if value.IsArray() || value.IsObject() {
		depth++
		if depth > responsesEmptyFieldMaxDepth {
			return false
		}
		empty := true
		value.ForEach(func(_, child gjson.Result) bool {
			empty = responsesFieldValueIsEmptyAtDepth(child, depth)
			return empty
		})
		return empty
	}
	return false
}

func responsesObjectHasUniqueFields(value gjson.Result) bool {
	if !value.IsObject() {
		return false
	}
	seen := make(map[string]struct{})
	unique := true
	value.ForEach(func(key, _ gjson.Result) bool {
		name := key.String()
		if _, duplicate := seen[name]; duplicate {
			unique = false
			return false
		}
		seen[name] = struct{}{}
		return true
	})
	return unique
}

func responsesAnnotationsAreEmpty(value gjson.Result) bool {
	return !value.Exists() || (value.IsArray() && len(value.Array()) == 0)
}

func responsesCompletedHasSemanticOutput(root gjson.Result) bool {
	response := root.Get("response")
	// Response configuration shares field names with output parts (e.g. text),
	// so only duplicate keys and output's array shape affect this projection.
	if response.Exists() && !responsesObjectHasUniqueFields(response) {
		return true
	}
	output := root.Get("response.output")
	if !output.Exists() {
		return false
	}
	if !output.IsArray() {
		return true
	}
	for _, item := range output.Array() {
		if responsesMessageItemDisposition(item).SemanticOutput {
			return true
		}
	}
	return false
}

func responsesVisibleDisposition() ResponsesOutputDisposition {
	return ResponsesOutputDisposition{Matched: true, Kind: Content, Visible: true, SemanticOutput: true}
}

// ClassifyStreamEvent classifies a complete SSE payload without doing I/O.
func ClassifyStreamEvent(protocol, eventType string, payload []byte) EventClass {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "responses" && protocol != "chat" && protocol != "messages" {
		return EventClass{Kind: Unknown}
	}
	if protocol == "chat" && strings.TrimSpace(string(payload)) == "[DONE]" {
		return EventClass{Kind: TerminalSuccess}
	}
	if protocol == "responses" {
		disposition := ResponsesEventOutputDisposition(eventType, payload)
		if disposition.Matched {
			return EventClass{Kind: disposition.Kind}
		}
	}
	if !gjson.ValidBytes(payload) {
		if protocol == "responses" {
			return EventClass{Kind: Content}
		}
		return EventClass{Kind: Unknown}
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		if protocol == "responses" {
			return EventClass{Kind: Content}
		}
		return EventClass{Kind: Unknown}
	}
	typ := strings.TrimSpace(root.Get("type").String())
	if typ == "" {
		typ = strings.TrimSpace(eventType)
	}
	// Terminal semantics precede the opening/content projection. Incomplete
	// responses (including output limits) terminate without transparent replay.
	if typ == "response.incomplete" {
		return EventClass{Kind: TerminalFailure}
	}
	if typ == "response.failed" || typ == "error" || (protocol == "chat" && ChatErrorField(root)) {
		err := root.Get("response.error")
		if !err.IsObject() {
			err = root.Get("error")
		}
		if !err.IsObject() {
			err = root
		}
		status := int(err.Get("status_code").Int())
		if status == 0 {
			status = int(root.Get("status_code").Int())
		}
		errType := err.Get("type").String()
		flatError := typ == "error" && !root.Get("response.error").IsObject() && !root.Get("error").IsObject()
		if flatError {
			// In a flat Responses error event, top-level type names the event,
			// not the error category. Do not classify it as error.type.
			errType = ""
		}
		result := ClassifyWithSettings(Outcome{Source: SourceStreamEvent, StatusCode: status,
			Code: err.Get("code").String(), Type: errType,
			Message: err.Get("message").String(), Body: payload}, Current())
		return EventClass{Kind: TerminalFailure, Retryable: result.Category.SwitchAccount()}
	}
	if protocol == "responses" && strings.HasPrefix(typ, "response.") {
		if typ == "response.metadata" {
			return EventClass{Kind: Unknown}
		}
		// Known Responses event types not handled by the shared structural
		// projection preserve their existing visible-content fallback.
		return EventClass{Kind: Content}
	}
	switch protocol {
	case "responses":
		switch typ {
		case "response.created", "response.in_progress", "response.queued", "keepalive":
			return EventClass{Kind: Prelude}
		case "response.metadata", "codex.response.metadata", "codex.rate_limits":
			// The Codex backend sends them before any content and before a
			// capacity error (codex.rate_limits carries the plan's usage
			// windows; production saw it commit streams that then failed with
			// a replayable 503). Exact names only, and only with the native
			// whitelist on; otherwise they retain their legacy Unknown result.
			if Current().NativePreludeWhitelistOn() {
				return EventClass{Kind: Prelude}
			}
			return EventClass{Kind: Unknown}
		case "response.completed", "response.done":
			return EventClass{Kind: TerminalSuccess}
		}
		if strings.HasPrefix(typ, "response.") && (strings.HasSuffix(typ, ".delta") || strings.HasSuffix(typ, ".done")) {
			return EventClass{Kind: Content}
		}
	case "chat":
		return EventClass{Kind: classifyChatChunk(root)}
	case "messages":
		switch typ {
		case "message_start", "ping":
			return EventClass{Kind: Prelude}
		case "message_stop":
			return EventClass{Kind: TerminalSuccess}
		case "content_block_start":
			block := root.Get("content_block")
			if !block.IsObject() {
				return EventClass{Kind: Unknown}
			}
			switch block.Get("type").String() {
			case "text":
				return eventContentUnlessEmpty(block.Get("text").String())
			case "thinking":
				return eventContentUnlessEmpty(block.Get("thinking").String())
			}
			return EventClass{Kind: Content}
		case "content_block_delta", "content_block_stop", "message_delta":
			return EventClass{Kind: Content}
		}
	}
	if protocol == "responses" {
		return EventClass{Kind: Content}
	}
	return EventClass{Kind: Unknown}
}

// ChatErrorField reports whether a Chat chunk's error field carries failure
// evidence: a non-blank string, or an object with a non-empty message, code
// or type. Relays may leave placeholders such as null, "", false, 0, {} or an
// object of empty fields in normal chunks.
func ChatErrorField(root gjson.Result) bool {
	err := root.Get("error")
	if err.Type == gjson.String {
		return strings.TrimSpace(err.Str) != ""
	}
	if !err.IsObject() {
		return false
	}
	return chatErrorValue(err.Get("message")) || chatErrorValue(err.Get("code")) || chatErrorValue(err.Get("type"))
}

func chatErrorValue(value gjson.Result) bool {
	switch value.Type {
	case gjson.String:
		return strings.TrimSpace(value.Str) != ""
	case gjson.Number:
		return value.Num != 0
	}
	return false
}

func eventContentUnlessEmpty(value string) EventClass {
	if value == "" {
		return EventClass{Kind: Prelude}
	}
	return EventClass{Kind: Content}
}

func classifyChatChunk(root gjson.Result) EventKind {
	if u := root.Get("usage"); u.Exists() && u.Type != gjson.Null {
		return TerminalSuccess
	}
	choices := root.Get("choices")
	if !choices.IsArray() || len(choices.Array()) == 0 {
		return Unknown
	}
	terminal := false
	for _, ch := range choices.Array() {
		if f := ch.Get("finish_reason"); f.Exists() && f.Type != gjson.Null {
			terminal = true
		}
		delta := ch.Get("delta")
		if !delta.IsObject() {
			return Unknown
		}
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
			return Content
		}
	}
	if terminal {
		return TerminalSuccess
	}
	return Prelude
}
