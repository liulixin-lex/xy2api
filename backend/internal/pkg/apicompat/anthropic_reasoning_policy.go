package apicompat

import (
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
)

// Reasoning is a gateway extension: official SDK compatibility ignores it.
// Keep the existing Claude controls and map legal lower efforts to the nearest
// supported level instead of rejecting an otherwise valid OpenAI request.
func mapAnthropicReasoning(req *ResponsesRequest, out *AnthropicRequest) error {
	effort := ""
	if len(normalizedRawJSON(req.anthropicAliases.ReasoningEffort)) > 0 && req.Reasoning == nil {
		if json.Unmarshal(req.anthropicAliases.ReasoningEffort, &effort) != nil {
			return unsupportedConversion("reasoning_effort", "expected a reasoning effort string")
		}
	}
	if req.Reasoning != nil {
		effort = req.Reasoning.Effort
	}
	switch effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return unsupportedConversion("reasoning.effort", "expected none, minimal, low, medium, high or xhigh (max is also supported by the gateway)")
	}
	if spec, ok := claude.Claude55(req.Model); ok {
		if effort == "" {
			effort = spec.DefaultEffort
		}
		if effort == "none" && spec.BetweenTools {
			out.Thinking = &AnthropicThinking{Type: "between_tools"}
			effort = "low"
		} else {
			if effort == "none" || effort == "minimal" {
				effort = "low"
			}
			out.Thinking = &AnthropicThinking{Type: "adaptive"}
		}
		if out.OutputConfig == nil {
			out.OutputConfig = &AnthropicOutputConfig{}
		}
		out.OutputConfig.Effort = effort
		return nil
	}
	if effort == "" {
		return nil
	}
	if effort == "none" {
		out.Thinking = &AnthropicThinking{Type: "disabled"}
		return nil
	}
	if effort == "minimal" {
		effort = "low"
	}
	if effort == "xhigh" {
		effort = "max"
	}
	if out.OutputConfig == nil {
		out.OutputConfig = &AnthropicOutputConfig{}
	}
	out.OutputConfig.Effort = effort
	if effort != "low" {
		if out.MaxTokens <= 1024 {
			return unsupportedConversion("max_output_tokens", "limit cannot accommodate Anthropic's minimum thinking budget")
		}
		budget := map[string]int{"medium": 4096, "high": 10240, "max": 32768}[effort]
		out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: min(budget, out.MaxTokens-1)}
	}
	return nil
}
