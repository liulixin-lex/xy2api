package apicompat

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// Pointers retain explicit zero across cumulative stream updates.
type AnthropicCacheCreation struct {
	Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens,omitempty"`
}

func (u *AnthropicUsage) UnmarshalJSON(data []byte) error {
	type wire AnthropicUsage
	var v wire
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*u = AnthropicUsage(v)
	root := gjson.ParseBytes(data)
	u.InputTokensPresent = root.Get("input_tokens").Type == gjson.Number
	u.OutputTokensPresent = root.Get("output_tokens").Type == gjson.Number
	u.CacheReadInputTokensPresent = root.Get("cache_read_input_tokens").Type == gjson.Number
	u.CacheCreationInputTokensPresent = root.Get("cache_creation_input_tokens").Type == gjson.Number
	u.InputTokens = max(u.InputTokens, 0)
	u.OutputTokens = max(u.OutputTokens, 0)
	u.CacheReadInputTokens = max(u.CacheReadInputTokens, 0)
	u.CacheCreationInputTokens = max(u.CacheCreationInputTokens, 0)
	if u.CacheCreation != nil {
		if p := u.CacheCreation.Ephemeral5mInputTokens; p != nil {
			*p = max(*p, 0)
		}
		if p := u.CacheCreation.Ephemeral1hInputTokens; p != nil {
			*p = max(*p, 0)
		}
	}
	return nil
}

// MergeAnthropicCacheUsage preserves provider presence independently from a
// normalized aggregate. Events contain cumulative values, never increments.
func MergeAnthropicCacheUsage(dst *AnthropicUsage, src AnthropicUsage) {
	if src.CacheCreationInputTokensPresent || src.CacheCreationInputTokens > 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		dst.CacheCreationInputTokensPresent = true
	}
	if src.CacheCreation != nil {
		if dst.CacheCreation == nil {
			dst.CacheCreation = &AnthropicCacheCreation{}
		}
		if p := src.CacheCreation.Ephemeral5mInputTokens; p != nil {
			v := *p
			dst.CacheCreation.Ephemeral5mInputTokens = &v
		}
		if p := src.CacheCreation.Ephemeral1hInputTokens; p != nil {
			v := *p
			dst.CacheCreation.Ephemeral1hInputTokens = &v
		}
	}
	if !dst.CacheCreationInputTokensPresent && dst.CacheCreation != nil {
		dst.CacheCreationInputTokens = 0
		if p := dst.CacheCreation.Ephemeral5mInputTokens; p != nil {
			dst.CacheCreationInputTokens += *p
		}
		if p := dst.CacheCreation.Ephemeral1hInputTokens; p != nil {
			dst.CacheCreationInputTokens += *p
		}
	}
}
