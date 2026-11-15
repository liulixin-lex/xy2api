package apicompat

import (
	"encoding/json"
	"slices"

	"github.com/liulixin-lex/xy2api/internal/pkg/claude"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
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
	if req.conversionTarget == "gemini" {
		if effort == "none" {
			out.Thinking = &AnthropicThinking{Type: "disabled"}
		} else if effort != "" {
			if out.OutputConfig == nil {
				out.OutputConfig = &AnthropicOutputConfig{}
			}
			out.OutputConfig.Effort = effort
		}
		return nil
	}
	if effort == "none" && claude.SupportsDisabledThinking(req.Model) {
		out.Thinking = &AnthropicThinking{Type: "disabled"}
		if spec, ok := claude.Claude55(req.Model); ok && spec.DisabledThinkingMaxEffort != "" {
			if out.OutputConfig == nil {
				out.OutputConfig = &AnthropicOutputConfig{}
			}
			out.OutputConfig.Effort = "low"
		}
		return nil
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
			out.Thinking = &AnthropicThinking{Type: claude.ThinkingModeForModel(req.Model)}
		}
		if out.OutputConfig == nil {
			out.OutputConfig = &AnthropicOutputConfig{}
		}
		out.OutputConfig.Effort = effort
		return nil
	}
	if spec, ok := claude.Claude52(req.Model); ok && effort == "" {
		effort = spec.DefaultEffort
	}
	if effort == "" {
		return nil
	}
	if claude.ThinkingModeForModel(req.Model) == "adaptive" {
		if effort == "none" || effort == "minimal" {
			effort = "low"
		}
		levels := claude.EffortLevelsForModel(req.Model)
		if len(levels) > 0 && !slices.Contains(levels, effort) {
			var err error
			effort, err = openai.NearestSupportedReasoningEffort(effort, levels)
			if err != nil {
				return err
			}
		}
		out.Thinking = &AnthropicThinking{Type: "adaptive"}
		if len(levels) > 0 {
			if out.OutputConfig == nil {
				out.OutputConfig = &AnthropicOutputConfig{}
			}
			out.OutputConfig.Effort = effort
		}
		return nil
	}
	if effort == "none" {
		out.Thinking = &AnthropicThinking{Type: "disabled"}
		return nil
	}
	if effort == "minimal" {
		effort = "low"
	}
	levels := claude.EffortLevelsForModel(req.Model)
	if len(levels) == 0 && effort == "xhigh" {
		effort = "max"
	}
	if len(levels) > 0 || !claude.IsClaudeModel(req.Model) {
		if out.OutputConfig == nil {
			out.OutputConfig = &AnthropicOutputConfig{}
		}
		mapped := effort
		if len(levels) > 0 && !slices.Contains(levels, mapped) {
			var err error
			mapped, err = openai.NearestSupportedReasoningEffort(mapped, levels)
			if err != nil {
				return err
			}
		}
		out.OutputConfig.Effort = mapped
	}
	if effort == "xhigh" {
		effort = "max"
	}
	if effort != "low" {
		if out.MaxTokens <= 1024 {
			return unsupportedConversion("max_output_tokens", "limit cannot accommodate Anthropic's minimum thinking budget")
		}
		budget := map[string]int{"medium": 4096, "high": 10240, "max": 32768}[effort]
		out.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: min(budget, out.MaxTokens-1)}
	}
	return nil
}
