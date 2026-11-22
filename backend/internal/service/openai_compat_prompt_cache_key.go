package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/apicompat"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai"
)

const compatPromptCacheKeyPrefix = "compat_cc_"

func shouldAutoInjectPromptCacheKeyForCompat(model string) bool {
	return openai.GPT62ModelID(model) != "" || shouldApplyCompatMessagesReplayPolicy(model)
}

// History trimming, previous_response_id and the todo guard share the legacy
// Messages replay contract. A cache identity alone needs none of these.
func shouldApplyCompatMessagesReplayPolicy(model string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	canonical := canonicalizeOpenAIModelAliasSpelling(trimmed)
	// GPT-6 Sol/Luna share the supported Responses cache identity contract.
	if canonical == "gpt-6" || canonical == "gpt-6-astra" || openai.IsGPT6SolOrLunaModelSpelling(canonical) || openai.IsGPT61SolModelSpelling(canonical) || openai.IsGPT61TerraModelSpelling(canonical) {
		return true
	}
	// 仅对 Responses 兼容路径支持的 GPT-5 族开启自动注入，避免 normalizeCodexModel
	// 的默认兜底把任意模型（如 gpt-4o、claude-*）误判为 gpt-5.4。
	if !strings.Contains(trimmed, "gpt-5") && !strings.Contains(trimmed, "codex") {
		return false
	}
	normalized := strings.TrimSpace(strings.ToLower(normalizeCodexModel(trimmed)))
	return strings.HasPrefix(normalized, "gpt-5") || strings.Contains(normalized, "codex")
}

func gpt62MessagesCacheKey(req apicompat.AnthropicRequest, model string, apiKeyID int64) string {
	seed := promptCacheKeyFromAnthropicMetadataSession(&req)
	if seed == "" {
		var user any
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				user = gpt62CacheContent(msg.Content, false)
				break
			}
		}
		tools := make([]apicompat.AnthropicTool, len(req.Tools))
		copy(tools, req.Tools)
		for i := range tools {
			tools[i].CacheControl = nil
		}
		parts, _ := json.Marshal([]any{gpt62CacheContent(req.System, true), tools, user})
		seed = string(parts)
	}
	return isolateOpenAISessionID(apiKeyID, model+"|"+seed)
}

// Canonicalize only Anthropic content blocks. A cache_control property in a
// tool's schema or a tool_use input is application data.
func gpt62CacheContent(raw json.RawMessage, system bool) any {
	var value any
	if len(raw) == 0 || decodeOpenAIJSONUseNumber(raw, &value) != nil {
		return nil
	}
	return gpt62CacheBlocks(value, system)
}

func gpt62CacheBlocks(value any, system bool) any {
	if text, ok := value.(string); ok {
		value = []any{map[string]any{"type": "text", "text": text}}
	}
	blocks, ok := value.([]any)
	if !ok {
		return value
	}
	stable := blocks[:0]
	for _, item := range blocks {
		if block, ok := item.(map[string]any); ok {
			delete(block, "cache_control")
			if system && block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					clean := stripSessionBillingHeader(text)
					if clean != text && clean == "" {
						continue
					}
					block["text"] = clean
				}
			}
			if block["type"] == "tool_result" {
				block["content"] = gpt62CacheBlocks(block["content"], false)
			}
		}
		stable = append(stable, item)
	}
	return stable
}
func deriveCompatPromptCacheKey(req *apicompat.ChatCompletionsRequest, mappedModel string) string {
	if req == nil {
		return ""
	}

	normalizedModel := normalizeCodexModel(strings.TrimSpace(mappedModel))
	if normalizedModel == "" {
		normalizedModel = normalizeCodexModel(strings.TrimSpace(req.Model))
	}
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(req.Model)
	}

	seedParts := []string{"model=" + normalizedModel}
	if req.ReasoningEffort != "" {
		seedParts = append(seedParts, "reasoning_effort="+strings.TrimSpace(req.ReasoningEffort))
	}
	if len(req.ToolChoice) > 0 {
		seedParts = append(seedParts, "tool_choice="+normalizeCompatSeedJSON(req.ToolChoice))
	}
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			seedParts = append(seedParts, "tools="+normalizeCompatSeedJSON(raw))
		}
	}
	if len(req.Functions) > 0 {
		if raw, err := json.Marshal(req.Functions); err == nil {
			seedParts = append(seedParts, "functions="+normalizeCompatSeedJSON(raw))
		}
	}

	firstUserCaptured := false
	prefixOpen := true
	for _, msg := range req.Messages {
		switch strings.TrimSpace(msg.Role) {
		case "system", "developer":
			if prefixOpen {
				seedParts = append(seedParts, msg.Role+"="+normalizeCompatSeedJSON(msg.Content))
			}
		case "user":
			prefixOpen = false
			if !firstUserCaptured {
				seedParts = append(seedParts, "first_user="+normalizeCompatSeedJSON(msg.Content))
				firstUserCaptured = true
			}
		default:
			prefixOpen = false
		}
	}

	return compatPromptCacheKeyPrefix + hashSensitiveValueForLog(strings.Join(seedParts, "|"))
}

func deriveAnthropicCompatPromptCacheKey(req *apicompat.AnthropicRequest, mappedModel string) string {
	if req == nil {
		return ""
	}
	if anchorKey := deriveAnthropicCacheControlPromptCacheKey(req); anchorKey != "" {
		return anchorKey
	}

	normalizedModel := normalizeCodexModel(strings.TrimSpace(mappedModel))
	if normalizedModel == "" {
		normalizedModel = normalizeCodexModel(strings.TrimSpace(req.Model))
	}
	if normalizedModel == "" {
		normalizedModel = strings.TrimSpace(req.Model)
	}

	seedParts := []string{"model=" + normalizedModel}
	if req.OutputConfig != nil && strings.TrimSpace(req.OutputConfig.Effort) != "" {
		seedParts = append(seedParts, "effort="+strings.TrimSpace(req.OutputConfig.Effort))
	}
	if len(req.ToolChoice) > 0 {
		seedParts = append(seedParts, "tool_choice="+normalizeCompatSeedJSON(req.ToolChoice))
	}
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			seedParts = append(seedParts, "tools="+normalizeCompatSeedJSON(raw))
		}
	}
	if len(req.System) > 0 {
		seedParts = append(seedParts, "system="+normalizeCompatSeedJSON(req.System))
	}

	firstUserCaptured := false
	for _, msg := range req.Messages {
		if strings.TrimSpace(msg.Role) != "user" || firstUserCaptured {
			continue
		}
		seedParts = append(seedParts, "first_user="+normalizeCompatSeedJSON(msg.Content))
		firstUserCaptured = true
	}

	return compatPromptCacheKeyPrefix + hashSensitiveValueForLog(strings.Join(seedParts, "|"))
}

func deriveAnthropicCacheControlPromptCacheKey(req *apicompat.AnthropicRequest) string {
	if req == nil {
		return ""
	}

	var parts []string
	var systemBlocks []apicompat.AnthropicContentBlock
	if len(req.System) > 0 && json.Unmarshal(req.System, &systemBlocks) == nil {
		for _, block := range systemBlocks {
			if block.Type == "text" &&
				block.CacheControl != nil &&
				strings.TrimSpace(block.CacheControl.Type) == "ephemeral" &&
				strings.TrimSpace(block.Text) != "" {
				parts = append(parts, "system:"+strings.TrimSpace(block.Text))
			}
		}
	}

	firstUserAnchor := ""
	for _, msg := range req.Messages {
		var blocks []apicompat.AnthropicContentBlock
		if len(msg.Content) == 0 || json.Unmarshal(msg.Content, &blocks) != nil {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		for _, block := range blocks {
			if block.Type != "text" ||
				block.CacheControl == nil ||
				strings.TrimSpace(block.CacheControl.Type) != "ephemeral" ||
				strings.TrimSpace(block.Text) == "" {
				continue
			}
			switch role {
			case "user":
				if firstUserAnchor == "" {
					firstUserAnchor = strings.TrimSpace(block.Text)
				}
			case "assistant":
				parts = append(parts, "assistant:"+strings.TrimSpace(block.Text))
			}
		}
	}
	if firstUserAnchor != "" {
		parts = append(parts, "user_anchor:"+firstUserAnchor)
	}
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte("anthropic-cache:" + strings.Join(parts, "\n")))
	return fmt.Sprintf("anthropic-cache-%x", sum[:16])
}

func normalizeCompatSeedJSON(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var tmp any
	if err := json.Unmarshal(v, &tmp); err != nil {
		return string(v)
	}
	out, err := json.Marshal(tmp)
	if err != nil {
		return string(v)
	}
	return string(out)
}
