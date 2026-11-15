package apicompat

import "strings"

type responsesTrackedTool struct {
	Item        ResponsesOutput
	Index       int
	OutputIndex int
	IndexKnown  bool
	Sent        string
	Closed      bool
}

// ResponsesToolCallTracker owns Chat-local identities and delivered prefixes.
type ResponsesToolCallTracker struct{ calls []*responsesTrackedTool }

func toolTrackingError(field, reason string) error {
	return &UpstreamConversionError{Field: field, Reason: reason, SourceProtocol: "responses"}
}

func (t *ResponsesToolCallTracker) find(itemID, callID string, index int, known bool) (*responsesTrackedTool, error) {
	var found *responsesTrackedTool
	for _, call := range t.calls {
		if (itemID != "" && call.Item.ID == itemID) || (callID != "" && call.Item.CallID == callID) {
			if found != nil && found != call {
				return nil, toolTrackingError("output", "ambiguous tool identity")
			}
			found = call
		}
	}
	if found != nil {
		return found, nil
	}
	if known {
		for _, call := range t.calls {
			if call.IndexKnown && call.OutputIndex == index {
				if found != nil {
					return nil, toolTrackingError("output_index", "ambiguous tool index")
				}
				found = call
			}
		}
	}
	return found, nil
}

func (t *ResponsesToolCallTracker) Register(item ResponsesOutput, index int, known, terminal bool) (*responsesTrackedTool, bool, error) {
	call, err := t.find(item.ID, item.CallID, index, known)
	if err != nil {
		return nil, false, err
	}
	if call != nil {
		if bufferedIdentityConflict(call.Item, item) || (call.Item.Name != "" && item.Name != "" && call.Item.Name != item.Name) {
			return nil, false, toolTrackingError("output", "conflicting tool identity")
		}
		if !terminal && known && call.IndexKnown && call.OutputIndex != index {
			return nil, false, toolTrackingError("output_index", "conflicting tool index")
		}
		call.Item = mergeBufferedItem(call.Item, item)
		if known && !call.IndexKnown {
			call.OutputIndex = index
			call.IndexKnown = true
		}
		return call, false, nil
	}
	call = &responsesTrackedTool{Item: item, Index: len(t.calls), OutputIndex: index, IndexKnown: known}
	t.calls = append(t.calls, call)
	return call, true, nil
}

func (t *ResponsesToolCallTracker) Resolve(event *ResponsesStreamEvent) (*responsesTrackedTool, error) {
	call, err := t.find(event.ItemID, event.CallID, event.OutputIndex, event.OutputIndexPresent || event.OutputIndex != 0)
	if err != nil {
		return nil, err
	}
	if call != nil {
		if (event.ItemID != "" && call.Item.ID != "" && event.ItemID != call.Item.ID) || (event.CallID != "" && call.Item.CallID != "" && event.CallID != call.Item.CallID) {
			return nil, toolTrackingError("output", "conflicting tool identity")
		}
		return call, nil
	}
	if event.ItemID != "" || event.CallID != "" || event.OutputIndexPresent || event.OutputIndex != 0 {
		return nil, toolTrackingError("output", "unknown tool identity")
	}
	for _, candidate := range t.calls {
		if !candidate.Closed {
			if call != nil {
				return nil, toolTrackingError("output", "ambiguous tool identity")
			}
			call = candidate
		}
	}
	if call == nil {
		return nil, toolTrackingError("output", "no open tool call")
	}
	return call, nil
}

func (t *ResponsesToolCallTracker) Snapshot(call *responsesTrackedTool, value string, complete bool) (string, error) {
	if !complete && strings.HasPrefix(call.Sent, value) {
		return "", nil
	}
	if !strings.HasPrefix(value, call.Sent) {
		return "", toolTrackingError("arguments", "tool snapshot conflicts with delivered prefix")
	}
	suffix := value[len(call.Sent):]
	call.Sent = value
	if call.Item.Type == "custom_tool_call" {
		call.Item.Input = value
	} else {
		call.Item.Arguments = value
	}
	if complete {
		call.Closed = true
	}
	return suffix, nil
}
