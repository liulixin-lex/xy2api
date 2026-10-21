package resilience

import (
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

// ClassifyStreamEvent classifies a complete SSE payload without doing I/O.
func ClassifyStreamEvent(protocol, eventType string, payload []byte) EventClass {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "responses" && protocol != "chat" && protocol != "messages" {
		return EventClass{Kind: Unknown}
	}
	if protocol == "chat" && strings.TrimSpace(string(payload)) == "[DONE]" {
		return EventClass{Kind: TerminalSuccess}
	}
	if !gjson.ValidBytes(payload) {
		return EventClass{Kind: Unknown}
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
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
	if typ == "response.failed" || typ == "error" || (protocol == "chat" && root.Get("error").Exists() && root.Get("error").Type != gjson.Null) {
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
		result := ClassifyWithSettings(Outcome{Source: SourceStreamEvent, StatusCode: status,
			Code: err.Get("code").String(), Type: err.Get("type").String(),
			Message: err.Get("message").String(), Body: payload}, Current())
		return EventClass{Kind: TerminalFailure, Retryable: result.Category.SwitchAccount()}
	}
	switch protocol {
	case "responses":
		switch typ {
		case "response.created", "response.in_progress", "response.queued", "keepalive":
			return EventClass{Kind: Prelude}
		case "response.completed", "response.done":
			return EventClass{Kind: TerminalSuccess}
		case "response.output_item.added", "response.content_part.added", "response.reasoning_summary_part.added":
			return EventClass{Kind: classifyResponsesAdded(root, typ)}
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
	return EventClass{Kind: Unknown}
}

func eventContentUnlessEmpty(value string) EventClass {
	if value == "" {
		return EventClass{Kind: Prelude}
	}
	return EventClass{Kind: Content}
}

// classifyResponsesAdded preserves the native first-output boundary: empty
// structures can be replayed, encrypted reasoning and tool arguments cannot.
func classifyResponsesAdded(root gjson.Result, typ string) EventKind {
	if typ == "response.output_item.added" {
		item := root.Get("item")
		if !item.IsObject() {
			return Unknown
		}
		if !emptyPreludeFields(item, true) {
			return Content
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning":
			if item.Get("encrypted_content").String() != "" {
				return Content
			}
			for _, part := range item.Get("summary").Array() {
				if strings.TrimSpace(part.Get("type").String()) != "summary_text" || part.Get("text").String() != "" {
					return Content
				}
			}
			return Prelude
		case "message":
			for _, part := range item.Get("content").Array() {
				switch strings.TrimSpace(part.Get("type").String()) {
				case "output_text":
					if part.Get("text").String() != "" {
						return Content
					}
				case "refusal":
					if part.Get("refusal").String() != "" {
						return Content
					}
				default:
					return Content
				}
			}
			return Prelude
		case "function_call":
			return eventContentUnlessEmpty(item.Get("arguments").String()).Kind
		case "custom_tool_call":
			return eventContentUnlessEmpty(item.Get("input").String()).Kind
		case "compaction":
			return eventContentUnlessEmpty(item.Get("encrypted_content").String()).Kind
		default:
			return Unknown
		}
	}
	part := root.Get("part")
	if !part.IsObject() {
		return Unknown
	}
	if !emptyPreludeFields(part, true) {
		return Content
	}
	partType := strings.TrimSpace(part.Get("type").String())
	if typ == "response.reasoning_summary_part.added" {
		if partType == "summary_text" {
			return eventContentUnlessEmpty(part.Get("text").String()).Kind
		}
		return Unknown
	}
	switch partType {
	case "output_text":
		return eventContentUnlessEmpty(part.Get("text").String()).Kind
	case "refusal":
		return eventContentUnlessEmpty(part.Get("refusal").String()).Kind
	}
	return Unknown
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

// emptyPreludeFields only ignores known structural fields. Unknown nonempty
// payloads, including nested annotations, must commit the attempt.
func emptyPreludeFields(value gjson.Result, structural bool) bool {
	switch {
	case value.Type == gjson.Null:
		return true
	case value.Type == gjson.String:
		return value.String() == ""
	case value.IsArray():
		for _, entry := range value.Array() {
			if !emptyPreludeFields(entry, structural) {
				return false
			}
		}
		return true
	case value.IsObject():
		empty := true
		value.ForEach(func(key, child gjson.Result) bool {
			if structural {
				switch key.String() {
				case "id", "type", "status", "role", "index", "phase":
					return true
				case "call_id", "name":
					if typ := value.Get("type").String(); typ == "function_call" || typ == "custom_tool_call" {
						return true
					}
				}
			}
			nestedStructure := structural && (key.String() == "content" || key.String() == "summary")
			if !emptyPreludeFields(child, nestedStructure) {
				empty = false
				return false
			}
			return true
		})
		return empty
	default:
		return false
	}
}
