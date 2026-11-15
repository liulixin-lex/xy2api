package apicompat

// ProjectTerminal projects authenticated terminal evidence between protocols.
// Readers remain responsible for proving that an otherwise empty reason ended.
// Call ProjectTerminalWithMapping when the caller records conversion notices.
func ProjectTerminal(source, target, status, reason string, hasCompleteTools, hasRefusal bool) (targetStatus, targetReason string, err error) {
	targetStatus, targetReason, _, err = ProjectTerminalWithMapping(source, target, status, reason, hasCompleteTools, hasRefusal)
	return targetStatus, targetReason, err
}

// ProjectTerminalWithMapping also reports whether an unlisted nonempty source
// status/reason used the existing mapper's normal-completion default. Callers
// record one map notice when unknownReasonMapped is true. Explicit table errors
// remain errors. As with ProjectTerminal, callers must authenticate termination.
func ProjectTerminalWithMapping(source, target, status, reason string, hasCompleteTools, hasRefusal bool) (targetStatus, targetReason string, unknownReasonMapped bool, err error) {
	fail := func() (string, string, bool, error) {
		return "", "", false, &UpstreamConversionError{Field: "finish_reason", Reason: "terminal reason has no equivalent", SourceProtocol: source, SourceReason: reason}
	}
	validProtocol := func(p string) bool {
		switch p {
		case "responses", "chat", "anthropic", "gemini":
			return true
		}
		return false
	}
	if !validProtocol(source) || !validProtocol(target) {
		return fail()
	}
	semantic := "complete"
	toolReason := false
	switch source {
	case "responses":
		switch status {
		case "completed":
			unknownReasonMapped = reason != ""
		case "incomplete":
			switch reason {
			case "", "max_output_tokens":
				semantic = "length"
			case "content_filter":
				semantic = "filter"
			default:
				unknownReasonMapped = true
			}
		case "", "failed", "cancelled", "canceled":
			return fail()
		default:
			unknownReasonMapped = true
		}
	case "chat":
		if status != "" {
			return fail()
		}
		switch reason {
		case "", "stop":
		case "tool_calls", "function_call":
			toolReason = true
		case "length":
			semantic = "length"
		case "content_filter":
			semantic = "filter"
		default:
			unknownReasonMapped = true
		}
	case "anthropic":
		if status != "" {
			return fail()
		}
		switch reason {
		case "", "end_turn", "stop_sequence":
		case "tool_use":
			toolReason = true
		case "max_tokens", "model_context_window_exceeded":
			semantic = "length"
		case "refusal":
			semantic = "refusal"
		case "pause_turn":
			if target == source {
				return "", reason, unknownReasonMapped, nil
			}
			return fail()
		default:
			unknownReasonMapped = true
		}
	case "gemini":
		if status != "" {
			return fail()
		}
		switch reason {
		case "", "STOP":
		case "MAX_TOKENS":
			semantic = "length"
		case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
			semantic = "filter"
		case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS":
			if target == source {
				return "", reason, unknownReasonMapped, nil
			}
			return fail()
		default:
			unknownReasonMapped = true
		}
	}
	if toolReason && !hasCompleteTools {
		return fail()
	}
	if semantic == "complete" && hasRefusal {
		semantic = "refusal"
	}
	switch target {
	case "responses":
		switch semantic {
		case "length":
			if source == "responses" && reason == "" {
				return "incomplete", "", unknownReasonMapped, nil
			}
			return "incomplete", "max_output_tokens", unknownReasonMapped, nil
		case "filter":
			return "incomplete", "content_filter", unknownReasonMapped, nil
		default:
			return "completed", "", unknownReasonMapped, nil
		}
	case "chat":
		switch semantic {
		case "length":
			return "", "length", unknownReasonMapped, nil
		case "filter":
			return "", "content_filter", unknownReasonMapped, nil
		case "refusal":
			return "", "stop", unknownReasonMapped, nil
		}
		if source == target && reason == "function_call" {
			return "", reason, unknownReasonMapped, nil
		}
		if hasCompleteTools {
			return "", "tool_calls", unknownReasonMapped, nil
		}
		return "", "stop", unknownReasonMapped, nil
	case "anthropic":
		switch semantic {
		case "length":
			if source == target && reason == "model_context_window_exceeded" {
				return "", reason, unknownReasonMapped, nil
			}
			return "", "max_tokens", unknownReasonMapped, nil
		case "filter", "refusal":
			return "", "refusal", unknownReasonMapped, nil
		}
		if source == target && reason == "stop_sequence" {
			return "", reason, unknownReasonMapped, nil
		}
		if hasCompleteTools {
			return "", "tool_use", unknownReasonMapped, nil
		}
		return "", "end_turn", unknownReasonMapped, nil
	case "gemini":
		switch semantic {
		case "length":
			return "", "MAX_TOKENS", unknownReasonMapped, nil
		case "filter":
			if source == target {
				return "", reason, unknownReasonMapped, nil
			}
			return "", "SAFETY", unknownReasonMapped, nil
		case "refusal":
			return "", "SAFETY", unknownReasonMapped, nil
		}
		return "", "STOP", unknownReasonMapped, nil
	}
	return fail()
}

func ResponsesTerminalEventType(status string) (string, error) {
	switch status {
	case "completed", "incomplete", "failed":
		return "response." + status, nil
	}
	return "", &UpstreamConversionError{Field: "status", Reason: "unsupported terminal status", SourceProtocol: "responses", SourceReason: status}
}
