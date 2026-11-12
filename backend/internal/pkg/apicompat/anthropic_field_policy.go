package apicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
)

type anthropicFieldHandling uint8

const (
	anthropicMap anthropicFieldHandling = iota
	anthropicIgnore
	anthropicReject
)

type anthropicFieldPolicy struct {
	field    string
	handling anthropicFieldHandling
	apply    func(*ResponsesRequest, *AnthropicRequest) error
}

// One policy for OpenAI requests sent to Anthropic, based on:
// https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk
// Responses-only fields use the corresponding Chat semantics. This table is
// never applied to requests sent to an OpenAI upstream.
var anthropicFieldPolicies = []anthropicFieldPolicy{
	{"model", anthropicMap, nil},
	{"messages/input/instructions", anthropicMap, nil},
	{"max_tokens/max_completion_tokens/max_output_tokens", anthropicMap, mapAnthropicTokenLimit},
	{"stream/stream_options", anthropicMap, nil}, // Usage events are emitted by the response bridge.
	{"temperature", anthropicMap, mapAnthropicTemperature},
	{"top_p", anthropicMap, mapAnthropicTopP},
	{"stop", anthropicMap, mapAnthropicStops},
	{"tools/functions", anthropicMap, mapAnthropicTools},
	{"tool_choice/function_call", anthropicMap, mapAnthropicToolChoice},
	{"parallel_tool_calls", anthropicMap, mapAnthropicParallelTools},
	{"response_format/text.format", anthropicMap, mapAnthropicResponseFormat}, // Gateway extension: retain structured outputs and explicit text mode.
	{"reasoning_effort/reasoning", anthropicMap, mapAnthropicReasoning},       // Gateway extension over the official ignored field.
	{"n", anthropicReject, validateAnthropicN},                                // Only an explicit 1 is representable.
	{"logprobs", anthropicIgnore, nil},
	{"top_logprobs", anthropicIgnore, nil},
	{"seed", anthropicIgnore, nil},
	{"presence_penalty", anthropicIgnore, nil},
	{"frequency_penalty", anthropicIgnore, nil},
	{"logit_bias", anthropicIgnore, nil},
	{"user", anthropicIgnore, nil},
	{"metadata", anthropicIgnore, nil},
	{"prediction", anthropicIgnore, nil},
	{"service_tier", anthropicIgnore, nil},
	{"audio", anthropicIgnore, nil},
	{"store", anthropicIgnore, nil},
	{"modalities", anthropicIgnore, nil},
	{"tools[].function.strict/functions[].strict/tools[].strict", anthropicIgnore, nil},
}

func applyAnthropicFieldPolicies(req *ResponsesRequest, out *AnthropicRequest) error {
	for _, policy := range anthropicFieldPolicies {
		if policy.handling == anthropicIgnore || policy.apply == nil {
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
	if len(bytes.TrimSpace(req.anthropicN)) == 0 {
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

func mapAnthropicTemperature(req *ResponsesRequest, out *AnthropicRequest) error {
	temperature := req.Temperature
	if req.anthropicChatSource != nil {
		temperature = req.anthropicChatSource.Temperature
	}
	if temperature == nil {
		return nil
	}
	v := *temperature
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return unsupportedConversion("temperature", "expected a nonnegative finite value")
	}
	v = min(v, 1)
	if spec, ok := claude.Claude55(req.Model); ok {
		if err := spec.CheckSampling(claude.SamplingFloat(&v), claude.SamplingValue{}, false); err != nil {
			return unsupportedConversion("temperature", err.Error())
		}
	}
	out.Temperature = &v
	return nil
}

func mapAnthropicTopP(req *ResponsesRequest, out *AnthropicRequest) error {
	topP, temperature := req.TopP, req.Temperature
	if req.anthropicChatSource != nil {
		topP, temperature = req.anthropicChatSource.TopP, req.anthropicChatSource.Temperature
	}
	if topP == nil {
		return nil
	}
	if temperature != nil {
		return unsupportedConversion("top_p", "temperature and top_p cannot be set together")
	}
	if spec, ok := claude.Claude55(req.Model); ok {
		if err := spec.CheckSampling(claude.SamplingValue{}, claude.SamplingFloat(topP), false); err != nil {
			return unsupportedConversion("top_p", err.Error())
		}
	}
	out.TopP = topP
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
	for _, stop := range stops {
		if strings.TrimSpace(stop) == "" {
			return unsupportedConversion("stop", "stop sequences must contain non-whitespace characters")
		}
	}
	out.StopSeqs = stops
	return nil
}

func mapAnthropicTools(req *ResponsesRequest, out *AnthropicRequest) error {
	if source := req.anthropicChatSource; source != nil {
		for i, tool := range source.Tools {
			if tool.Type != "function" && knownAnthropicServerTool(tool.Type) {
				continue
			}
			if tool.Type != "function" || tool.Function == nil {
				return unsupportedConversion(fmt.Sprintf("tools[%d].type", i), "Anthropic conversion supports function tools only")
			}
		}
	}
	for i, tool := range req.Tools {
		field := fmt.Sprintf("tools[%d]", i)
		if knownAnthropicServerTool(tool.Type) {
			typ, name := tool.Type, tool.Name
			if typ != "code_execution" {
				typ, name = "web_search_20250305", "web_search"
			}
			server := AnthropicTool{CacheControl: tool.CacheControl, DeferLoading: tool.DeferLoading, Custom: tool.Custom, Type: typ, Name: name}
			if typ == "code_execution" {
				server.InputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out.Tools = append(out.Tools, server)
			continue
		}
		if tool.Type != "function" && (tool.Type != "custom" || len(normalizedRawJSON(tool.Parameters)) == 0) {
			return unsupportedConversion(field+".type", "unsupported tool type for Anthropic conversion")
		}
		if source := req.anthropicChatSource; source != nil {
			if i < len(source.Tools) {
				field += ".function"
			} else {
				field = fmt.Sprintf("functions[%d]", i-len(source.Tools))
			}
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
