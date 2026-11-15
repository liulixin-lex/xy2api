package apicompat

// ProjectTerminal projects authenticated terminal evidence between protocols.
// Readers remain responsible for proving that an otherwise empty reason ended.
func ProjectTerminal(source, target, status, reason string, hasCompleteTools, hasRefusal bool) (targetStatus, targetReason string, err error) {
	fail := func() (string, string, error) {
		return "", "", &UpstreamConversionError{Field: "finish_reason", Reason: "terminal reason has no equivalent", SourceProtocol: source, SourceReason: reason}
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
			if reason != "" {
				return fail()
			}
		case "incomplete":
			switch reason {
			case "", "max_output_tokens":
				semantic = "length"
			case "content_filter":
				semantic = "filter"
			default:
				return fail()
			}
		default:
			return fail()
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
			return fail()
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
				return "", reason, nil
			}
			return fail()
		default:
			return fail()
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
				return "", reason, nil
			}
			return fail()
		default:
			return fail()
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
				return "incomplete", "", nil
			}
			return "incomplete", "max_output_tokens", nil
		case "filter":
			return "incomplete", "content_filter", nil
		default:
			return "completed", "", nil
		}
	case "chat":
		switch semantic {
		case "length":
			return "", "length", nil
		case "filter":
			return "", "content_filter", nil
		case "refusal":
			return "", "stop", nil
		}
		if source == target && reason == "function_call" {
			return "", reason, nil
		}
		if hasCompleteTools {
			return "", "tool_calls", nil
		}
		return "", "stop", nil
	case "anthropic":
		switch semantic {
		case "length":
			if source == target && reason == "model_context_window_exceeded" {
				return "", reason, nil
			}
			return "", "max_tokens", nil
		case "filter", "refusal":
			return "", "refusal", nil
		}
		if source == target && reason == "stop_sequence" {
			return "", reason, nil
		}
		if hasCompleteTools {
			return "", "tool_use", nil
		}
		return "", "end_turn", nil
	case "gemini":
		switch semantic {
		case "length":
			return "", "MAX_TOKENS", nil
		case "filter":
			if source == target {
				return "", reason, nil
			}
			return "", "SAFETY", nil
		case "refusal":
			return "", "SAFETY", nil
		}
		return "", "STOP", nil
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
