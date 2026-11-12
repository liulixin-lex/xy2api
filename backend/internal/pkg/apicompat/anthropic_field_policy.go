package apicompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
)

type anthropicFieldPolicy struct {
	field string
	apply func(*ResponsesRequest, *AnthropicRequest) error
}

// One policy for OpenAI requests sent to Anthropic, based on:
// https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk
// Responses-only fields use the corresponding Chat semantics. This table is
// never applied to requests sent to an OpenAI upstream.
var anthropicFieldPolicies = []anthropicFieldPolicy{
	{"model", nil},
	{"messages/input/instructions", nil},
	{"max_tokens/max_completion_tokens/max_output_tokens", mapAnthropicTokenLimit},
	{"stream/stream_options", nil}, // Usage events are emitted by the response bridge.
	{"temperature/top_p", mapAnthropicSampling},
	{"stop", mapAnthropicStops},
	{"tools/functions", mapAnthropicTools},
	{"tool_choice/function_call", mapAnthropicToolChoice},
	{"parallel_tool_calls", mapAnthropicParallelTools},
	{"response_format/text.format", mapAnthropicResponseFormat}, // Gateway extension: retain structured outputs and explicit text mode.
	{"reasoning_effort/reasoning", mapAnthropicReasoning},       // Gateway extension over the official ignored field.
	{"n", validateAnthropicN},                                   // Only an explicit 1 is representable.
	// Officially ignored: logprobs, top_logprobs, seed, presence_penalty,
	// frequency_penalty, logit_bias, user, metadata, prediction, service_tier,
	// audio, store, modalities and function strict. None enter AnthropicRequest.
}

func applyAnthropicFieldPolicies(req *ResponsesRequest, out *AnthropicRequest) error {
	for _, policy := range anthropicFieldPolicies {
		if policy.apply == nil {
			continue
		}
		if err := policy.apply(req, out); err != nil {
			var unsupported *UnsupportedConversionError
			if req.anthropicChatSource != nil && errors.As(err, &unsupported) {
				field := unsupported.Field
				field = strings.Replace(field, "text.format", "response_format", 1)
				field = strings.Replace(field, "reasoning.effort", "reasoning_effort", 1)
				return unsupportedConversion(field, unsupported.Reason)
			}
			return err
		}
	}
	return nil
}

// n must survive decoding and the Chat -> Responses intermediate, without
// changing native OpenAI serialization or the reverse converters.
func (r *ChatCompletionsRequest) UnmarshalJSON(data []byte) error {
	type wire ChatCompletionsRequest
	aux := struct {
		*wire
		N json.RawMessage `json:"n"`
	}{wire: (*wire)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.anthropicN = aux.N
	type rawTool struct {
		Type         string                 `json:"type"`
		Name         string                 `json:"name"`
		Function     *ChatFunction          `json:"function"`
		InputSchema  json.RawMessage        `json:"input_schema"`
		Parameters   json.RawMessage        `json:"parameters"`
		CacheControl *AnthropicCacheControl `json:"cache_control"`
		Custom       json.RawMessage        `json:"custom"`
		DeferLoading *bool                  `json:"defer_loading"`
	}
	var tools []rawTool
	if err := json.Unmarshal(data, &struct {
		Tools *[]rawTool `json:"tools"`
	}{Tools: &tools}); err != nil {
		return err
	}
	r.anthropicToolNames = make([]string, len(tools))
	r.anthropicDroppedToolNames = nil
	r.anthropicNativeTools = nil
	for i, tool := range tools {
		r.anthropicToolNames[i] = tool.Name
		if nativeAnthropicToolType.MatchString(tool.Type) {
			schema := tool.InputSchema
			if len(normalizedRawJSON(schema)) == 0 {
				schema = tool.Parameters
			}
			if r.anthropicNativeTools == nil {
				r.anthropicNativeTools = make(map[int]ResponsesTool)
			}
			r.anthropicNativeTools[i] = ResponsesTool{Type: tool.Type, Name: tool.Name, Parameters: schema, CacheControl: tool.CacheControl, Custom: tool.Custom, DeferLoading: tool.DeferLoading}
		}
		if (tool.Type == "function" && tool.Function != nil) || knownAnthropicServerTool(tool.Type) || nativeAnthropicToolType.MatchString(tool.Type) {
			continue
		}
		name := tool.Name
		if tool.Function != nil {
			name = tool.Function.Name
		}
		if name != "" {
			if r.anthropicDroppedToolNames == nil {
				r.anthropicDroppedToolNames = make(map[string]bool)
			}
			r.anthropicDroppedToolNames[name] = true
		}
	}
	return nil
}

func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	type wire ResponsesRequest
	aux := struct {
		*wire
		N                   json.RawMessage `json:"n"`
		Stop                json.RawMessage `json:"stop"`
		ResponseFormat      json.RawMessage `json:"response_format"`
		MaxTokens           json.RawMessage `json:"max_tokens"`
		MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
		ReasoningEffort     json.RawMessage `json:"reasoning_effort"`
	}{wire: (*wire)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.anthropicN = aux.N
	r.anthropicChatSource = nil
	r.anthropicAliases = anthropicRequestAliases{Stop: aux.Stop, ResponseFormat: aux.ResponseFormat, MaxTokens: aux.MaxTokens, MaxCompletionTokens: aux.MaxCompletionTokens, ReasoningEffort: aux.ReasoningEffort}
	return nil
}

type anthropicRequestAliases struct {
	ReasoningEffort     json.RawMessage
	Stop                json.RawMessage
	ResponseFormat      json.RawMessage
	MaxTokens           json.RawMessage
	MaxCompletionTokens json.RawMessage
}

func validateAnthropicN(req *ResponsesRequest, _ *AnthropicRequest) error {
	if len(normalizedRawJSON(req.anthropicN)) == 0 {
		return nil
	}
	var n int
	if json.Unmarshal(req.anthropicN, &n) != nil || n != 1 {
		return unsupportedConversion("n", "Anthropic supports exactly one completion; omit n or set it to 1")
	}
	return nil
}

func mapAnthropicTokenLimit(req *ResponsesRequest, out *AnthropicRequest) error {
	limit := req.MaxOutputTokens
	field := "max_output_tokens"
	if limit == nil {
		alias := req.anthropicAliases.MaxTokens
		field = "max_tokens"
		if len(normalizedRawJSON(req.anthropicAliases.MaxCompletionTokens)) > 0 {
			alias = req.anthropicAliases.MaxCompletionTokens
			field = "max_completion_tokens"
		}
		if len(normalizedRawJSON(alias)) > 0 {
			var n int
			if json.Unmarshal(alias, &n) != nil {
				return unsupportedConversion(field, "expected an integer token limit")
			}
			limit = &n
		}
	}
	if err := validateTokenLimit(field, limit); err != nil {
		return err
	}
	out.MaxTokens = 8192
	if limit != nil {
		out.MaxTokens = *limit
	}
	return nil
}

// The official SDK compatibility docs require default sampling on Claude 4.7+
// and the 5.5 specs have model-specific limits. Drop rejected knobs instead of
// rejecting ordinary clients that always send their sampling defaults.
func mapAnthropicSampling(req *ResponsesRequest, out *AnthropicRequest) error {
	temperature, topP := req.Temperature, req.TopP
	if req.anthropicChatSource != nil {
		temperature, topP = req.anthropicChatSource.Temperature, req.anthropicChatSource.TopP
	}
	if temperature != nil {
		v := *temperature
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return unsupportedConversion("temperature", "expected a nonnegative finite value")
		}
		v = min(v, 1)
		temperature = &v
	}
	if spec, ok := claude.Claude55(req.Model); ok && spec.Sampling != claude.Claude55SamplingUpstream {
		if temperature != nil && *temperature != 1 {
			temperature = nil
		}
		if topP != nil && (spec.Sampling == claude.Claude55SamplingDefaultOnly && (*topP < 0.99 || *topP > 1) || spec.Sampling == claude.Claude55SamplingDefaultExclusive && *topP != 0.99) {
			topP = nil
		}
		if spec.Sampling == claude.Claude55SamplingDefaultExclusive && temperature != nil {
			topP = nil
		}
	} else if claude.RejectsNonDefaultSampling(req.Model) {
		if temperature != nil && *temperature != 1 {
			temperature = nil
		}
		if topP != nil && *topP != 0.99 {
			topP = nil
		}
		if temperature != nil {
			topP = nil
		}
	}
	out.Temperature, out.TopP = temperature, topP
	return nil
}

func mapAnthropicStops(req *ResponsesRequest, out *AnthropicRequest) error {
	raw := req.bridgeStop
	if len(raw) == 0 {
		raw = req.anthropicAliases.Stop
	}
	stops, err := parseStopSequences(raw)
	if err != nil {
		return err
	}
	out.StopSeqs = stops[:0]
	for _, stop := range stops {
		if strings.TrimSpace(stop) != "" {
			out.StopSeqs = append(out.StopSeqs, stop)
		}
	}
	return nil
}

func mapAnthropicTools(req *ResponsesRequest, out *AnthropicRequest) error {
	tools := req.Tools
	var paths []string
	if source := req.anthropicChatSource; source != nil {
		tools = nil
		for i, tool := range source.Tools {
			field := fmt.Sprintf("tools[%d]", i)
			var converted []ResponsesTool
			if nativeAnthropicToolType.MatchString(tool.Type) {
				if native, ok := source.anthropicNativeTools[i]; ok {
					converted = []ResponsesTool{native}
				} else {
					name := ""
					if i < len(source.anthropicToolNames) {
						name = source.anthropicToolNames[i]
					}
					converted = []ResponsesTool{{Type: tool.Type, Name: name, CacheControl: tool.CacheControl, Custom: tool.Custom, DeferLoading: tool.DeferLoading}}
				}
			} else {
				converted = convertChatToolsToResponses([]ChatTool{tool}, nil)
			}
			if len(converted) == 0 {
				logDroppedAnthropicTool(field+".type", tool.Type)
				continue
			}
			if tool.Type == "function" {
				field += ".function"
			}
			tools = append(tools, converted...)
			paths = append(paths, field)
		}
		for i, f := range source.Functions {
			tools = append(tools, convertChatToolsToResponses(nil, []ChatFunction{f})...)
			paths = append(paths, fmt.Sprintf("functions[%d]", i))
		}
	}
	for i, tool := range tools {
		field := fmt.Sprintf("tools[%d]", i)
		if len(paths) > i {
			field = paths[i]
		}
		if knownAnthropicServerTool(tool.Type) || nativeAnthropicToolType.MatchString(tool.Type) {
			typ, name := tool.Type, tool.Name
			if typ == "web_search" || typ == "google_search" || typ == "web_search_20250305" {
				typ, name = "web_search_20250305", "web_search"
			}
			server := AnthropicTool{CacheControl: tool.CacheControl, DeferLoading: tool.DeferLoading, Custom: tool.Custom, Type: typ, Name: name}
			if typ == "code_execution" || typ != "web_search_20250305" && nativeAnthropicToolType.MatchString(typ) {
				var err error
				server.Description = tool.Description
				server.InputSchema, err = anthropicToolSchema(tool.Parameters, field+".parameters")
				if err != nil {
					return err
				}
			}
			out.Tools = append(out.Tools, server)
			continue
		}
		if tool.Type != "function" && tool.Type != "custom" {
			logDroppedAnthropicTool(field+".type", tool.Type)
			continue
		}
		schema, err := anthropicToolSchema(tool.Parameters, field+".parameters")
		if err != nil {
			return err
		}
		out.Tools = append(out.Tools, AnthropicTool{CacheControl: tool.CacheControl,
			DeferLoading: tool.DeferLoading, Custom: tool.Custom,
			Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	return nil
}

func logDroppedAnthropicTool(field, toolType string) {
	slog.Debug("dropped unsupported tool in Anthropic conversion", "field", field, "tool_type", toolType)
}

var nativeAnthropicToolType = regexp.MustCompile(`^[a-z][a-z0-9_]*_[0-9]{8}$`)

// These server-tool mappings predate the field policy and remain gateway
// extensions. Unknown types must never be passed through to Anthropic.
func knownAnthropicServerTool(typ string) bool {
	switch typ {
	case "web_search", "google_search", "web_search_20250305", "code_execution":
		return true
	}
	return false
}

func mapAnthropicToolChoice(req *ResponsesRequest, out *AnthropicRequest) error {
	if len(normalizedRawJSON(req.ToolChoice)) == 0 {
		return nil
	}
	if droppedAnthropicToolChoice(req) {
		return unsupportedConversion("tool_choice", "the selected tool was dropped because Anthropic does not support its tool type")
	}
	choice, err := convertResponsesToAnthropicToolChoice(req.ToolChoice)
	if err != nil {
		return err
	}
	var value struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(choice, &value)
	if spec, ok := claude.Claude55(req.Model); ok && !spec.ForcedToolChoice && (value.Type == "any" || value.Type == "tool") {
		return unsupportedConversion("tool_choice", "this model does not support forced tool_choice; use auto or none")
	}
	out.ToolChoice = choice
	return nil
}

func droppedAnthropicToolChoice(req *ResponsesRequest) bool {
	var choice struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(req.ToolChoice, &choice) != nil || choice.Type == "allowed_tools" {
		return false
	}
	name := strings.TrimSpace(choice.Name)
	if name == "" {
		name = strings.TrimSpace(choice.Function.Name)
	}
	selected := func(typ, toolName string) bool {
		if name != "" {
			return name == toolName
		}
		return choice.Type != "" && choice.Type == typ
	}
	if source := req.anthropicChatSource; source != nil {
		if source.anthropicDroppedToolNames[name] {
			return true
		}
		for _, tool := range source.Tools {
			toolName := ""
			if tool.Function != nil {
				toolName = tool.Function.Name
			}
			if selected(tool.Type, toolName) && (tool.Type != "function" || tool.Function == nil) && !knownAnthropicServerTool(tool.Type) && !nativeAnthropicToolType.MatchString(tool.Type) {
				return true
			}
		}
		return false
	}
	for _, tool := range req.Tools {
		if selected(tool.Type, tool.Name) && tool.Type != "function" && tool.Type != "custom" && !knownAnthropicServerTool(tool.Type) && !nativeAnthropicToolType.MatchString(tool.Type) {
			return true
		}
	}
	return false
}

func mapAnthropicParallelTools(req *ResponsesRequest, out *AnthropicRequest) error {
	if req.ParallelToolCalls == nil || len(out.Tools) == 0 {
		return nil
	}
	choice := map[string]any{"type": "auto"}
	if len(out.ToolChoice) > 0 {
		if err := json.Unmarshal(out.ToolChoice, &choice); err != nil {
			return err
		}
	}
	if choice["type"] != "none" {
		choice["disable_parallel_tool_use"] = !*req.ParallelToolCalls
	}
	out.ToolChoice, _ = json.Marshal(choice)
	return nil
}

func mapAnthropicResponseFormat(req *ResponsesRequest, out *AnthropicRequest) error {
	var raw json.RawMessage
	if req.Text != nil {
		raw = req.Text.Format
	}
	if len(normalizedRawJSON(raw)) == 0 && len(normalizedRawJSON(req.anthropicAliases.ResponseFormat)) > 0 {
		converted, err := chatResponseFormatToResponsesTextFormat(req.anthropicAliases.ResponseFormat)
		if err != nil {
			return err
		}
		raw = converted
	}
	format, err := responsesTextFormatToAnthropic(raw)
	if err != nil {
		return err
	}
	if len(format) > 0 {
		out.OutputConfig = &AnthropicOutputConfig{Format: format}
	}
	return nil
}
