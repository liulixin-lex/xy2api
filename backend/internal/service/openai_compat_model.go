package service

import (
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
)

func NormalizeOpenAICompatRequestedModel(model string) string {
	if openai.IsGPT61SolModelSpelling(model) {
		return "gpt-6.1-sol"
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}

	normalized, _, ok := splitOpenAICompatReasoningModel(trimmed)
	if !ok || normalized == "" {
		return trimmed
	}
	return normalized
}

func applyOpenAICompatModelNormalization(req *apicompat.AnthropicRequest) {
	if req != nil && openai.IsGPT61SolModelSpelling(req.Model) {
		canonical := openai.CanonicalizeOpenAIModelAliasSpelling(req.Model)
		if effort, ok := strings.CutPrefix(canonical, "gpt-6.1-sol-"); ok && effort != "openai-compact" {
			req.Model = "gpt-6.1-sol"
			if req.OutputConfig == nil {
				req.OutputConfig = &apicompat.AnthropicOutputConfig{}
			}
			if req.OutputConfig.Effort == "" {
				req.OutputConfig.Effort = effort
			}
			return
		}
	}

	if req == nil {
		return
	}

	originalModel := strings.TrimSpace(req.Model)
	if originalModel == "" {
		return
	}

	normalizedModel, derivedEffort, hasReasoningSuffix := splitOpenAICompatReasoningModel(originalModel)
	if hasReasoningSuffix && normalizedModel != "" {
		req.Model = normalizedModel
	}

	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		return
	}

	claudeEffort := openAIReasoningEffortToClaudeOutputEffort(derivedEffort)
	if claudeEffort == "" {
		return
	}

	if req.OutputConfig == nil {
		req.OutputConfig = &apicompat.AnthropicOutputConfig{}
	}
	req.OutputConfig.Effort = claudeEffort
}

func splitOpenAICompatReasoningModel(model string) (normalizedModel string, reasoningEffort string, ok bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", "", false
	}

	modelID := trimmed
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}
	modelID = strings.TrimSpace(modelID)
	if !strings.HasPrefix(strings.ToLower(modelID), "gpt-") {
		return trimmed, "", false
	}

	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return trimmed, "", false
	}

	last := strings.NewReplacer("-", "", "_", "", " ", "").Replace(parts[len(parts)-1])
	switch last {
	case "none", "minimal":
	case "low", "medium", "high":
		reasoningEffort = last
	case "xhigh", "extrahigh":
		reasoningEffort = "xhigh"
	default:
		return trimmed, "", false
	}

	return normalizeCodexModel(modelID), reasoningEffort, true
}

func openAIReasoningEffortToClaudeOutputEffort(effort string) string {
	switch strings.TrimSpace(effort) {
	case "low", "medium", "high":
		return effort
	case "xhigh":
		return "max"
	default:
		return ""
	}
}

// openAICompatAnthropicReasoningEffort resolves the effort emitted by the
// Anthropic bridge after the final upstream model is known. Anthropic's max is
// normally translated to OpenAI xhigh, but GPT-5.6 accepts the original max
// value on Responses and Chat Completions.
func openAICompatAnthropicReasoningEffort(req *apicompat.AnthropicRequest, upstreamModel, convertedEffort string) string {
	if convertedEffort == "none" {
		return convertedEffort
	}
	if req == nil || req.OutputConfig == nil || !strings.EqualFold(strings.TrimSpace(req.OutputConfig.Effort), "max") {
		return convertedEffort
	}
	if normalized := normalizeOpenAICompatOutboundEffort(req.OutputConfig.Effort, upstreamModel); normalized != "" {
		return normalized
	}
	return convertedEffort
}

// Preserve provider request normalization independently of wire-only usage reading.
func normalizeOpenAICompatOutboundEffort(raw, model string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "none") && openai.IsGPT6SolOrLunaModelSpelling(model) {
		return "none"
	}
	if strings.EqualFold(strings.TrimSpace(raw), "max") && supportsOpenAICompatMax(model) {
		return "max"
	}
	return normalizeOpenAIReasoningEffort(raw)
}

func supportsOpenAICompatMax(model string) bool {
	if isOpenAIGPT6Model(model) || isOpenAIGPT56Model(model) {
		return true
	}

	normalized := strings.ToLower(lastOpenAIModelSegment(model))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch {
	case strings.HasPrefix(normalized, "deepseek-v4"), strings.HasPrefix(normalized, "deepseek-flash"):
		// deepseek-flash（= DeepSeek-V4.1-Flash）与 v4 系同为 low/high/max 档位。
		return true
	case strings.HasPrefix(normalized, "glm-"):
		return true
	case strings.HasPrefix(normalized, "kimi-"), strings.HasPrefix(normalized, "moonshot-"):
		return true
	case normalized == "k3" || strings.HasPrefix(normalized, "k3-"):
		return true
	default:
		return false
	}
}
