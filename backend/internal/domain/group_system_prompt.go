package domain

import (
	"fmt"
	"strings"
)

// GroupSystemPromptConfig is private administrator policy, never a user-facing DTO.
type GroupSystemPromptConfig struct {
	Prompt       string            `json:"prompt"`
	Scope        string            `json:"scope"`
	Models       []string          `json:"models"`
	ModelPrompts map[string]string `json:"model_prompts"`
}

func (c GroupSystemPromptConfig) Clone() GroupSystemPromptConfig {
	result := c
	if result.Scope == "" {
		result.Scope = "all"
	}
	result.Models = append([]string{}, c.Models...)
	result.ModelPrompts = make(map[string]string, len(c.ModelPrompts))
	for model, prompt := range c.ModelPrompts {
		result.ModelPrompts[model] = prompt
	}
	return result
}

func (c GroupSystemPromptConfig) Resolve(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if prompt := c.ModelPrompts[model]; strings.TrimSpace(prompt) != "" {
		return prompt
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return ""
	}
	if c.Scope == "all" || c.Scope == "" {
		return c.Prompt
	}
	if c.Scope == "selected" {
		for _, candidate := range c.Models {
			if candidate == model {
				return c.Prompt
			}
		}
	}
	return ""
}

func NormalizeGroupSystemPromptConfig(c GroupSystemPromptConfig) (GroupSystemPromptConfig, error) {
	result := c.Clone()
	if result.Scope != "all" && result.Scope != "selected" {
		return GroupSystemPromptConfig{}, fmt.Errorf("system prompt scope must be all or selected")
	}
	if strings.TrimSpace(result.Prompt) == "" {
		result.Prompt = ""
	}
	result.Models = []string{}
	seen := map[string]bool{}
	for _, raw := range c.Models {
		model := strings.TrimSpace(raw)
		if model == "" {
			return GroupSystemPromptConfig{}, fmt.Errorf("system prompt model must not be empty")
		}
		if !seen[model] {
			result.Models = append(result.Models, model)
			seen[model] = true
		}
	}
	if result.Prompt != "" && result.Scope == "selected" && len(result.Models) == 0 {
		return GroupSystemPromptConfig{}, fmt.Errorf("select at least one model for the system prompt")
	}
	result.ModelPrompts = map[string]string{}
	seen = map[string]bool{}
	for raw, prompt := range c.ModelPrompts {
		model := strings.TrimSpace(raw)
		if model == "" || seen[model] {
			return GroupSystemPromptConfig{}, fmt.Errorf("model system prompt names must be nonempty and unique")
		}
		seen[model] = true
		if strings.TrimSpace(prompt) != "" {
			result.ModelPrompts[model] = prompt
		}
	}
	return result, nil
}
