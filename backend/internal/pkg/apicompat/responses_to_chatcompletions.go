package apicompat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → ChatCompletionsResponse
// ---------------------------------------------------------------------------

// ResponsesToChatCompletions converts a Responses API response into a Chat
// Completions response. Text output items are concatenated into
// choices[0].message.content, refusal parts into choices[0].message.refusal;
// function_call items become tool_calls.
func ResponsesToChatCompletions(resp *ResponsesResponse, model string) *ChatCompletionsResponse {
	id := resp.ID
	if id == "" {
		id = generateChatCmplID()
	}

	out := &ChatCompletionsResponse{
		ID:                     id,
		Object:                 "chat.completion",
		Created:                time.Now().Unix(),
		Model:                  model,
		ServiceTier:            resp.ServiceTier,
		PromptCacheDiagnostics: resp.PromptCacheDiagnostics,
	}

	var contentText string
	var refusalText string
	var reasoningText string
	var toolCalls []ChatToolCall

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch {
				case part.Type == "output_text" && part.Text != "":
					contentText += part.Text
				case part.Type == "refusal" && part.Refusal != "":
					refusalText += part.Refusal
				}
			}
		case "function_call", "custom_tool_call":
			toolCalls = append(toolCalls, ChatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: ChatFunctionCall{
					Name:      item.Name,
					Arguments: responsesToolArguments(item, resp.Status == "completed"),
				},
			})
		case "reasoning":
			reasoningText += ResponsesOutputReasoningText(item)
		case "web_search_call":
			// silently consumed — results already incorporated into text output
		}
	}

	msg := ChatMessage{Role: "assistant", Refusal: refusalText}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	if contentText != "" {
		raw, _ := json.Marshal(contentText)
		msg.Content = raw
	}
	if reasoningText != "" {
		msg.ReasoningContent = reasoningText
	}

	finishReason := responsesStatusToChatFinishReason(resp.Status, resp.IncompleteDetails, toolCalls, refusalText != "")

	out.Choices = []ChatChoice{{
		Index:        0,
		Message:      msg,
		FinishReason: finishReason,
	}}

	out.Usage = chatUsageFromResponsesUsage(resp.Usage)

	return out
}

func responsesStatusToChatFinishReason(status string, details *ResponsesIncompleteDetails, toolCalls []ChatToolCall, hasRefusal bool) string {
	reason := ""
	if details != nil {
		reason = details.Reason
	}
	_, finish, _ := ProjectTerminal("responses", "chat", status, reason, len(toolCalls) > 0, hasRefusal)
	return finish
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []ChatCompletionsChunk (stateful converter)
// ---------------------------------------------------------------------------

// ResponsesEventToChatState tracks state for converting a sequence of Responses
// SSE events into Chat Completions SSE chunks.
type ResponsesEventToChatState struct {
	visible                *BufferedResponseAccumulator
	visibleSent            map[*bufferedOutputSlot]map[int]string
	SawRefusal             bool
	reasoning              *BufferedResponseAccumulator
	reasoningSent          map[*bufferedOutputSlot]string
	reasoningCarrier       map[*bufferedOutputSlot]string
	tools                  ResponsesToolCallTracker
	err                    error
	ID                     string
	Model                  string
	Created                int64
	ServiceTier            string // upstream tier observed on response events; echoed on chunks
	SentRole               bool
	SawToolCall            bool
	SawText                bool
	Finalized              bool        // true after finish chunk has been emitted
	NextToolCallIndex      int         // next sequential tool_call index to assign
	OutputIndexToToolIndex map[int]int // Responses output_index → Chat tool_calls index
	OutputIndexToArguments map[int]string
	IncludeUsage           bool
	Usage                  *ChatUsage
}

// NewResponsesEventToChatState returns an initialised stream state.
func NewResponsesEventToChatState() *ResponsesEventToChatState {
	return &ResponsesEventToChatState{
		ID:                     generateChatCmplID(),
		Created:                time.Now().Unix(),
		OutputIndexToToolIndex: make(map[int]int),
		OutputIndexToArguments: make(map[int]string),
	}
}

// ResponsesEventToChatChunks converts a single Responses SSE event into zero
// or more Chat Completions chunks, updating state as it goes.
func (state *ResponsesEventToChatState) Err() error { return state.err }

func ResponsesEventToChatChunks(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt == nil || state.Finalized || state.err != nil {
		return nil
	}
	switch evt.Type {
	case "response.created":
		return resToChatHandleCreated(evt, state)
	case "response.output_text.delta":
		return resToChatHandleTextDelta(evt, state)
	case "response.output_text.done", "response.refusal.done", "response.content_part.added", "response.content_part.done":
		if evt.Part != nil && evt.Part.Type == "reasoning_text" {
			return resToChatHandleReasoningDelta(evt, state)
		}
		return resToChatHandleVisibleEvent(evt, state)
	case "response.refusal.delta":
		return resToChatHandleRefusalDelta(evt, state)
	case "response.output_item.added":
		return resToChatHandleOutputItemAdded(evt, state)
	case "response.output_item.done":
		return resToChatHandleOutputItemDone(evt, state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具（如新版 apply_patch）的输入增量与 function_call 参数增量同形，
		// 均按 OutputIndex 累加到对应工具调用。
		"response.custom_tool_call_input.delta":
		return resToChatHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		return resToChatHandleFuncArgsDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量（真实 Codex 客户端消费的 reasoning_text.delta），
		// 与 reasoning summary 一样映射为 reasoning_content。
		"response.reasoning_text.delta":
		return resToChatHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done", "response.reasoning_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return resToChatHandleReasoningDelta(evt, state)
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToChatHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesChatStream emits a final chunk with finish_reason if the
// stream ended without a proper completion event (e.g. upstream disconnect).
// It is idempotent: if a completion event already emitted the finish chunk,
// this returns nil.
func FinalizeResponsesChatStream(state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.Finalized {
		return nil
	}
	state.Finalized = true

	finishReason := "stop"
	if state.SawToolCall {
		finishReason = "tool_calls"
	}

	chunks := []ChatCompletionsChunk{makeChatFinishChunk(state, finishReason)}

	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:          state.ID,
			Object:      "chat.completion.chunk",
			Created:     state.Created,
			Model:       state.Model,
			ServiceTier: state.ServiceTier,
			Choices:     []ChatChunkChoice{},
			Usage:       state.Usage,
		})
	}

	return chunks
}

// ChatChunkToSSE formats a ChatCompletionsChunk as an SSE data line.
func ChatChunkToSSE(chunk ChatCompletionsChunk) (string, error) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("data: %s\n\n", data), nil
}

// --- internal handlers ---

func resToChatHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Response != nil {
		if evt.Response.ID != "" {
			state.ID = evt.Response.ID
		}
		if state.Model == "" && evt.Response.Model != "" {
			state.Model = evt.Response.Model
		}
		if evt.Response.ServiceTier != "" {
			state.ServiceTier = evt.Response.ServiceTier
		}
	}
	// Emit the role chunk.
	if state.SentRole {
		return nil
	}
	state.SentRole = true

	role := "assistant"
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{Role: role})}
}

func resToChatHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	return resToChatHandleVisibleEvent(evt, state)
}
func resToChatHandleRefusalDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	return resToChatHandleVisibleEvent(evt, state)
}

func resToChatToolSnapshot(item ResponsesOutput, index int, known, complete, terminal bool, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if item.Type != "function_call" && item.Type != "custom_tool_call" {
		return nil
	}
	call, added, err := state.tools.Register(item, index, known, terminal)
	if err != nil {
		state.err = err
		return nil
	}
	state.SawToolCall = true
	state.NextToolCallIndex = len(state.tools.calls)
	if known {
		state.OutputIndexToToolIndex[index] = call.Index
	}
	var chunks []ChatCompletionsChunk
	if added {
		i := call.Index
		chunks = append(chunks, makeChatDeltaChunk(state, ChatDelta{ToolCalls: []ChatToolCall{{Index: &i, ID: call.Item.CallID, Type: "function", Function: ChatFunctionCall{Name: call.Item.Name}}}}))
	}
	value := item.Arguments
	if complete && item.Type == "function_call" && value == "" {
		value = call.Sent
	}
	if item.Type == "custom_tool_call" {
		value = item.Input
	}
	suffix, err := state.tools.Snapshot(call, value, complete)
	if err != nil {
		state.err = err
		return nil
	}
	if suffix != "" {
		chunks = append(chunks, resToChatToolArgumentsChunk(call, suffix, state))
	}
	return chunks
}

func resToChatToolArgumentsChunk(call *responsesTrackedTool, value string, state *ResponsesEventToChatState) ChatCompletionsChunk {
	index := call.Index
	return makeChatDeltaChunk(state, ChatDelta{ToolCalls: []ChatToolCall{{Index: &index, Function: ChatFunctionCall{Arguments: value}}}})
}

func resToChatHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Item != nil && evt.Item.Type == "reasoning" {
		return resToChatHandleReasoningDelta(evt, state)
	}
	if evt.Item != nil && evt.Item.Type == "message" {
		return resToChatHandleVisibleEvent(evt, state)
	}
	if evt.Item == nil {
		return nil
	}
	return resToChatToolSnapshot(*evt.Item, evt.OutputIndex, evt.OutputIndexPresent || evt.OutputIndex != 0, false, false, state)
}

func resToChatHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Item != nil && evt.Item.Type == "reasoning" {
		return resToChatHandleReasoningDelta(evt, state)
	}
	if evt.Item != nil && evt.Item.Type == "message" {
		return resToChatHandleVisibleEvent(evt, state)
	}
	if evt.Item == nil {
		return nil
	}
	return resToChatToolSnapshot(*evt.Item, evt.OutputIndex, evt.OutputIndexPresent || evt.OutputIndex != 0, true, false, state)
}

func resToChatHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Delta == "" {
		return nil
	}
	call, err := state.tools.Resolve(evt)
	if err != nil {
		state.err = err
		return nil
	}
	if call.Closed {
		return nil
	}
	call.Sent += evt.Delta
	if call.IndexKnown {
		state.OutputIndexToArguments[call.OutputIndex] = call.Sent
	}
	return []ChatCompletionsChunk{resToChatToolArgumentsChunk(call, evt.Delta, state)}
}

func resToChatHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	call, err := state.tools.Resolve(evt)
	if err != nil {
		state.err = err
		return nil
	}
	value := evt.Arguments
	present := evt.ArgumentsPresent || value != ""
	if evt.Type == "response.custom_tool_call_input.done" {
		value = evt.Input
		present = evt.InputPresent || value != ""
	}
	if !present {
		return nil
	}
	suffix, err := state.tools.Snapshot(call, value, true)
	if err != nil {
		state.err = err
		return nil
	}
	if suffix == "" {
		return nil
	}
	return []ChatCompletionsChunk{resToChatToolArgumentsChunk(call, suffix, state)}
}

func resToChatRecoverTerminalTools(resp *ResponsesResponse, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	var chunks []ChatCompletionsChunk
	if resp != nil {
		for i, item := range resp.Output {
			chunks = append(chunks, resToChatToolSnapshot(item, i, true, true, true, state)...)
			if state.err != nil {
				return nil
			}
		}
	}
	return chunks
}

func resToChatHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.reasoning == nil {
		state.reasoning = NewBufferedResponseAccumulator()
		state.reasoningSent = map[*bufferedOutputSlot]string{}
		state.reasoningCarrier = map[*bufferedOutputSlot]string{}
	}
	state.reasoning.ProcessEvent(evt)
	if state.reasoning.Err() != nil {
		state.err = state.reasoning.Err()
		return nil
	}
	var chunks []ChatCompletionsChunk
	for _, slot := range state.reasoning.slots {
		closed := evt.Type == "response.output_item.done" && evt.Item != nil && (slot.item.ID == evt.Item.ID || (evt.Item.ID == "" && slot.index == evt.OutputIndex))
		chunks = append(chunks, resToChatRecoverReasoningItem(slot, closed, state)...)
		if state.err != nil {
			return nil
		}
	}
	return chunks
}

func resToChatRecoverReasoningItem(slot *bufferedOutputSlot, closed bool, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	item := slot.item
	indices := make([]int, 0, len(slot.summaries))
	for i := range slot.summaries {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		item.Summary = append(item.Summary, slot.summaries[i])
	}
	indices = indices[:0]
	for i := range slot.parts {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		item.Content = append(item.Content, slot.parts[i])
	}
	summary := item
	summary.Content = nil
	carrier := "summary"
	text := ResponsesOutputReasoningText(summary)
	if text == "" {
		if !closed {
			return nil
		}
		carrier = "content"
		text = ResponsesOutputReasoningText(item)
	}
	if text == "" {
		return nil
	}
	if previous := state.reasoningCarrier[slot]; previous != "" && previous != carrier {
		state.err = toolTrackingError("reasoning", "reasoning carrier conflicts with delivered text")
		return nil
	}
	sent := state.reasoningSent[slot]
	if !strings.HasPrefix(text, sent) {
		state.err = toolTrackingError("reasoning", "reasoning snapshot conflicts with delivered prefix")
		return nil
	}
	state.reasoningCarrier[slot] = carrier
	state.reasoningSent[slot] = text
	suffix := text[len(sent):]
	if suffix == "" {
		return nil
	}
	return []ChatCompletionsChunk{makeChatDeltaChunk(state, ChatDelta{ReasoningContent: &suffix})}
}

func resToChatRecoverTerminalReasoning(resp *ResponsesResponse, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.reasoning == nil {
		state.reasoning = NewBufferedResponseAccumulator()
		state.reasoningSent = map[*bufferedOutputSlot]string{}
		state.reasoningCarrier = map[*bufferedOutputSlot]string{}
	}
	if resp != nil {
		for i, item := range resp.Output {
			if item.Type == "reasoning" {
				state.reasoning.snapshot(item, i, true, 3)
			}
		}
	}
	if state.reasoning.Err() != nil {
		state.err = state.reasoning.Err()
		return nil
	}
	var chunks []ChatCompletionsChunk
	for _, slot := range state.reasoning.slots {
		chunks = append(chunks, resToChatRecoverReasoningItem(slot, true, state)...)
		if state.err != nil {
			return nil
		}
	}
	return chunks
}

func resToChatHandleVisibleEvent(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if state.visible == nil {
		state.visible = NewBufferedResponseAccumulator()
		state.visibleSent = map[*bufferedOutputSlot]map[int]string{}
	}
	state.visible.ProcessEvent(evt)
	if state.visible.Err() != nil {
		state.err = state.visible.Err()
		return nil
	}
	return resToChatRecoverVisible(state)
}

func resToChatRecoverVisible(state *ResponsesEventToChatState) []ChatCompletionsChunk {
	var chunks []ChatCompletionsChunk
	for _, slot := range state.visible.slots {
		if state.visibleSent[slot] == nil {
			state.visibleSent[slot] = map[int]string{}
		}
		indices := make([]int, 0, len(slot.parts))
		for i := range slot.parts {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			part := slot.parts[i]
			text := part.Text
			if part.Type == "refusal" {
				text = part.Refusal
				state.SawRefusal = state.SawRefusal || text != ""
			} else if part.Type != "output_text" {
				continue
			}
			sent := state.visibleSent[slot][i]
			if !strings.HasPrefix(text, sent) {
				state.err = toolTrackingError("content", "content snapshot conflicts with delivered prefix")
				return nil
			}
			suffix := text[len(sent):]
			state.visibleSent[slot][i] = text
			if suffix == "" {
				continue
			}
			delta := ChatDelta{}
			if part.Type == "refusal" {
				delta.Refusal = &suffix
			} else {
				delta.Content = &suffix
				state.SawText = true
			}
			chunks = append(chunks, makeChatDeltaChunk(state, delta))
		}
	}
	return chunks
}

func resToChatHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToChatState) []ChatCompletionsChunk {
	if evt.Usage != nil {
		state.Usage = chatUsageFromResponsesUsage(evt.Usage)
	}
	resp := evt.Response
	if resp == nil {
		state.err = toolTrackingError("response", "missing terminal response")
		return nil
	}
	if resp.Usage != nil {
		state.Usage = chatUsageFromResponsesUsage(resp.Usage)
	}
	if resp.ServiceTier != "" {
		state.ServiceTier = resp.ServiceTier
	}
	if evt.Type == "response.failed" {
		state.err = toolTrackingError("status", "upstream response failed")
		return nil
	}
	if err := ValidateResponsesForChat(resp); err != nil {
		state.err = err
		return nil
	}
	recovered := resToChatRecoverTerminalReasoning(resp, state)
	if state.err != nil {
		return nil
	}
	recovered = append(recovered, resToChatRecoverTerminalTools(resp, state)...)
	if state.err != nil {
		return nil
	}
	recovered = append(recovered, resToChatValidateTools(resp, state)...)
	if state.err != nil {
		return nil
	}
	if state.visible == nil {
		state.visible = NewBufferedResponseAccumulator()
		state.visibleSent = map[*bufferedOutputSlot]map[int]string{}
	}
	for i, item := range resp.Output {
		if item.Type == "message" {
			state.visible.snapshot(item, i, true, 3)
		}
	}
	if state.visible.Err() != nil {
		state.err = state.visible.Err()
		return nil
	}
	recovered = append(recovered, resToChatRecoverVisible(state)...)
	if state.err != nil {
		return nil
	}
	reason := ""
	if resp.IncompleteDetails != nil {
		reason = resp.IncompleteDetails.Reason
	}
	_, finishReason, err := ProjectTerminal("responses", "chat", resp.Status, reason, state.SawToolCall, state.SawRefusal)
	if err != nil {
		state.err = err
		return nil
	}
	state.Finalized = true
	finish := makeChatFinishChunk(state, finishReason)
	finish.PromptCacheDiagnostics = resp.PromptCacheDiagnostics
	recovered = append(recovered, finish)
	if state.IncludeUsage && state.Usage != nil {
		recovered = append(recovered, ChatCompletionsChunk{ID: state.ID, Object: "chat.completion.chunk", Created: state.Created, Model: state.Model, ServiceTier: state.ServiceTier, Choices: []ChatChunkChoice{}, Usage: state.Usage})
	}
	return recovered
}

// FinalizeResponsesChatStreamWithTerminal only closes an independently
// authenticated stream. The legacy finalizer remains unchanged for old callers.
func FinalizeResponsesChatStreamWithTerminal(state *ResponsesEventToChatState, terminalEligible bool) []ChatCompletionsChunk {
	if state.Finalized || state.err != nil {
		return nil
	}
	if !terminalEligible {
		state.err = toolTrackingError("response", "missing authenticated terminal")
		return nil
	}
	return resToChatHandleCompleted(&ResponsesStreamEvent{Type: "response.completed", Response: &ResponsesResponse{Status: "completed"}}, state)
}

func chatUsageFromResponsesUsage(u *ResponsesUsage) *ChatUsage {
	if u == nil {
		return nil
	}
	usage := &ChatUsage{
		PromptTokens:            u.InputTokens,
		CompletionTokens:        u.OutputTokens,
		TotalTokens:             u.InputTokens + u.OutputTokens,
		UsagePresenceKnown:      u.UsagePresenceKnown,
		PromptTokensPresent:     u.InputTokensPresent,
		CompletionTokensPresent: u.OutputTokensPresent,
		TotalTokensPresent:      u.TotalTokensPresent,
	}
	usage.PromptTokensDetails = promptDetailsFromResponses(u.InputTokensDetails)
	if u.CacheCreationInputTokens > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &ChatTokenDetails{}
		}
		if usage.PromptTokensDetails.CacheWriteTokens == 0 && usage.PromptTokensDetails.CacheCreationTokens == 0 {
			usage.PromptTokensDetails.CacheCreationTokens = u.CacheCreationInputTokens
		}
	}
	usage.CompletionTokensDetails = completionDetailsFromResponses(u.OutputTokensDetails)
	return usage
}

// promptDetailsFromResponses maps Responses-API input_tokens_details into a
// Chat-Completions prompt_tokens_details, preserving an explicit cached zero.
func promptDetailsFromResponses(src *ResponsesInputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	return &ChatTokenDetails{
		CachedTokensPresent: true,
		CachedTokens:        src.CachedTokens,
		AudioTokens:         src.AudioTokens,
		CacheCreationTokens: src.CacheCreationTokens,
		CacheWriteTokens:    src.CacheWriteTokens,
	}
}

// completionDetailsFromResponses maps Responses-API output_tokens_details
// into a Chat-Completions completion_tokens_details. Mirrors the OpenAI
// official CompletionUsage schema: reasoning_tokens, audio_tokens, and
// the predicted-outputs accepted/rejected counts. Returns nil when nothing
// would be emitted so non-reasoning, non-audio responses stay clean.
func completionDetailsFromResponses(src *ResponsesOutputTokensDetails) *ChatTokenDetails {
	if src == nil {
		return nil
	}
	if src.ReasoningTokens == 0 && src.AudioTokens == 0 &&
		src.AcceptedPredictionTokens == 0 && src.RejectedPredictionTokens == 0 {
		return nil
	}
	return &ChatTokenDetails{
		ReasoningTokens:          src.ReasoningTokens,
		AudioTokens:              src.AudioTokens,
		AcceptedPredictionTokens: src.AcceptedPredictionTokens,
		RejectedPredictionTokens: src.RejectedPredictionTokens,
	}
}

func makeChatDeltaChunk(state *ResponsesEventToChatState, delta ChatDelta) ChatCompletionsChunk {
	return ChatCompletionsChunk{
		ID:          state.ID,
		Object:      "chat.completion.chunk",
		Created:     state.Created,
		Model:       state.Model,
		ServiceTier: state.ServiceTier,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: nil,
		}},
	}
}

func makeChatFinishChunk(state *ResponsesEventToChatState, finishReason string) ChatCompletionsChunk {
	empty := ""
	return ChatCompletionsChunk{
		ID:          state.ID,
		Object:      "chat.completion.chunk",
		Created:     state.Created,
		Model:       state.Model,
		ServiceTier: state.ServiceTier,
		Choices: []ChatChunkChoice{{
			Index:        0,
			Delta:        ChatDelta{Content: &empty},
			FinishReason: &finishReason,
		}},
	}
}

// generateChatCmplID returns a "chatcmpl-" prefixed random hex ID.
func generateChatCmplID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "chatcmpl-" + hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// BufferedResponseAccumulator: accumulates SSE delta events for non-streaming
// paths where the terminal event may have empty output.
// ---------------------------------------------------------------------------

type bufferedOutputSlot struct {
	item         ResponsesOutput
	index        int
	explicit     bool
	rank         int
	argsRank     int
	parts        map[int]ResponsesContentPart
	partRanks    map[int]int
	summaries    map[int]ResponsesSummary
	summaryRanks map[int]int
}

// BufferedResponseAccumulator keeps independent output and content slots. It
// restores content only; the reader still authenticates the upstream terminal.
type BufferedResponseAccumulator struct {
	slots   []*bufferedOutputSlot
	current *bufferedOutputSlot
	err     error
}

func NewBufferedResponseAccumulator() *BufferedResponseAccumulator {
	return &BufferedResponseAccumulator{}
}
func (a *BufferedResponseAccumulator) Err() error { return a.err }

func (a *BufferedResponseAccumulator) fail(field, reason string) {
	if a.err == nil {
		a.err = &UpstreamConversionError{Field: field, Reason: reason, SourceProtocol: "responses"}
	}
}

func bufferedIdentityConflict(left, right ResponsesOutput) bool {
	return (left.ID != "" && right.ID != "" && left.ID != right.ID) ||
		(left.CallID != "" && right.CallID != "" && left.CallID != right.CallID) ||
		(left.Type != "" && right.Type != "" && left.Type != right.Type)
}

func (a *BufferedResponseAccumulator) slot(item ResponsesOutput, index int, explicit, terminal bool) *bufferedOutputSlot {
	if !terminal && !explicit && item.ID == "" && item.CallID == "" && a.current != nil && !bufferedIdentityConflict(a.current.item, item) {
		return a.current
	}
	var found *bufferedOutputSlot
	for _, candidate := range a.slots {
		if (item.ID != "" && item.ID == candidate.item.ID) || (item.CallID != "" && item.CallID == candidate.item.CallID) {
			if found != nil && found != candidate {
				a.fail("output", "ambiguous output identity")
				return nil
			}
			found = candidate
		}
	}
	if found != nil {
		if bufferedIdentityConflict(found.item, item) {
			a.fail("output", "conflicting output identity")
			return nil
		}
		if !terminal && explicit && found.explicit && found.index != index {
			a.fail("output_index", "conflicting output index")
			return nil
		}
		return found
	}
	if explicit {
		for _, candidate := range a.slots {
			if candidate.index != index || !candidate.explicit {
				continue
			}
			if bufferedIdentityConflict(candidate.item, item) {
				a.fail("output", "conflicting output identity")
				return nil
			}
			return candidate
		}
	}
	// Old callers may construct events without indices. Reuse only an anonymous,
	// compatible slot; distinct item/call IDs never collapse into one another.
	for _, candidate := range a.slots {
		if candidate.explicit || bufferedIdentityConflict(candidate.item, item) {
			continue
		}
		if item.ID != "" && candidate.item.ID != "" || item.CallID != "" && candidate.item.CallID != "" {
			continue
		}
		if found != nil {
			a.fail("output", "ambiguous unindexed output")
			return nil
		}
		found = candidate
	}
	if found != nil {
		if explicit {
			found.index = index
			found.explicit = true
		}
		return found
	}
	if !explicit {
		index = len(a.slots)
	}
	found = &bufferedOutputSlot{index: index, explicit: explicit, parts: map[int]ResponsesContentPart{}, partRanks: map[int]int{}, summaries: map[int]ResponsesSummary{}, summaryRanks: map[int]int{}}
	a.slots = append(a.slots, found)
	return found
}

func mergeBufferedItem(previous, incoming ResponsesOutput) ResponsesOutput {
	out := incoming
	if out.Type == "" {
		out.Type = previous.Type
	}
	if out.ID == "" {
		out.ID = previous.ID
	}
	if out.CallID == "" {
		out.CallID = previous.CallID
	}
	if out.Role == "" {
		out.Role = previous.Role
	}
	if out.Status == "" {
		out.Status = previous.Status
	}
	if out.Name == "" {
		out.Name = previous.Name
	}
	if out.Namespace == "" {
		out.Namespace = previous.Namespace
	}
	if out.Action == nil {
		out.Action = previous.Action
	}
	if out.Arguments == "" {
		out.Arguments = previous.Arguments
	}
	if out.Input == "" {
		out.Input = previous.Input
	}
	if out.EncryptedContent == "" {
		out.EncryptedContent = previous.EncryptedContent
	}
	if len(out.encryptedContentRaw) == 0 {
		out.encryptedContentRaw = previous.encryptedContentRaw
	}
	if len(out.EncryptedFunctionArgs) == 0 {
		out.EncryptedFunctionArgs = previous.EncryptedFunctionArgs
	}
	out.Content = nil
	out.Summary = nil
	return out
}

func (a *BufferedResponseAccumulator) part(slot *bufferedOutputSlot, index int, part ResponsesContentPart, rank int, delta bool) {
	if index < 0 {
		a.fail("content_index", "invalid content index")
		return
	}
	old := slot.parts[index]
	if old.Type != "" && part.Type != "" && old.Type != part.Type {
		if slot.explicit {
			a.fail("content", "conflicting content part types")
			return
		}
		matched := -1
		for candidateIndex, candidate := range slot.parts {
			if candidate.Type == part.Type {
				if matched >= 0 {
					a.fail("content", "ambiguous unindexed content part")
					return
				}
				matched = candidateIndex
			}
		}
		if matched >= 0 {
			index = matched
		} else {
			index = len(slot.parts)
		}
		old = slot.parts[index]
	}
	if delta {
		if slot.partRanks[index] > 1 {
			return
		}
		if part.Type == "refusal" {
			part.Refusal = old.Refusal + part.Refusal
		} else {
			part.Text = old.Text + part.Text
		}
	} else if strings.TrimSpace(part.Text) == "" && part.Refusal == "" && (old.Text != "" || old.Refusal != "") {
		return
	}
	if rank >= slot.partRanks[index] {
		slot.parts[index] = part
		slot.partRanks[index] = rank
	}
}

func (a *BufferedResponseAccumulator) summary(slot *bufferedOutputSlot, index int, text string, rank int, delta bool) {
	if index < 0 {
		a.fail("summary_index", "invalid summary index")
		return
	}
	old := slot.summaries[index]
	if delta {
		if slot.summaryRanks[index] > 1 {
			return
		}
		text = old.Text + text
	} else if text == "" && old.Text != "" {
		return
	}
	if rank >= slot.summaryRanks[index] {
		slot.summaries[index] = ResponsesSummary{Type: "summary_text", Text: text}
		slot.summaryRanks[index] = rank
	}
}

func (a *BufferedResponseAccumulator) snapshot(item ResponsesOutput, index int, explicit bool, rank int) {
	slot := a.slot(item, index, explicit, rank == 3)
	if slot == nil {
		return
	}
	if rank == 1 {
		a.current = slot
	}
	if rank >= slot.rank {
		slot.item = mergeBufferedItem(slot.item, item)
		slot.rank = rank
	}
	for i, part := range item.Content {
		a.part(slot, i, part, rank, false)
	}
	for i, summary := range item.Summary {
		if summary.Type == "summary_text" {
			a.summary(slot, i, summary.Text, rank, false)
		}
	}
	if item.Arguments != "" && rank >= slot.argsRank {
		slot.item.Arguments = item.Arguments
		slot.argsRank = rank
	}
	if item.Type == "custom_tool_call" && (item.Input != "" || slot.item.Input == "") && rank >= slot.argsRank {
		slot.item.Input = item.Input
		slot.argsRank = rank
	}
}

func (a *BufferedResponseAccumulator) ProcessEvent(event *ResponsesStreamEvent) {
	if event == nil || a.err != nil {
		return
	}
	explicit := event.OutputIndexPresent || event.OutputIndex != 0
	switch event.Type {
	case "response.output_item.added", "response.output_item.done":
		if event.Item != nil {
			rank := 1
			if event.Type == "response.output_item.done" {
				rank = 2
			}
			a.snapshot(*event.Item, event.OutputIndex, explicit, rank)
		}
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		if event.Response != nil {
			for i, item := range event.Response.Output {
				a.snapshot(item, i, true, 3)
			}
			snapshot := *event.Response
			snapshot.Output = a.BuildOutput()
			if err := validateResponsesChatTools(&snapshot); err != nil && a.err == nil {
				a.err = err
			}
		}
	case "response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done", "response.content_part.added", "response.content_part.done", "response.reasoning_text.delta", "response.reasoning_text.done":
		kind := "message"
		part := ResponsesContentPart{Type: "output_text", Text: event.Text}
		rank := 2
		delta := strings.HasSuffix(event.Type, ".delta")
		if delta {
			rank = 1
			part.Text = event.Delta
			if event.Delta == "" {
				return
			}
		}
		if strings.HasPrefix(event.Type, "response.refusal.") {
			part.Type = "refusal"
			part.Text = ""
			part.Refusal = event.Refusal
			if delta {
				part.Refusal = event.Delta
			}
		}
		if strings.HasPrefix(event.Type, "response.reasoning_text.") {
			kind = "reasoning"
			part.Type = "reasoning_text"
		}
		if strings.HasPrefix(event.Type, "response.content_part.") {
			if event.Part == nil {
				return
			}
			part = *event.Part
			if part.Type == "reasoning_text" {
				kind = "reasoning"
			}
			if event.Type == "response.content_part.added" {
				rank = 1
			}
		}
		slot := a.slot(ResponsesOutput{Type: kind, ID: event.ItemID}, event.OutputIndex, explicit, false)
		if slot == nil {
			return
		}
		slot.item = mergeBufferedItem(slot.item, ResponsesOutput{Type: kind, ID: event.ItemID})
		if kind == "message" && slot.item.Role == "" {
			slot.item.Role = "assistant"
		}
		a.part(slot, event.ContentIndex, part, rank, delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		text := event.Text
		rank := 2
		delta := strings.HasSuffix(event.Type, ".delta")
		if delta {
			text = event.Delta
			rank = 1
			if text == "" {
				return
			}
		}
		if event.Part != nil {
			text = event.Part.Text
		}
		if strings.HasSuffix(event.Type, ".added") {
			rank = 1
		}
		slot := a.slot(ResponsesOutput{Type: "reasoning", ID: event.ItemID}, event.OutputIndex, explicit, false)
		if slot == nil {
			return
		}
		slot.item = mergeBufferedItem(slot.item, ResponsesOutput{Type: "reasoning", ID: event.ItemID})
		a.summary(slot, event.SummaryIndex, text, rank, delta)
	case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		kind := "function_call"
		custom := strings.HasPrefix(event.Type, "response.custom_tool_call_input")
		if custom {
			kind = "custom_tool_call"
		}
		slot := a.slot(ResponsesOutput{Type: kind, ID: event.ItemID, CallID: event.CallID, Name: event.Name}, event.OutputIndex, explicit, false)
		if slot == nil {
			return
		}
		slot.item = mergeBufferedItem(slot.item, ResponsesOutput{Type: kind, ID: event.ItemID, CallID: event.CallID, Name: event.Name})
		value := event.Arguments
		present := event.ArgumentsPresent || value != ""
		if custom {
			value = event.Input
			present = event.InputPresent || value != ""
		}
		if strings.HasSuffix(event.Type, ".delta") {
			if slot.argsRank > 1 {
				return
			}
			if custom {
				slot.item.Input += event.Delta
			} else {
				slot.item.Arguments += event.Delta
			}
			slot.argsRank = 1
		} else if present && slot.argsRank <= 2 {
			if custom {
				slot.item.Input = value
			} else {
				slot.item.Arguments = value
			}
			slot.argsRank = 2
		}
	}
}

func bufferedItemHasContent(slot *bufferedOutputSlot) bool {
	if slot.item.Type != "message" && slot.item.Type != "reasoning" {
		return slot.item.Type != ""
	}
	if slot.item.ID != "" || slot.item.Status != "" || len(slot.item.encryptedContentRaw) > 0 || slot.item.EncryptedContent != "" {
		return true
	}
	for _, part := range slot.parts {
		if part.Text != "" || part.Refusal != "" {
			return true
		}
	}
	for _, part := range slot.summaries {
		if part.Text != "" {
			return true
		}
	}
	return false
}

func (a *BufferedResponseAccumulator) HasContent() bool {
	for _, slot := range a.slots {
		if bufferedItemHasContent(slot) {
			return true
		}
	}
	return false
}

func (a *BufferedResponseAccumulator) BuildOutput() []ResponsesOutput {
	slots := append([]*bufferedOutputSlot(nil), a.slots...)
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].index == slots[j].index {
			return slots[i].explicit && !slots[j].explicit
		}
		return slots[i].index < slots[j].index
	})
	var output []ResponsesOutput
	for _, slot := range slots {
		if !bufferedItemHasContent(slot) {
			continue
		}
		item := slot.item
		indices := make([]int, 0, len(slot.parts))
		for i := range slot.parts {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			item.Content = append(item.Content, slot.parts[i])
		}
		indices = indices[:0]
		for i := range slot.summaries {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			item.Summary = append(item.Summary, slot.summaries[i])
		}
		output = append(output, item)
	}
	return output
}

func (a *BufferedResponseAccumulator) SupplementResponseOutput(resp *ResponsesResponse) {
	if resp == nil {
		return
	}
	for i, item := range resp.Output {
		a.snapshot(item, i, true, 3)
	}
	if a.err != nil {
		return
	}
	if a.HasContent() {
		resp.Output = a.BuildOutput()
	}
	if err := validateResponsesChatTools(resp); err != nil {
		a.err = err
	}
}
