package apicompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → AnthropicResponse
// ---------------------------------------------------------------------------

// ResponsesToAnthropic converts a Responses API response directly into an
// Anthropic Messages response. Reasoning output items are mapped to thinking
// blocks; function_call items become tool_use blocks.
func ResponsesToAnthropic(resp *ResponsesResponse, model string) *AnthropicResponse {
	if ValidateResponsesForAnthropic(resp) != nil {
		return nil
	}
	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	var blocks []AnthropicContentBlock

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			summaryText := ResponsesOutputReasoningText(item)
			// Always surface encrypted_content as thinking.signature so Claude
			// Code / multi-turn clients can send it back. Signature-only
			// thinking blocks are valid when the model omits a visible summary.
			if summaryText != "" || strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  summaryText,
					Signature: item.EncryptedContent,
				})
			}
		case "message":
			for _, part := range item.Content {
				text := part.Text
				if part.Type == "refusal" {
					text = part.Refusal
				}
				if (part.Type == "output_text" || part.Type == "refusal") && text != "" {
					blocks = append(blocks, AnthropicContentBlock{
						Type: "text",
						Text: text,
					})
				}
			}
		case "function_call":
			raw := item.Arguments
			if raw == "" && resp.Status == "completed" {
				raw = "{}"
			}
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, raw),
			})
		case "custom_tool_call":
			input, _ := json.Marshal(map[string]string{"input": item.Input})
			blocks = append(blocks, AnthropicContentBlock{Type: "tool_use", ID: fromResponsesCallID(item.CallID), Name: item.Name, Input: input})
		case "web_search_call":
			if item.Status != "completed" {
				continue
			}
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			inputJSON, _ := json.Marshal(map[string]string{"query": query})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "server_tool_use",
				ID:    toolUseID,
				Name:  "web_search",
				Input: inputJSON,
			})
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "web_search_tool_result",
				ToolUseID: toolUseID,
				Content:   responsesWebSearchSources(item),
			})
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	out.Content = blocks

	stopReason := responsesStatusToAnthropicStopReason(resp.Status, resp.IncompleteDetails, blocks)
	if responsesHasRefusal(resp) {
		_, stopReason, _ = ProjectTerminal("responses", "anthropic", resp.Status, responsesIncompleteReason(resp), containsAnthropicToolUseBlock(blocks), true)
	}
	out.StopReason = AnthropicStopReasonPtr(stopReason)

	if resp.Usage != nil {
		out.Usage = anthropicUsageFromResponsesUsage(resp.Usage)
	}

	return out
}

// ValidateResponsesForAnthropic checks complete generated values before projection.
// Callers must surface this error before invoking the legacy pointer-only converter.
func ValidateResponsesForAnthropic(resp *ResponsesResponse) error {
	if resp == nil {
		return &UpstreamConversionError{Field: "response", Reason: "missing response", SourceProtocol: "responses"}
	}
	if resp.Error != nil {
		return &UpstreamConversionError{Field: "response.error", Reason: "upstream response failed", SourceProtocol: "responses"}
	}
	if _, _, err := ProjectTerminal("responses", "anthropic", resp.Status, responsesIncompleteReason(resp), false, responsesHasRefusal(resp)); err != nil {
		return err
	}
	for _, item := range resp.Output {
		kind, raw := "function", item.Arguments
		switch item.Type {
		case "function_call":
			if raw == "" && resp.Status == "completed" {
				raw = "{}"
			}
		case "custom_tool_call":
			kind, raw = "custom", item.Input
		default:
			continue
		}
		if err := resToAnthValidateArguments(kind, raw, len(normalizedRawJSON(item.EncryptedFunctionArgs)) > 0); err != nil {
			return err
		}
	}
	return nil
}

func resToAnthValidateArguments(kind, raw string, opaque bool) error {
	if err := ValidateToolArguments(kind, raw, true, opaque); err != nil {
		return err
	}
	if opaque {
		return &UpstreamConversionError{Field: "encrypted_function_args", Reason: "encrypted arguments have no Messages tool input representation", SourceProtocol: "responses"}
	}
	return nil
}

func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	inputTokens := usage.InputTokens - cachedTokens - usage.CacheCreationInputTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

func responsesIncompleteReason(resp *ResponsesResponse) string {
	if resp.IncompleteDetails != nil {
		return resp.IncompleteDetails.Reason
	}
	return ""
}

func responsesHasRefusal(resp *ResponsesResponse) bool {
	for _, item := range resp.Output {
		for _, part := range item.Content {
			if part.Type == "refusal" {
				return true
			}
		}
	}
	return false
}

func responsesStatusToAnthropicStopReason(status string, details *ResponsesIncompleteDetails, blocks []AnthropicContentBlock) string {
	reason := ""
	if details != nil {
		reason = details.Reason
	}
	_, stop, _ := ProjectTerminal("responses", "anthropic", status, reason, containsAnthropicToolUseBlock(blocks), false)
	return stop
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []AnthropicStreamEvent (stateful converter)
// ---------------------------------------------------------------------------

// responsesTextPart identifies one output_text part of a streamed response.
type responsesTextPart struct {
	ItemID       string
	Kind         string
	OutputIndex  int
	ContentIndex int
}

// responsesAnthropicTool keeps identities and parameter bytes for one output item.
// Call IDs alone are not unique across items.
type responsesAnthropicTool struct {
	index                      int
	itemID, callID, name, kind string
	raw, emitted               string
	complete, opened, closed   bool
	opaque                     bool
}

type responsesAnthropicReasoning struct {
	summary, raw  map[int]string
	delivered     string
	signature     string
	signatureSent bool
	rawConflict   bool
}

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	MessageStartSent bool
	MessageStopSent  bool
	err              error
	tools            map[int]*responsesAnthropicTool
	toolItems        map[string]*responsesAnthropicTool
	toolOrder        []*responsesAnthropicTool
	activeTool       *responsesAnthropicTool
	pendingBlocks    []ResponsesStreamEvent
	successTerminal  bool
	searchesDone     map[string]bool
	itemIDs          map[int]string
	reasoning        map[string]*responsesAnthropicReasoning
	activeReasoning  *responsesAnthropicReasoning

	ContentBlockIndex   int
	ContentBlockOpen    bool
	CurrentBlockType    string // "text" | "thinking" | "tool_use"
	CurrentToolName     string
	CurrentToolArgs     string
	CurrentToolHadDelta bool
	// PendingThinkingSignature is filled from reasoning.encrypted_content and
	// emitted as signature_delta before the thinking block is closed.
	PendingThinkingSignature string
	HasToolCall              bool

	// OutputIndexToBlockIdx maps Responses output_index → Anthropic content block index.
	OutputIndexToBlockIdx map[int]int

	// textByPart records the text already delivered for each output_text part
	// so that a done payload can be reconciled against it. It outlives the
	// content block; closeCurrentBlock must not reset it.
	textByPart map[responsesTextPart]*strings.Builder
	// textDelivered records whether any assistant text reached the client.
	textDelivered bool
	hasRefusal    bool

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// NewResponsesEventToAnthropicState returns an initialised stream state.
func NewResponsesEventToAnthropicState() *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OutputIndexToBlockIdx: make(map[int]int),
		tools:                 make(map[int]*responsesAnthropicTool),
		toolItems:             make(map[string]*responsesAnthropicTool),
		textByPart:            make(map[responsesTextPart]*strings.Builder),
		Created:               time.Now().Unix(),
	}
}

// Err returns the first conversion failure; service readers own error encoding.
func (state *ResponsesEventToAnthropicState) Err() error { return state.err }

func (state *ResponsesEventToAnthropicState) fail(field, reason string) {
	if state.err == nil {
		state.err = &UpstreamConversionError{Field: field, Reason: reason, SourceProtocol: "responses"}
	}
}

func resToAnthToolOf(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) *responsesAnthropicTool {
	id, callID := evt.ItemID, evt.CallID
	if evt.Item != nil {
		if evt.Item.ID != "" {
			id = evt.Item.ID
		}
		if evt.Item.CallID != "" {
			callID = evt.Item.CallID
		}
	}
	tool := state.toolItems[id]
	indexed := state.tools[evt.OutputIndex]
	// Programmatic callers retain the old integer API; wire zero is explicit only
	// when its presence bit is set. An item ID without an index never means zero.
	hasIndex := evt.OutputIndexPresent || evt.OutputIndex != 0 || id == "" || evt.Type == "response.output_item.added"
	if tool != nil && hasIndex && tool.index != evt.OutputIndex {
		state.fail("output_index", "tool identities disagree")
		return nil
	}
	if tool == nil && hasIndex {
		tool = indexed
	}
	if tool != nil {
		if evt.Item != nil {
			if evt.Item.Type != tool.kind || (evt.Item.Name != "" && tool.name != "" && evt.Item.Name != tool.name) {
				state.fail("item_id", "tool identities disagree")
				return nil
			}
			tool.opaque = tool.opaque || len(normalizedRawJSON(evt.Item.EncryptedFunctionArgs)) > 0
		}
		if id != "" && tool.itemID != "" && id != tool.itemID || callID != "" && tool.callID != "" && callID != tool.callID {
			state.fail("item_id", "tool identities disagree")
			return nil
		}
		if id != "" && tool.itemID == "" {
			tool.itemID = id
			state.toolItems[id] = tool
		}
		return tool
	}
	if evt.Item == nil || (evt.Item.Type != "function_call" && evt.Item.Type != "custom_tool_call") {
		return nil
	}
	tool = &responsesAnthropicTool{index: evt.OutputIndex, itemID: id, callID: callID, name: evt.Item.Name, kind: evt.Item.Type,
		opaque: len(normalizedRawJSON(evt.Item.EncryptedFunctionArgs)) > 0}
	state.tools[tool.index] = tool
	if id != "" {
		state.toolItems[id] = tool
	}
	state.toolOrder = append(state.toolOrder, tool)
	return tool
}

func resToAnthToolBlocksEvent(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) bool {
	if state.activeTool == nil || state.activeTool.complete {
		return false
	}
	switch evt.Type {
	case "response.output_item.added":
		if evt.Item == nil {
			return false
		}
		if evt.Item.Type == "function_call" || evt.Item.Type == "custom_tool_call" {
			return resToAnthToolOf(evt, state) != state.activeTool
		}
		return evt.Item.Type == "reasoning"
	case "response.output_item.done":
		return evt.Item != nil && evt.Item.Type != "function_call" && evt.Item.Type != "custom_tool_call"
	case "response.output_text.delta", "response.output_text.done", "response.content_part.done",
		"response.refusal.delta", "response.refusal.done", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_text.done":
		return true
	}
	return false
}

// resToAnthDrainPendingBlocks replays only blocks delayed by an unfinished tool.
func resToAnthDrainPendingBlocks(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent
	for len(state.pendingBlocks) > 0 && state.err == nil && (state.activeTool == nil || state.activeTool.complete) {
		evt := state.pendingBlocks[0]
		state.pendingBlocks = state.pendingBlocks[1:]
		events = append(events, resToAnthDispatchEvent(&evt, state)...)
	}
	return events
}

// ResponsesEventToAnthropicEvents converts a single Responses SSE event into
// zero or more Anthropic SSE events, updating state as it goes.
func ResponsesEventToAnthropicEvents(
	evt *ResponsesStreamEvent,
	state *ResponsesEventToAnthropicState,
) []AnthropicStreamEvent {
	if state.err != nil || state.MessageStopSent {
		if state.err != nil && (evt.Type == "response.completed" || evt.Type == "response.done" || evt.Type == "response.incomplete" || evt.Type == "response.failed") {
			resToAnthRecordUsage(evt, state)
		}
		return nil
	}
	if resToAnthToolBlocksEvent(evt, state) {
		state.pendingBlocks = append(state.pendingBlocks, *evt)
		return nil
	}
	events := resToAnthDispatchEvent(evt, state)
	return append(events, resToAnthDrainPendingBlocks(state)...)
}

func resToAnthDispatchEvent(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Type == "response.output_item.added" && evt.Item != nil && evt.Item.ID != "" {
		if state.itemIDs == nil {
			state.itemIDs = make(map[int]string)
		}
		state.itemIDs[evt.OutputIndex] = evt.Item.ID
	}
	switch evt.Type {
	case "response.created":
		return resToAnthHandleCreated(evt, state)
	case "response.output_item.added":
		return resToAnthHandleOutputItemAdded(evt, state)
	case "response.output_text.delta", "response.refusal.delta":
		return resToAnthHandleTextDelta(evt, state)
	case "response.output_text.done":
		return resToAnthHandleTextDone(evt, state)
	case "response.refusal.done":
		return resToAnthHandleRefusalDone(evt, state)
	case "response.content_part.done":
		if evt.Part != nil {
			copy := *evt
			switch evt.Part.Type {
			case "refusal":
				copy.Type = "response.refusal.done"
				copy.Refusal = evt.Part.Refusal
				return resToAnthHandleRefusalDone(&copy, state)
			case "output_text":
				copy.Type = "response.output_text.done"
				copy.Text = evt.Part.Text
				return resToAnthHandleTextDone(&copy, state)
			}
		}
		return nil
	case "response.function_call_arguments.delta",
		// custom/freeform 工具的输入增量与 function_call 参数增量同形。
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量，与 reasoning summary 一样映射为 thinking。
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		return resToAnthHandleReasoningDelta(evt, state)

	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream emits synthetic termination events if the
// stream ended without a proper completion event.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.err != nil {
		return nil
	}
	if state.activeTool != nil && !state.activeTool.complete {
		state.fail("arguments", "tool arguments ended without completion")
		return nil
	}
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	stopReason := "end_turn"
	if state.HasToolCall {
		stopReason = "tool_use"
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// ResponsesAnthropicEventToSSE formats an AnthropicStreamEvent as an SSE line pair.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// --- internal handlers ---

func resToAnthHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response != nil {
		state.ResponseID = evt.Response.ID
		// Only use upstream model if no override was set (e.g. originalModel)
		if state.Model == "" {
			state.Model = evt.Response.Model
		}
	}

	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true

	// Official Anthropic message_start uses stop_reason: null and usage with
	// input_tokens when known. We leave StopReason nil (JSON null) and usage
	// zeros until response.completed; never emit stop_reason:"" which breaks
	// strict clients' turn-finalization / session usage accounting.
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         state.ResponseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      state.Model,
			StopReason: nil,
			Usage: AnthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}}
}

func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// function_call 与 custom_tool_call（custom/freeform 工具，如新版 apply_patch）
	// 同样映射为 Anthropic 的 tool_use 块。
	case "function_call", "custom_tool_call":
		tool := resToAnthToolOf(evt, state)
		if tool == nil || tool.opened {
			return nil
		}
		events := closeCurrentBlock(state)
		idx := state.ContentBlockIndex
		state.OutputIndexToBlockIdx[tool.index] = idx
		state.ContentBlockOpen = true
		state.CurrentBlockType = "tool_use"
		state.CurrentToolName = tool.name
		state.CurrentToolArgs = tool.raw
		state.CurrentToolHadDelta = false
		state.activeTool = tool
		tool.opened = true
		state.HasToolCall = true
		events = append(events, AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: fromResponsesCallID(tool.callID), Name: tool.name, Input: json.RawMessage("{}")}})
		return append(events, resToAnthEmitTool(tool, state)...)

	case "reasoning":
		r := resToAnthReasoningOf(evt, state)
		if evt.Item.EncryptedContent != "" {
			r.signature = evt.Item.EncryptedContent
		}
		if state.activeReasoning == r {
			return nil
		}
		return resToAnthOpenReasoning(r, state)

	case "message":
		return nil
	}

	return nil
}

func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	return resToAnthEmitText(evt.Delta, resToAnthTextPartOf(evt, state), state)
}

func resToAnthTextPartOf(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) responsesTextPart {
	part := responsesTextPart{OutputIndex: evt.OutputIndex, ContentIndex: evt.ContentIndex, Kind: "output_text"}
	if strings.Contains(evt.Type, "refusal") {
		part.Kind = "refusal"
		state.hasRefusal = true
	}
	part.ItemID = evt.ItemID
	if part.ItemID == "" {
		part.ItemID = state.itemIDs[evt.OutputIndex]
	}
	if part.ItemID != "" {
		part.OutputIndex = 0
	}
	return part
}

func resToAnthHandleRefusalDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	events := resToAnthRecoverText(evt.Refusal, resToAnthTextPartOf(evt, state), state)
	return append(events, resToAnthHandleBlockDone(state)...)
}

// resToAnthEmitText opens a text block when needed, emits text, and records it
// against its part so that a later payload for the same part is reconciled
// against what the client already received.
func resToAnthEmitText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if text == "" {
		return nil
	}

	var events []AnthropicStreamEvent

	if !state.ContentBlockOpen || state.CurrentBlockType != "text" {
		events = append(events, closeCurrentBlock(state)...)

		idx := state.ContentBlockIndex
		state.ContentBlockOpen = true
		state.CurrentBlockType = "text"

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type: "text",
				Text: "",
			},
		})
	}

	delivered, ok := state.textByPart[part]
	if !ok {
		delivered = &strings.Builder{}
		state.textByPart[part] = delivered
	}
	_, _ = delivered.WriteString(text)
	state.textDelivered = true

	idx := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: text,
		},
	})
	return events
}

// resToAnthRecoverText emits the tail of a finished text payload that never
// reached the client. Streamed text cannot be recalled, so a payload that does
// not extend what was already delivered is left alone.
func resToAnthRecoverText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	builder, known := state.textByPart[part]
	if !known && state.textDelivered && part.ItemID == "" && part.Kind != "refusal" {
		// The payload is indexed differently from every delta seen so far, so
		// which part it finishes cannot be established. Recovering it could
		// repeat an answer the client already has, which is worse than leaving
		// a partially delivered one alone.
		return nil
	}

	var delivered string
	if known {
		delivered = builder.String()
	}
	if text == delivered || !strings.HasPrefix(text, delivered) {
		return nil
	}
	return resToAnthEmitText(text[len(delivered):], part, state)
}

// resToAnthHandleTextDone recovers text that upstream carried only on the done
// event before closing the block, which some streams use instead of sending
// output_text.delta at all.
func resToAnthHandleTextDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		// The message is already terminated; a late payload cannot be delivered
		// without emitting a content block after message_stop.
		return resToAnthHandleBlockDone(state)
	}

	events := resToAnthRecoverText(evt.Text, resToAnthTextPartOf(evt, state), state)
	return append(events, resToAnthHandleBlockDone(state)...)
}

func resToAnthEmitTool(tool *responsesAnthropicTool, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if tool != state.activeTool || tool.closed || state.err != nil {
		return nil
	}
	raw := tool.raw
	if tool.kind == "custom_tool_call" {
		if !tool.complete {
			return nil
		}
		wrapped, _ := json.Marshal(map[string]string{"input": raw})
		raw = string(wrapped)
	}
	if tool.opaque {
		return nil
	}
	if tool.complete && raw == "" && tool.kind == "function_call" {
		raw = "{}"
	}
	if tool.name == "Read" && tool.kind == "function_call" {
		if ValidateToolArguments("function", raw, true, false) != nil {
			return nil
		}
		raw = string(sanitizeAnthropicToolUseInput(tool.name, raw))
	}
	var events []AnthropicStreamEvent
	if strings.HasPrefix(raw, tool.emitted) && len(raw) > len(tool.emitted) {
		idx := state.ContentBlockIndex
		events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: raw[len(tool.emitted):]}})
		tool.emitted = raw
		state.CurrentToolHadDelta = true
	}
	state.CurrentToolArgs = tool.raw
	if tool.complete {
		events = append(events, closeCurrentBlock(state)...)
		tool.closed = true
		state.activeTool = nil
	}
	return events
}

func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	tool := resToAnthToolOf(evt, state)
	if tool == nil || tool.closed || evt.Delta == "" {
		return nil
	}
	tool.raw += evt.Delta
	return resToAnthEmitTool(tool, state)
}

func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	tool := resToAnthToolOf(evt, state)
	if tool == nil {
		return nil
	}
	raw, present := evt.Arguments, evt.ArgumentsPresent || evt.Arguments != ""
	if tool.kind == "custom_tool_call" {
		raw, present = evt.Input, evt.InputPresent || evt.Input != ""
	}
	resToAnthCompleteTool(tool, raw, present, state)
	return resToAnthEmitTool(tool, state)
}

func resToAnthCompleteTool(tool *responsesAnthropicTool, raw string, present bool, state *ResponsesEventToAnthropicState) {
	if present {
		if (tool.complete && raw != tool.raw) || !strings.HasPrefix(raw, tool.raw) {
			state.fail("arguments", "final tool arguments disagree with delivered input")
			return
		}
		tool.raw = raw
	}
	kind := "function"
	if tool.kind == "custom_tool_call" {
		kind = "custom"
	}
	raw = tool.raw
	if kind == "function" && raw == "" && !tool.opaque {
		if !state.successTerminal {
			return
		}
		raw = "{}"
	}
	if err := resToAnthValidateArguments(kind, raw, tool.opaque); err != nil {
		if state.err == nil {
			state.err = err
		}
		return
	}
	tool.complete = true
}

func resToAnthItemKey(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) string {
	if evt.Item != nil && evt.Item.ID != "" {
		return evt.Item.ID
	}
	if evt.ItemID != "" {
		return evt.ItemID
	}
	if id := state.itemIDs[evt.OutputIndex]; id != "" {
		return id
	}
	return fmt.Sprintf("output:%d", evt.OutputIndex)
}

func resToAnthReasoningOf(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) *responsesAnthropicReasoning {
	if state.reasoning == nil {
		state.reasoning = make(map[string]*responsesAnthropicReasoning)
	}
	key := resToAnthItemKey(evt, state)
	r := state.reasoning[key]
	if r == nil {
		r = &responsesAnthropicReasoning{summary: make(map[int]string), raw: make(map[int]string)}
		state.reasoning[key] = r
	}
	return r
}

func resToAnthJoinReasoning(parts map[int]string) string {
	keys := make([]int, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var b strings.Builder
	for _, k := range keys {
		_, _ = b.WriteString(parts[k])
	}
	return b.String()
}

func resToAnthOpenReasoning(r *responsesAnthropicReasoning, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	events := closeCurrentBlock(state)
	idx := state.ContentBlockIndex
	state.ContentBlockOpen = true
	state.CurrentBlockType = "thinking"
	state.activeReasoning = r
	state.PendingThinkingSignature = r.signature
	events = append(events, AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: ""}})
	return events
}

func resToAnthEmitReasoning(r *responsesAnthropicReasoning, text string, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !strings.HasPrefix(text, r.delivered) {
		state.fail("reasoning", "selected reasoning text disagrees with delivered text")
		return nil
	}
	if text == r.delivered {
		return nil
	}
	var events []AnthropicStreamEvent
	if state.activeReasoning != r || !state.ContentBlockOpen {
		events = append(events, resToAnthOpenReasoning(r, state)...)
	}
	idx := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: text[len(r.delivered):]}})
	r.delivered = text
	return events
}

func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	r := resToAnthReasoningOf(evt, state)
	parts, index := r.raw, evt.ContentIndex
	summary := strings.HasPrefix(evt.Type, "response.reasoning_summary")
	if summary {
		parts, index = r.summary, evt.SummaryIndex
	}
	if strings.HasSuffix(evt.Type, ".done") {
		// Text has no presence bit; legacy done events may omit the snapshot.
		if evt.Text == "" {
			return nil
		}
		if !strings.HasPrefix(evt.Text, parts[index]) {
			if summary {
				state.fail("reasoning", "reasoning completion disagrees with its deltas")
			} else {
				r.rawConflict = true
			}
			return nil
		}
		parts[index] = evt.Text
	} else {
		parts[index] += evt.Delta
	}
	if summary {
		return resToAnthEmitReasoning(r, resToAnthJoinReasoning(r.summary), state)
	}
	return nil
}

func resToAnthRecoverReasoning(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	r := resToAnthReasoningOf(evt, state)
	item := evt.Item
	summary := resToAnthJoinReasoning(r.summary)
	// A supplied nonempty summary is authoritative; raw text never overrides it.
	hasSummary := false
	for _, part := range item.Summary {
		if part.Type == "summary_text" && part.Text != "" {
			hasSummary = true
		}
	}
	if hasSummary {
		final := ResponsesOutputReasoningText(*item)
		if !strings.HasPrefix(final, summary) {
			state.fail("reasoning", "summary completion disagrees with its deltas")
			return nil
		}
		summary = final
		r.summary = map[int]string{0: final}
	}
	selected := summary
	if selected == "" {
		if r.rawConflict {
			state.fail("reasoning", "reasoning completion disagrees with its deltas")
			return nil
		}
		selected = resToAnthJoinReasoning(r.raw)
		final := ResponsesOutputReasoningText(*item)
		if final != "" {
			if !strings.HasPrefix(final, selected) {
				state.fail("reasoning", "reasoning completion disagrees with its deltas")
				return nil
			}
			selected = final
			r.raw = map[int]string{0: final}
		}
	}
	events := resToAnthEmitReasoning(r, selected, state)
	if state.err != nil {
		return events
	}
	if sig := strings.TrimSpace(item.EncryptedContent); sig != "" {
		r.signature = sig
	}
	if r.signature != "" && !r.signatureSent && state.activeReasoning != r {
		events = append(events, resToAnthOpenReasoning(r, state)...)
	}
	if state.activeReasoning == r {
		if !r.signatureSent {
			state.PendingThinkingSignature = r.signature
		}
		events = append(events, closeCurrentBlock(state)...)
	}
	return events
}

func resToAnthHandleBlockDone(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	return closeCurrentBlock(state)
}

func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	if evt.Item.Type == "function_call" || evt.Item.Type == "custom_tool_call" {
		tool := resToAnthToolOf(evt, state)
		if tool == nil {
			return nil
		}
		raw, present := evt.Item.Arguments, evt.Item.Arguments != ""
		if tool.kind == "custom_tool_call" {
			raw, present = evt.Item.Input, evt.Item.Input != "" || tool.raw == ""
		}
		resToAnthCompleteTool(tool, raw, present, state)
		if state.err != nil {
			return nil
		}
		if !tool.opened {
			if state.activeTool != nil && !state.activeTool.complete {
				state.pendingBlocks = append(state.pendingBlocks, *evt)
				return nil
			}
			return resToAnthHandleOutputItemAdded(evt, state)
		}
		return resToAnthEmitTool(tool, state)
	}

	if evt.Item.Type == "message" {
		var events []AnthropicStreamEvent
		for i, part := range evt.Item.Content {
			event := ResponsesStreamEvent{OutputIndex: evt.OutputIndex, ItemID: evt.Item.ID, ContentIndex: i, Type: "response.output_text.done", Text: part.Text}
			switch part.Type {
			case "output_text":
				events = append(events, resToAnthRecoverText(part.Text, resToAnthTextPartOf(&event, state), state)...)
			case "refusal":
				event.Type = "response.refusal.done"
				events = append(events, resToAnthRecoverText(part.Refusal, resToAnthTextPartOf(&event, state), state)...)
			}
		}
		if state.CurrentBlockType == "text" {
			events = append(events, closeCurrentBlock(state)...)
		}
		return events
	}

	// Handle web_search_call → synthesize server_tool_use + web_search_tool_result blocks.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	if evt.Item.Type == "reasoning" {
		return resToAnthRecoverReasoning(evt, state)
	}

	if state.ContentBlockOpen {
		return closeCurrentBlock(state)
	}
	return nil
}

// resToAnthHandleWebSearchDone converts an OpenAI web_search_call output item
// into Anthropic server_tool_use + web_search_tool_result content block pairs.
// This allows Claude Code to count the searches performed.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	key := evt.Item.ID
	if key == "" {
		key = fmt.Sprintf("output:%d", evt.OutputIndex)
	}
	if state.searchesDone == nil {
		state.searchesDone = make(map[string]bool)
	}
	if state.searchesDone[key] {
		return nil
	}
	state.searchesDone[key] = true
	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	// Emit server_tool_use block (start + stop).
	idx1 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx1,
		ContentBlock: &AnthropicContentBlock{
			Type:  "server_tool_use",
			ID:    toolUseID,
			Name:  "web_search",
			Input: inputJSON,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx1,
	})
	state.ContentBlockIndex++

	// Emit web_search_tool_result block (start + stop).
	// Return only sources present on this completed upstream item.
	idx2 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx2,
		ContentBlock: &AnthropicContentBlock{
			Type:      "web_search_tool_result",
			ToolUseID: toolUseID,
			Content:   responsesWebSearchSources(*evt.Item),
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx2,
	})
	state.ContentBlockIndex++

	return events
}

// resToAnthRecoverTerminalText emits assistant text that only ever appeared in
// the terminal response payload, which some streams populate without sending
// any output_text event.
//
// Named items and refusal parts are reconciled independently. Anonymous text
// retains the legacy guard when a terminal array cannot identify streamed items.
func resToAnthRecoverTerminalText(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response == nil {
		return nil
	}
	var events []AnthropicStreamEvent
	hadText := state.textDelivered
	anonymousText := false
	for part, delivered := range state.textByPart {
		if part.ItemID == "" && part.Kind == "output_text" && delivered.Len() > 0 {
			anonymousText = true
		}
	}
	for outputIndex, item := range evt.Response.Output {
		if item.Type != "message" {
			continue
		}
		for contentIndex, content := range item.Content {
			if content.Type != "output_text" && content.Type != "refusal" {
				continue
			}
			if content.Type == "output_text" && hadText && (item.ID == "" || anonymousText) {
				continue
			}
			part := responsesTextPart{OutputIndex: outputIndex, ItemID: item.ID, ContentIndex: contentIndex, Kind: content.Type}
			if part.ItemID != "" {
				part.OutputIndex = 0
			}
			text := content.Text
			if content.Type == "refusal" {
				text = content.Refusal
				state.hasRefusal = true
			}
			if !hadText && content.Type == "output_text" {
				// Preserve recovery of all anonymous terminal parts on empty streams.
				events = append(events, resToAnthEmitText(text, part, state)...)
			} else {
				events = append(events, resToAnthRecoverText(text, part, state)...)
			}
		}
	}
	if len(events) > 0 {
		events = append(events, closeCurrentBlock(state)...)
	}
	return events
}

func resToAnthRecordUsage(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) {
	if evt.Usage != nil {
		usage := anthropicUsageFromResponsesUsage(evt.Usage)
		state.InputTokens = usage.InputTokens
		state.OutputTokens = usage.OutputTokens
		state.CacheReadInputTokens = usage.CacheReadInputTokens
		state.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			usage := anthropicUsageFromResponsesUsage(evt.Response.Usage)
			state.InputTokens = usage.InputTokens
			state.OutputTokens = usage.OutputTokens
			state.CacheReadInputTokens = usage.CacheReadInputTokens
			state.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
	}

}

func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	resToAnthRecordUsage(evt, state)

	status, reason := strings.TrimPrefix(evt.Type, "response."), ""
	if status == "done" {
		status = "completed"
	}
	if evt.Response != nil {
		status = evt.Response.Status
		reason = responsesIncompleteReason(evt.Response)
		state.hasRefusal = state.hasRefusal || responsesHasRefusal(evt.Response)
	}
	if evt.Type == "response.failed" || (evt.Response != nil && evt.Response.Error != nil) {
		status = "failed"
	}
	if status == "" {
		status = strings.TrimPrefix(evt.Type, "response.")
		if status == "done" {
			status = "completed"
		}
	}
	_, _, err := ProjectTerminal("responses", "anthropic", status, reason, state.HasToolCall, state.hasRefusal)
	if err != nil {
		if state.err == nil {
			state.err = err
		}
		return nil
	}

	var events []AnthropicStreamEvent
	if evt.Response != nil && evt.Response.Status == "completed" {
		state.successTerminal = true
		terminalOnlyTools := len(state.toolOrder) == 0
		for i, item := range evt.Response.Output {
			if terminalOnlyTools && (item.Type == "function_call" || item.Type == "custom_tool_call") {
				events = append(events, resToAnthHandleOutputItemDone(&ResponsesStreamEvent{OutputIndex: i, Item: &item}, state)...)
				continue
			}
			if tool := state.toolItems[item.ID]; tool != nil {
				raw, present := item.Arguments, item.Arguments != ""
				if tool.kind == "custom_tool_call" {
					raw, present = item.Input, item.Input != "" || tool.raw == ""
				}
				resToAnthCompleteTool(tool, raw, present, state)
			}
		}
		if state.err != nil {
			return nil
		}
		for _, tool := range state.toolOrder {
			resToAnthCompleteTool(tool, "", false, state)
		}
		if state.err != nil {
			return nil
		}
		if state.activeTool != nil {
			events = append(events, resToAnthEmitTool(state.activeTool, state)...)
		}
		events = append(events, resToAnthDrainPendingBlocks(state)...)
	}
	if state.activeTool != nil && !state.activeTool.complete {
		state.fail("arguments", "tool arguments ended without completion")
		return events
	}
	if evt.Response != nil {
		for i, item := range evt.Response.Output {
			if item.Type == "reasoning" {
				events = append(events, resToAnthRecoverReasoning(&ResponsesStreamEvent{OutputIndex: i, Item: &item}, state)...)
			}
		}
	}
	if state.err != nil {
		return events
	}
	events = append(events, closeCurrentBlock(state)...)
	events = append(events, resToAnthRecoverTerminalText(evt, state)...)
	if evt.Response != nil {
		for i, item := range evt.Response.Output {
			if item.Type == "web_search_call" && item.Status == "completed" {
				events = append(events, resToAnthHandleWebSearchDone(&ResponsesStreamEvent{OutputIndex: i, Item: &item}, state)...)
			}
		}
	}

	_, stopReason, err := ProjectTerminal("responses", "anthropic", status, reason, state.HasToolCall, state.hasRefusal)
	if err != nil {
		if state.err == nil {
			state.err = err
		}
		return events
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

func closeCurrentBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.CurrentBlockType == "tool_use" && state.activeTool != nil && !state.activeTool.complete {
		return nil
	}
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	var events []AnthropicStreamEvent
	// Emit signature_delta before stop so Claude clients retain encrypted
	// reasoning for the next turn (required for Grok multi-turn cache).
	if state.CurrentBlockType == "thinking" {
		if sig := strings.TrimSpace(state.PendingThinkingSignature); sig != "" {
			events = append(events, AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &idx,
				Delta: &AnthropicDelta{
					Type:      "signature_delta",
					Signature: sig,
				},
			})
		}
		state.PendingThinkingSignature = ""
		if state.activeReasoning != nil {
			state.activeReasoning.signatureSent = state.activeReasoning.signature != ""
			state.activeReasoning = nil
		}
	}
	state.ContentBlockOpen = false
	state.ContentBlockIndex++
	state.CurrentToolName = ""
	state.CurrentToolArgs = ""
	state.CurrentToolHadDelta = false
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx,
	})
	return events
}
