package apicompat

// AnthropicToResponsesResponseWithBridgeOptions opts an API-key bridge into the
// revised response contract. The original wrapper remains available to other routes.
func AnthropicToResponsesResponseWithBridgeOptions(resp *AnthropicResponse, opts ConversionOptions) (*ResponsesResponse, error) {
	out := AnthropicToResponsesResponse(resp)
	if bridgeEnabled(opts.Bridge) {
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
	return FinalizeAnthropicResponsesStream(state)
}

func (state *AnthropicEventToResponsesState) Err() error {
	if state == nil {
		return nil
	}
	return state.bridgeErr
}
