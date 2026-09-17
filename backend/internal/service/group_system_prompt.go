package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type GroupPromptProtocol string

const (
	GroupPromptChat      GroupPromptProtocol = "chat"
	GroupPromptResponses GroupPromptProtocol = "responses"
	GroupPromptAnthropic GroupPromptProtocol = "anthropic"
	GroupPromptGemini    GroupPromptProtocol = "gemini"
)

type groupSystemPromptContextKey struct{}
type groupSystemPromptPolicy struct {
	config GroupSystemPromptConfig
	model  string
}

func HasGroupSystemPrompts(config GroupSystemPromptConfig) bool {
	if strings.TrimSpace(config.Prompt) != "" {
		return true
	}
	for _, prompt := range config.ModelPrompts {
		if strings.TrimSpace(prompt) != "" {
			return true
		}
	}
	return false
}

// Bind once before routing; fallback accounts and groups must not replace this policy.
func WithGroupSystemPrompt(ctx context.Context, config GroupSystemPromptConfig, model string) context.Context {
	return context.WithValue(ctx, groupSystemPromptContextKey{}, groupSystemPromptPolicy{config.Clone(), strings.TrimSpace(model)})
}

func WithGroupSystemPromptModel(ctx context.Context, model string) context.Context {
	policy, ok := ctx.Value(groupSystemPromptContextKey{}).(groupSystemPromptPolicy)
	if !ok {
		return ctx
	}
	policy.model = strings.TrimSpace(model)
	return context.WithValue(ctx, groupSystemPromptContextKey{}, policy)
}

func ApplyGroupSystemPrompt(ctx context.Context, body []byte, protocol GroupPromptProtocol) ([]byte, error) {
	policy, ok := ctx.Value(groupSystemPromptContextKey{}).(groupSystemPromptPolicy)
	if !ok {
		return body, nil
	}
	if !GroupSystemPromptSupportsRequest(policy.model, body) {
		return body, nil
	}
	return PrependGroupSystemPrompt(body, protocol, policy.config.Resolve(policy.model))
}

func GroupSystemPromptSupportsRequest(model string, body []byte) bool {
	if isOpenAIImageGenerationModel(model) || isImageGenerationModel(model) {
		return false
	}
	for _, path := range []string{"generationConfig.responseModalities", "generation_config.response_modalities", "request.generationConfig.responseModalities"} {
		for _, modality := range gjson.GetBytes(body, path).Array() {
			if strings.EqualFold(modality.String(), "IMAGE") {
				return false
			}
		}
	}
	return true
}

func newGroupPromptAntigravityRequest(ctx context.Context, baseURL, action, token string, body []byte) (*http.Request, error) {
	body, err := ApplyGroupSystemPrompt(ctx, body, GroupPromptGemini)
	if err != nil {
		return nil, err
	}
	return antigravity.NewAPIRequestWithURL(ctx, baseURL, action, token, body)
}

func applyGroupSystemPromptWSMap(ctx context.Context, payload map[string]any) (map[string]any, error) {
	policy, ok := ctx.Value(groupSystemPromptContextKey{}).(groupSystemPromptPolicy)
	if !ok || policy.config.Resolve(policy.model) == "" {
		return payload, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	body, err = ApplyGroupSystemPrompt(ctx, body, GroupPromptResponses)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// The caller supplies an uninjected request on every attempt. No content-based
// deduplication: identical client text must not suppress administrator policy.
func PrependGroupSystemPrompt(body []byte, protocol GroupPromptProtocol, prompt string) ([]byte, error) {
	if strings.TrimSpace(prompt) == "" {
		return body, nil
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, fmt.Errorf("system prompt requires a JSON object request")
	}
	set := func(path string, value any) ([]byte, error) {
		parent := gjson.ParseBytes(body)
		for _, field := range strings.Split(path, ".") {
			count := 0
			parent.ForEach(func(key, _ gjson.Result) bool {
				if strings.EqualFold(key.String(), field) {
					count++
				}
				return true
			})
			if count > 1 {
				return nil, fmt.Errorf("ambiguous %s field", path)
			}
			parent = parent.Get(field)
		}
		return sjson.SetBytes(body, path, value)
	}
	prepend := func(path string, item any, allowString bool, preserveClaudePrefix bool) ([]byte, error) {
		existing := gjson.GetBytes(body, path)
		if allowString && existing.Type == gjson.String {
			return set(path, prompt+"\n\n"+existing.String())
		}
		if existing.Exists() && existing.Type != gjson.Null && !existing.IsArray() {
			return nil, fmt.Errorf("%s must be an array", path)
		}
		items := []json.RawMessage{}
		for _, value := range existing.Array() {
			items = append(items, json.RawMessage(value.Raw))
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		index := 0
		if preserveClaudePrefix {
			for index < len(items) {
				text := gjson.GetBytes(items[index], "text").String()
				if !strings.HasPrefix(text, "x-anthropic-billing-header:") && text != claudeCodeSystemPrompt {
					break
				}
				index++
			}
		}
		items = append(items, nil)
		copy(items[index+1:], items[index:])
		items[index] = encoded
		return set(path, items)
	}
	switch protocol {
	case GroupPromptChat:
		return prepend("messages", map[string]string{"role": "system", "content": prompt}, false, false)
	case GroupPromptResponses:
		instructions := gjson.GetBytes(body, "instructions")
		if instructions.Exists() && instructions.Type != gjson.Null && instructions.Type != gjson.String {
			return nil, fmt.Errorf("instructions must be a string")
		}
		if instructions.String() != "" {
			prompt += "\n\n" + instructions.String()
		}
		return set("instructions", prompt)
	case GroupPromptAnthropic:
		return prepend("system", map[string]string{"type": "text", "text": prompt}, true, true)
	case GroupPromptGemini:
		prefix := ""
		if gjson.GetBytes(body, "request").IsObject() {
			prefix = "request."
		}
		field := prefix + "systemInstruction"
		if !gjson.GetBytes(body, field).Exists() && gjson.GetBytes(body, prefix+"system_instruction").Exists() {
			field = prefix + "system_instruction"
		}
		instruction := gjson.GetBytes(body, field)
		if instruction.Exists() && instruction.Type != gjson.Null && !instruction.IsObject() {
			return nil, fmt.Errorf("systemInstruction must be an object")
		}
		return prepend(field+".parts", map[string]string{"text": prompt}, false, false)
	default:
		return nil, fmt.Errorf("unsupported group system prompt protocol: %s", protocol)
	}
}

func newGroupPromptUpstreamRequest(ctx context.Context, method, url string, body []byte, protocol GroupPromptProtocol) (*http.Request, error) {
	body, err := ApplyGroupSystemPrompt(ctx, body, protocol)
	if err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
}
