package apicompat

// AnthropicToResponsesResponseWithBridgeOptions opts an API-key bridge into the
// revised response contract. The original wrapper remains available to other routes.
func AnthropicToResponsesResponseWithBridgeOptions(resp *AnthropicResponse, opts ConversionOptions) (*ResponsesResponse, error) {
	out := AnthropicToResponsesResponse(resp)
	if bridgeEnabled(opts.Bridge) {
		var err error
		out.Status, out.IncompleteDetails, err = bridgeResponsesTerminal("anthropic", AnthropicStopReasonString(resp.StopReason), bridgeAnthropicHasTools(resp.Content), false)
		if err != nil {
			return nil, err
		}
		for i := range out.Output {
			if out.Output[i].Type == "reasoning" {
				out.Output[i].ID = generateReasoningItemID()
			}
		}
	}
	return out, nil
}

func AnthropicEventToResponsesEventsWithBridgeOptions(event *AnthropicStreamEvent, state *AnthropicEventToResponsesState, opts ConversionOptions) []ResponsesStreamEvent {
	if state == nil || event == nil {
		return nil
	}
	if !state.CreatedSent {
		state.Bridge = opts.Bridge
	}
	return AnthropicEventToResponsesEvents(event, state)
}

func FinalizeAnthropicResponsesStreamWithBridgeOptions(state *AnthropicEventToResponsesState, opts ConversionOptions) []ResponsesStreamEvent {
	if state == nil {
		return nil
	}
	if !state.CreatedSent {
		state.Bridge = opts.Bridge
	}
	if bridgeEnabled(state.Bridge) && !state.CompletedSent && state.StopReason == "" {
		return closeCurrentResponsesItem(state)
	}
	return FinalizeAnthropicResponsesStream(state)
}

func (state *AnthropicEventToResponsesState) Err() error {
	if state == nil {
		return nil
	}
	return state.bridgeErr
}

func appendAnthropicReasoningSummary(state *AnthropicEventToResponsesState, text string) []ResponsesStreamEvent {
	if text == "" {
		return nil
	}
	var events []ResponsesStreamEvent
	if !state.bridgeSummaryOpen {
		state.bridgeSummaryOpen = true
		events = append(events, makeResponsesEvent(state, "response.reasoning_summary_part.added", &ResponsesStreamEvent{ItemID: state.CurrentItemID, OutputIndex: state.OutputIndex, SummaryIndex: 0, Part: &ResponsesContentPart{Type: "summary_text"}}))
	}
	state.CurrentSummary += text
	return append(events, makeResponsesEvent(state, "response.reasoning_summary_text.delta", &ResponsesStreamEvent{ItemID: state.CurrentItemID, OutputIndex: state.OutputIndex, SummaryIndex: 0, Delta: text}))
}

func closeAnthropicReasoningSummary(state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if !state.bridgeSummaryOpen {
		return nil
	}
	state.bridgeSummaryOpen = false
	return []ResponsesStreamEvent{
		makeResponsesEvent(state, "response.reasoning_summary_text.done", &ResponsesStreamEvent{ItemID: state.CurrentItemID, OutputIndex: state.OutputIndex, SummaryIndex: 0, Text: state.CurrentSummary}),
		makeResponsesEvent(state, "response.reasoning_summary_part.done", &ResponsesStreamEvent{ItemID: state.CurrentItemID, OutputIndex: state.OutputIndex, SummaryIndex: 0, Part: &ResponsesContentPart{Type: "summary_text", Text: state.CurrentSummary}}),
	}
}
