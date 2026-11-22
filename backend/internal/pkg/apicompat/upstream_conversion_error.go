package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// UpstreamConversionError describes a generated value that cannot be projected.
type UpstreamConversionError struct{ Field, Reason, SourceProtocol, SourceReason, IdentityConflict string }

func (e *UpstreamConversionError) Error() string {
	return fmt.Sprintf("upstream conversion failed at %s: %s", e.Field, e.Reason)
}

// Diagnostic returns bounded labels only. Field/Reason can contain generated
// values in older converters; never expose them (or SourceReason) directly.
func (e *UpstreamConversionError) Diagnostic() UpstreamConversionError {
	d := UpstreamConversionError{SourceProtocol: "unknown", Field: "response", Reason: "invalid_response"}
	switch e.SourceProtocol {
	case "responses", "chat", "anthropic", "gemini":
		d.SourceProtocol = e.SourceProtocol
	}
	switch e.Field {
	case "response", "output", "output_index", "content", "content_index", "summary_index", "item_id", "arguments", "reasoning", "status", "stream", "finish_reason", "stop_reason", "encrypted_function_args", "tool_calls":
		d.Field = e.Field
	case "response.error", "choices.index", "choices.message.content", "choices.delta.content", "tool_calls.index", "response.output", "response.status", "response.output.content.text", "delta":
		d.Field = strings.ReplaceAll(e.Field, ".", "_")
	default:
		// These two indexed fields are emitted by validateResponsesChatTools.
		if strings.HasPrefix(e.Field, "output[") && strings.HasSuffix(e.Field, "].arguments") {
			d.Field = "output_arguments"
		} else if strings.HasPrefix(e.Field, "output[") && strings.HasSuffix(e.Field, "].status") {
			d.Field = "output_status"
		} else if e.Field != "" {
			d.Field = "other"
		}
	}
	switch e.Reason {
	case "ambiguous output identity", "conflicting output identity", "conflicting output index", "ambiguous unindexed output", "invalid content index", "invalid summary index", "conflicting content part types", "ambiguous unindexed content part":
		d.Reason = strings.ReplaceAll(e.Reason, " ", "_")
	case "ambiguous tool identity", "conflicting tool identity", "unknown tool identity", "ambiguous tool index", "conflicting tool index", "tool identities disagree", "announced tool identity changed":
		d.Reason = "tool_identity_conflict"
	case "expected complete JSON", "expected a JSON object", "expected complete tool arguments", "tool_search requires a complete JSON object":
		d.Reason = "invalid_tool_arguments"
	case "tool call is not complete", "tool arguments ended without completion", "tool input has no complete finish", "no open tool call":
		d.Reason = "incomplete_tool_call"
	case "tool snapshot conflicts with delivered prefix", "final tool arguments disagree with delivered input":
		d.Reason = "conflicting_tool_arguments"
	case "reasoning carrier conflicts with delivered text", "reasoning snapshot conflicts with delivered prefix", "selected reasoning text disagrees with delivered text", "reasoning completion disagrees with its deltas", "summary completion disagrees with its deltas":
		d.Reason = "conflicting_reasoning"
	case "terminal reason has no equivalent", "unsupported terminal status":
		d.Reason = "unsupported_terminal"
	case "missing response":
		d.Reason = "missing_response"
	case "missing authenticated terminal", "missing Anthropic terminal event":
		d.Reason = "missing_terminal"
	case "upstream response failed":
		d.Reason = "upstream_response_failed"
	case "invalid upstream business event", "invalid or failed Chat Completions stream frame", "invalid Chat Completions chunk":
		d.Reason = "invalid_event"
	case "unsupported tool kind", "encrypted arguments have no Messages tool input representation":
		d.Reason = "unsupported_tool"
	case "only choice zero is supported":
		d.Reason = "unsupported_choice_index"
	default:
		if e.Field == "choices.index" {
			d.Reason = "unsupported_choice_index"
		}
	}
	switch d.Reason {
	case "conflicting_output_identity", "ambiguous_output_identity", "conflicting_output_index", "ambiguous_unindexed_output", "tool_identity_conflict":
		d.IdentityConflict = "unspecified"
		switch e.IdentityConflict {
		case "item_id_call_id", "item_id_type", "item_id_name", "call_id_item_id", "call_id_type", "call_id_name", "output_index_item_id", "output_index_call_id", "output_index_type", "output_index_name", "item_id_call_id_multiple_items", "item_id_output_index", "call_id_output_index", "unindexed_multiple_items":
			d.IdentityConflict = e.IdentityConflict
		}
	}
	return d
}

func ValidateToolArguments(kind, raw string, requireObject, opaque bool) error {
	invalid := func(reason string) error { return &UpstreamConversionError{Field: "arguments", Reason: reason} }
	switch kind {
	case "custom":
		return nil
	case "function":
	default:
		return invalid("unsupported tool kind")
	}
	if opaque {
		return nil
	}
	if !json.Valid([]byte(raw)) {
		return invalid("expected complete JSON")
	}
	if requireObject && !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return invalid("expected a JSON object")
	}
	return nil
}
